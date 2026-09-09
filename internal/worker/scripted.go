package worker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type Script struct {
	Events      []Event
	Disposition Disposition
	Summary     string
}

// ScriptedAdapter is a deterministic worker used to exercise orchestration
// without starting a real provider CLI.
type ScriptedAdapter struct {
	provider string
	scripts  map[Role][]Script

	mu               sync.Mutex
	sessions         map[string]*scriptedSession
	nextScriptByRole map[Role]int
	repeatScript     bool
}

var ErrScriptNotFound = errors.New("worker script not found")
var ErrSessionNotFound = errors.New("worker session not found")
var ErrSessionConflict = errors.New("worker session conflicts with existing session")
var ErrSessionFinished = errors.New("worker session has finished")
var ErrInvalidSessionState = errors.New("worker command is invalid for session state")
var ErrCommandConflict = errors.New("worker command ID reused for a different command")

func NewScriptedAdapter(provider string, scripts map[Role]Script) *ScriptedAdapter {
	queuedScripts := make(map[Role][]Script, len(scripts))
	for role, script := range scripts {
		queuedScripts[role] = []Script{script}
	}
	return NewQueuedScriptedAdapter(provider, queuedScripts)
}

// NewRepeatingScriptedAdapter returns a deterministic provider suitable for a
// long-running development worker. Every new logical session receives the
// same role-specific script while keeping a unique provider session identity.
func NewRepeatingScriptedAdapter(
	provider string,
	scripts map[Role]Script,
) *ScriptedAdapter {
	adapter := NewScriptedAdapter(provider, scripts)
	adapter.repeatScript = true
	return adapter
}

func NewQueuedScriptedAdapter(
	provider string,
	scripts map[Role][]Script,
) *ScriptedAdapter {
	copiedScripts := make(map[Role][]Script, len(scripts))
	for role, roleScripts := range scripts {
		copiedScripts[role] = make([]Script, len(roleScripts))
		for index, script := range roleScripts {
			script.Events = append([]Event(nil), script.Events...)
			copiedScripts[role][index] = script
		}
	}
	return &ScriptedAdapter{
		provider:         strings.TrimSpace(provider),
		scripts:          copiedScripts,
		sessions:         make(map[string]*scriptedSession),
		nextScriptByRole: make(map[Role]int),
	}
}

func (a *ScriptedAdapter) Start(
	_ context.Context,
	request SessionRequest,
) (Session, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if a.provider == "" {
		return nil, fmt.Errorf("%w: provider is required", ErrInvalidSessionRequest)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if existing, ok := a.sessions[request.SessionID]; ok {
		if existing.request.Equal(request) {
			return existing, nil
		}
		return nil, ErrSessionConflict
	}

	roleScripts := a.scripts[request.Role]
	nextScript := a.nextScriptByRole[request.Role]
	if a.repeatScript {
		nextScript = 0
	}
	if nextScript >= len(roleScripts) {
		return nil, fmt.Errorf("%w for role %q", ErrScriptNotFound, request.Role)
	}
	script := roleScripts[nextScript]
	if !a.repeatScript {
		a.nextScriptByRole[request.Role]++
	}
	providerSessionID := fmt.Sprintf(
		"%s:%s:%d:%s",
		a.provider,
		request.Role,
		nextScript,
		request.SessionID,
	)
	session := newScriptedSession(
		request,
		providerSessionID,
		script,
		0,
		nil,
	)
	a.sessions[request.SessionID] = session
	go session.run()
	return session, nil
}

func (a *ScriptedAdapter) Resume(
	_ context.Context,
	request ResumeRequest,
) (Session, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[request.SessionID]
	if ok {
		if session.providerSessionID != request.ProviderSessionID ||
			session.request.FeatureID != request.FeatureID ||
			session.request.Role != request.Role {
			return nil, ErrSessionConflict
		}
		return session, nil
	}

	scriptIndex, err := a.scriptIndex(request)
	if err != nil {
		return nil, err
	}
	roleScripts := a.scripts[request.Role]
	if scriptIndex < 0 || scriptIndex >= len(roleScripts) {
		return nil, fmt.Errorf("%w for role %q", ErrScriptNotFound, request.Role)
	}
	script := roleScripts[scriptIndex]
	nextEvent, consistent := reconcileScriptEvents(script.Events, request.Recovery.CompletedEvents)
	requiresReview := !consistent ||
		len(request.Recovery.PendingCommands) > 0 ||
		request.Recovery.PreviousState == "paused" ||
		request.Recovery.PreviousState == "pause_requested"
	assessment := &RecoveryAssessment{
		Consistent: consistent, RequiresUserReview: requiresReview,
	}
	assessmentText := scriptedRecoveryAssessmentText(assessment, request.Recovery)
	session = newScriptedSession(
		request.SessionRequest,
		request.ProviderSessionID,
		script,
		nextEvent,
		&Event{
			Type:               EventRecoveryAssessment,
			Text:               assessmentText,
			RecoveryAssessment: assessment,
		},
	)
	a.sessions[request.SessionID] = session
	if !a.repeatScript && a.nextScriptByRole[request.Role] <= scriptIndex {
		a.nextScriptByRole[request.Role] = scriptIndex + 1
	}
	go session.run()
	return session, nil
}

func (a *ScriptedAdapter) scriptIndex(request ResumeRequest) (int, error) {
	prefix := fmt.Sprintf("%s:%s:", a.provider, request.Role)
	remainder, found := strings.CutPrefix(request.ProviderSessionID, prefix)
	if found {
		indexText, sessionID, ok := strings.Cut(remainder, ":")
		if ok && sessionID == request.SessionID {
			index, err := strconv.Atoi(indexText)
			if err == nil {
				return index, nil
			}
		}
	}

	// Provider IDs created before recovery support did not encode the script
	// index. Recognize them only when the deterministic session suffix makes
	// the original queue position unambiguous.
	if request.ProviderSessionID == a.provider+":"+request.SessionID {
		return inferScriptIndex(request.Role, request.SessionID)
	}
	return 0, ErrSessionConflict
}

func inferScriptIndex(role Role, sessionID string) (int, error) {
	suffix := sessionID
	if index := strings.LastIndex(sessionID, ":"); index >= 0 {
		suffix = sessionID[index+1:]
	}
	switch role {
	case RoleCoder:
		if suffix == "implementation" {
			return 0, nil
		}
		if round, err := parseNumberedSuffix(suffix, "review-fix-"); err == nil {
			return round, nil
		}
	case RoleReviewer:
		if round, err := parseNumberedSuffix(suffix, "review-"); err == nil {
			return round - 1, nil
		}
	case RoleLead:
		if round, err := parseNumberedSuffix(suffix, "plan-lead-"); err == nil {
			return round - 1, nil
		}
	case RoleConsultant:
		if round, err := parseNumberedSuffix(suffix, "plan-consultant-"); err == nil {
			return round - 1, nil
		}
	}
	return 0, ErrSessionConflict
}

func parseNumberedSuffix(value string, prefix string) (int, error) {
	number, found := strings.CutPrefix(value, prefix)
	if !found {
		return 0, ErrSessionConflict
	}
	parsed, err := strconv.Atoi(number)
	if err != nil || parsed < 1 {
		return 0, ErrSessionConflict
	}
	return parsed, nil
}

func reconcileScriptEvents(script []Event, completed []Event) (int, bool) {
	next := 0
	for _, event := range completed {
		if event.Type == EventRecoveryAssessment ||
			event.Type == EventPauseAcknowledged ||
			event.Type == EventContinued {
			continue
		}
		if next >= len(script) ||
			event.Type != script[next].Type ||
			event.Text != script[next].Text {
			return next, false
		}
		next++
	}
	return next, true
}

func scriptedRecoveryAssessmentText(
	assessment *RecoveryAssessment,
	recovery RecoveryContext,
) string {
	if !assessment.Consistent {
		return "Recovery assessment blocked: durable session activity contradicts the simulated provider script; user review is required."
	}
	if len(recovery.PendingCommands) > 0 {
		commands := make([]string, 0, len(recovery.PendingCommands))
		for _, command := range recovery.PendingCommands {
			commands = append(commands, command.ID+" ("+string(command.Type)+")")
		}
		return fmt.Sprintf(
			"Recovery assessment: durable activity is consistent, but %d command delivery outcome is uncertain [%s]; user review is required before continuing. Repository, Git, tests, and Forgejo PR state are not applicable to this simulated worker.",
			len(recovery.PendingCommands),
			strings.Join(commands, ", "),
		)
	}
	if recovery.PreviousState == "paused" || recovery.PreviousState == "pause_requested" {
		return "Recovery assessment: durable activity is consistent and the pre-restart pause intent was preserved; user review is required before continuing. Repository, Git, tests, and Forgejo PR state are not applicable to this simulated worker."
	}
	return "Recovery assessment: durable activity matches the restored simulated conversation and workflow phase. Repository, worktree, Git HEAD/status/diff, interrupted tests, and Forgejo PR state are not applicable to this simulated worker."
}

// Advance releases one scripted action. It is intentionally outside Adapter:
// production workers advance themselves, while tests control fake timing exactly.
func (a *ScriptedAdapter) Advance(
	ctx context.Context,
	sessionID string,
) error {
	a.mu.Lock()
	session, ok := a.sessions[sessionID]
	a.mu.Unlock()
	if !ok {
		return ErrSessionNotFound
	}
	return session.advanceOne(ctx)
}

type scriptedSession struct {
	request           SessionRequest
	providerSessionID string
	script            Script
	commands          chan commandDelivery
	advance           chan struct{}
	events            chan Event
	done              chan struct{}

	mu            sync.Mutex
	finished      bool
	result        Result
	commandsByID  map[string]*commandRecord
	nextEvent     int
	recoveryEvent *Event
}

type commandDelivery struct {
	command Command
	record  *commandRecord
}

type commandRecord struct {
	command Command
	done    chan struct{}
	err     error
}

func newScriptedSession(
	request SessionRequest,
	providerSessionID string,
	script Script,
	nextEvent int,
	recoveryEvent *Event,
) *scriptedSession {
	eventBuffer := len(script.Events) + 8
	if eventBuffer < 16 {
		eventBuffer = 16
	}
	return &scriptedSession{
		request:           request.Clone(),
		providerSessionID: providerSessionID,
		script:            script,
		commands:          make(chan commandDelivery),
		advance:           make(chan struct{}),
		events:            make(chan Event, eventBuffer),
		done:              make(chan struct{}),
		commandsByID:      make(map[string]*commandRecord),
		nextEvent:         nextEvent,
		recoveryEvent:     recoveryEvent,
	}
}

func (s *scriptedSession) ProviderSessionID() string {
	return s.providerSessionID
}

func (s *scriptedSession) Events() <-chan Event {
	return s.events
}

func (s *scriptedSession) Send(
	ctx context.Context,
	command Command,
) error {
	if err := command.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	if existing, ok := s.commandsByID[command.ID]; ok {
		if existing.command != command {
			s.mu.Unlock()
			return ErrCommandConflict
		}
		s.mu.Unlock()
		return waitForCommand(ctx, existing)
	}
	if s.finished {
		s.mu.Unlock()
		return ErrSessionFinished
	}
	record := &commandRecord{command: command, done: make(chan struct{})}
	s.commandsByID[command.ID] = record
	s.mu.Unlock()

	delivery := commandDelivery{command: command, record: record}
	select {
	case s.commands <- delivery:
		return waitForCommand(ctx, record)
	case <-s.done:
		s.removePendingCommand(record)
		return ErrSessionFinished
	case <-ctx.Done():
		s.removePendingCommand(record)
		return ctx.Err()
	}
}

func (s *scriptedSession) Wait(ctx context.Context) (Result, error) {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func (s *scriptedSession) advanceOne(ctx context.Context) error {
	select {
	case <-s.done:
		return ErrSessionFinished
	default:
	}
	select {
	case s.advance <- struct{}{}:
		return nil
	case <-s.done:
		return ErrSessionFinished
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *scriptedSession) run() {
	if s.recoveryEvent != nil {
		s.events <- *s.recoveryEvent
	}
	if len(s.script.Events) == 0 && s.recoveryEvent == nil {
		s.finish(OutcomeCompleted)
		return
	}

	paused := s.recoveryEvent != nil
	for {
		if paused {
			delivery := <-s.commands
			paused = s.handleCommand(delivery, paused)
			if s.isFinished() {
				return
			}
			if !paused && s.nextEvent == len(s.script.Events) {
				s.finish(OutcomeCompleted)
				return
			}
			continue
		}

		select {
		case delivery := <-s.commands:
			paused = s.handleCommand(delivery, paused)
			if s.isFinished() {
				return
			}
		case <-s.advance:
			s.events <- s.script.Events[s.nextEvent]
			s.nextEvent++
			if s.nextEvent == len(s.script.Events) {
				s.finish(OutcomeCompleted)
				return
			}
		}
	}
}

func (s *scriptedSession) handleCommand(
	delivery commandDelivery,
	paused bool,
) bool {
	var err error
	switch delivery.command.Type {
	case CommandMessage:
		// The coordinator records the user message once; the script can respond
		// through its next observable event without echoing the message here.
	case CommandPause:
		if paused {
			err = ErrInvalidSessionState
			break
		}
		paused = true
		s.events <- Event{Type: EventPauseAcknowledged, Text: "paused at a safe boundary"}
	case CommandContinue:
		if !paused {
			err = ErrInvalidSessionState
			break
		}
		paused = false
		s.events <- Event{Type: EventContinued, Text: "session continued"}
	case CommandStop:
		s.completeCommand(delivery.record, nil)
		s.finish(OutcomeStopped)
		return paused
	}
	s.completeCommand(delivery.record, err)
	return paused
}

func (s *scriptedSession) completeCommand(record *commandRecord, err error) {
	s.mu.Lock()
	record.err = err
	close(record.done)
	s.mu.Unlock()
}

func (s *scriptedSession) removePendingCommand(record *commandRecord) {
	s.mu.Lock()
	if existing := s.commandsByID[record.command.ID]; existing == record {
		delete(s.commandsByID, record.command.ID)
	}
	s.mu.Unlock()
}

func (s *scriptedSession) finish(outcome Outcome) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	disposition := s.script.Disposition
	if outcome == OutcomeStopped {
		disposition = ""
	}
	s.result = Result{
		Outcome:           outcome,
		Disposition:       disposition,
		ProviderSessionID: s.providerSessionID,
		Summary:           s.script.Summary,
	}
	close(s.done)
	close(s.events)
	s.mu.Unlock()
}

func (s *scriptedSession) isFinished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

func waitForCommand(ctx context.Context, record *commandRecord) error {
	select {
	case <-record.done:
		return record.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
