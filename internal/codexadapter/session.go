package codexadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/processsupervisor"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

var (
	ErrUnsupportedCommand = errors.New("Codex adapter command is unsupported")
	ErrSessionFinished    = errors.New("Codex session is already finished")
	ErrEventBackpressure  = errors.New("Codex activity consumer fell behind")
)

type session struct {
	client           *protocolClient
	process          *processsupervisor.Process
	threadID         string
	workingDirectory string
	shutdownTimeout  time.Duration
	outputContract   worker.OutputContract
	events           chan worker.Event
	done             chan struct{}

	mu               sync.Mutex
	turnID           string
	lastAgentMessage string
	pendingMessage   string
	forced           bool
	result           worker.Result
	waitErr          error
	finished         bool
}

var _ worker.ForceStoppableSession = (*session)(nil)

func newSession(
	client *protocolClient,
	process *processsupervisor.Process,
	threadID string,
	workingDirectory string,
	shutdownTimeout time.Duration,
	eventBuffer int,
	outputContract worker.OutputContract,
) *session {
	return &session{
		client:           client,
		process:          process,
		threadID:         threadID,
		workingDirectory: workingDirectory,
		shutdownTimeout:  shutdownTimeout,
		outputContract:   outputContract,
		events:           make(chan worker.Event, eventBuffer),
		done:             make(chan struct{}),
	}
}

func (session *session) start(turnID string) {
	session.mu.Lock()
	session.turnID = turnID
	session.mu.Unlock()
	go session.run()
}

func (session *session) ProviderSessionID() string {
	if session == nil {
		return ""
	}
	return session.threadID
}

func (session *session) Events() <-chan worker.Event {
	if session == nil {
		return nil
	}
	return session.events
}

func (session *session) Send(ctx context.Context, command worker.Command) error {
	if session == nil {
		return errors.New("Codex session is required")
	}
	if err := command.Validate(); err != nil {
		return err
	}
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return ErrSessionFinished
	}
	threadID := session.threadID
	turnID := session.turnID
	session.mu.Unlock()

	switch command.Type {
	case worker.CommandMessage:
		params := struct {
			ThreadID            string      `json:"threadId"`
			ExpectedTurnID      string      `json:"expectedTurnId"`
			ClientUserMessageID string      `json:"clientUserMessageId"`
			Input               []textInput `json:"input"`
		}{
			ThreadID:            threadID,
			ExpectedTurnID:      turnID,
			ClientUserMessageID: command.ID,
			Input:               []textInput{{Type: "text", Text: command.Message}},
		}
		var response struct {
			TurnID string `json:"turnId"`
		}
		if err := session.client.request(ctx, "turn/steer", params, &response); err != nil {
			return fmt.Errorf("steer Codex turn: %w", err)
		}
		if response.TurnID != turnID {
			return fmt.Errorf(
				"%w: turn/steer returned turn %q, want %q",
				ErrProtocol,
				response.TurnID,
				turnID,
			)
		}
		return nil
	case worker.CommandStop:
		params := struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}{ThreadID: threadID, TurnID: turnID}
		var response map[string]any
		if err := session.client.request(ctx, "turn/interrupt", params, &response); err != nil {
			return fmt.Errorf("interrupt Codex turn: %w", err)
		}
		return nil
	case worker.CommandPause, worker.CommandContinue:
		return fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
}

func (session *session) Wait(ctx context.Context) (worker.Result, error) {
	if session == nil {
		return worker.Result{}, errors.New("Codex session is required")
	}
	if ctx == nil {
		return worker.Result{}, errors.New("context is required")
	}
	select {
	case <-session.done:
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.result, session.waitErr
	case <-ctx.Done():
		return worker.Result{}, ctx.Err()
	}
}

func (session *session) ForceStop(ctx context.Context, _ string) error {
	if session == nil {
		return errors.New("Codex session is required")
	}
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return nil
	}
	session.forced = true
	session.mu.Unlock()
	if _, err := session.process.ForceStop(ctx); err != nil {
		return fmt.Errorf("force-stop Codex app-server: %w", err)
	}
	return nil
}

func (session *session) run() {
	for {
		select {
		case notification, open := <-session.client.notifications:
			if !open {
				session.finishFromProcessExit()
				return
			}
			events, terminal, result, err := session.translate(notification)
			if err != nil {
				session.stopProcess()
				session.complete(worker.Result{}, err)
				return
			}
			for _, event := range events {
				select {
				case session.events <- event:
				default:
					session.stopProcess()
					session.complete(worker.Result{}, ErrEventBackpressure)
					return
				}
			}
			if terminal {
				session.stopProcess()
				session.complete(result, nil)
				return
			}
		case <-session.client.done:
			session.finishFromProcessExit()
			return
		}
	}
}

func (session *session) finishFromProcessExit() {
	session.mu.Lock()
	forced := session.forced
	session.mu.Unlock()
	if forced {
		session.complete(worker.Result{
			Outcome:           worker.OutcomeStopped,
			ProviderSessionID: session.threadID,
			Summary:           "Codex process was force-stopped.",
		}, nil)
		return
	}
	session.complete(worker.Result{}, session.client.connectionError())
}

func (session *session) stopAfterFailedLaunch() {
	session.stopProcess()
	session.complete(worker.Result{}, errors.New("Codex turn did not start"))
}

func (session *session) stopProcess() {
	ctx, cancel := context.WithTimeout(context.Background(), session.shutdownTimeout)
	_, err := session.process.Terminate(ctx)
	cancel()
	if err == nil {
		return
	}
	forceCtx, forceCancel := context.WithTimeout(context.Background(), session.shutdownTimeout)
	defer forceCancel()
	_, _ = session.process.ForceStop(forceCtx)
}

func (session *session) complete(result worker.Result, err error) {
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return
	}
	session.finished = true
	session.result = result
	session.waitErr = err
	close(session.events)
	close(session.done)
	session.mu.Unlock()
}

type scopedNotification struct {
	ThreadID  string     `json:"threadId"`
	TurnID    string     `json:"turnId"`
	Turn      turnRecord `json:"turn"`
	Item      threadItem `json:"item"`
	WillRetry bool       `json:"willRetry"`
}

type turnRecord struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type threadItem struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Text       string             `json:"text"`
	Phase      string             `json:"phase"`
	Summary    []string           `json:"summary"`
	Status     string             `json:"status"`
	Command    string             `json:"command"`
	ExitCode   *int               `json:"exitCode"`
	DurationMS *int64             `json:"durationMs"`
	Changes    []fileUpdateChange `json:"changes"`
}

type fileUpdateChange struct {
	Path string         `json:"path"`
	Diff string         `json:"diff"`
	Kind fileUpdateKind `json:"kind"`
}

type fileUpdateKind struct {
	Type     string  `json:"type"`
	MovePath *string `json:"move_path"`
}

func (session *session) translate(
	message protocolMessage,
) ([]worker.Event, bool, worker.Result, error) {
	switch message.Method {
	case "thread/started":
		var params struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := decodeParams(message, &params); err != nil {
			return nil, false, worker.Result{}, err
		}
		if strings.TrimSpace(params.Thread.ID) == "" || params.Thread.ID != session.threadID {
			return nil, false, worker.Result{}, session.scopeError(params.Thread.ID, "")
		}
		return nil, false, worker.Result{}, nil
	case "turn/started", "turn/completed", "item/started", "item/completed", "error":
		var params scopedNotification
		if err := decodeParams(message, &params); err != nil {
			return nil, false, worker.Result{}, err
		}
		turnID := params.TurnID
		if turnID == "" {
			turnID = params.Turn.ID
		}
		if params.ThreadID != session.threadID || turnID != session.currentTurnID() {
			return nil, false, worker.Result{}, session.scopeError(params.ThreadID, turnID)
		}
		switch message.Method {
		case "turn/started":
			if params.Turn.Status != "inProgress" {
				return nil, false, worker.Result{}, fmt.Errorf(
					"%w: turn/started used status %q",
					ErrProtocol,
					params.Turn.Status,
				)
			}
			return events(event(worker.EventActivity, "Codex started working.")), false, worker.Result{}, nil
		case "item/started":
			if strings.TrimSpace(params.Item.ID) == "" || strings.TrimSpace(params.Item.Type) == "" {
				return nil, false, worker.Result{}, fmt.Errorf("%w: item/started omitted item identity or type", ErrProtocol)
			}
			return events(session.startedItem(params.Item)), false, worker.Result{}, nil
		case "item/completed":
			if strings.TrimSpace(params.Item.ID) == "" || strings.TrimSpace(params.Item.Type) == "" {
				return nil, false, worker.Result{}, fmt.Errorf("%w: item/completed omitted item identity or type", ErrProtocol)
			}
			completed, err := session.completedItem(params.Item)
			return completed, false, worker.Result{}, err
		case "error":
			text := "Codex reported an error."
			if params.WillRetry {
				text = "Codex reported a temporary error and will retry."
			}
			return events(event(worker.EventActivity, text)), false, worker.Result{}, nil
		case "turn/completed":
			return session.completedTurn(params.Turn)
		}
	case "item/agentMessage/delta", "turn/diff/updated", "thread/tokenUsage/updated":
		// Deltas are intentionally ignored. The completed item is authoritative,
		// avoids duplicate text, and passes through the worker's safety filter.
		return nil, false, worker.Result{}, nil
	default:
		// App Server evolves quickly and emits many optional notifications. An
		// unknown notification cannot change Commitarium state, so it is ignored.
		return nil, false, worker.Result{}, nil
	}
	return nil, false, worker.Result{}, nil
}

func (session *session) startedItem(item threadItem) *worker.Event {
	switch item.Type {
	case "commandExecution", "fileChange":
		// A command or file change is published once, when its completed item has
		// the final execution facts. This avoids duplicate UI steps.
		return nil
	case "webSearch":
		return event(worker.EventActivity, "Codex started a web search.")
	case "mcpToolCall", "dynamicToolCall":
		return event(worker.EventActivity, "Codex started a tool call.")
	case "subAgentActivity", "collabAgentToolCall":
		return event(worker.EventActivity, "Codex started delegated agent work.")
	default:
		return nil
	}
}

func (session *session) completedItem(item threadItem) ([]worker.Event, error) {
	switch item.Type {
	case "agentMessage":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return nil, nil
		}
		switch item.Phase {
		case "commentary":
			return session.narrationEvents(text), nil
		case "", "final_answer":
			// Older App Server versions do not classify messages. Preserve the
			// existing final-message behavior when phase is absent.
		default:
			return nil, fmt.Errorf("%w: agent message used phase %q", ErrProtocol, item.Phase)
		}
		if session.outputContract == worker.OutputContractPlanningLead ||
			session.outputContract == worker.OutputContractImplementationLead ||
			session.outputContract == worker.OutputContractImplementationReview ||
			session.outputContract == worker.OutputContractImplementationReadiness ||
			session.outputContract == worker.OutputContractIntervention ||
			session.outputContract == worker.OutputContractToolchainSetup {
			session.mu.Lock()
			session.pendingMessage = text
			session.mu.Unlock()
			return nil, nil
		}
		session.mu.Lock()
		session.lastAgentMessage = text
		session.mu.Unlock()
		return events(event(worker.EventMessage, text)), nil
	case "commandExecution":
		return session.commandEvents(item), nil
	case "fileChange":
		return session.fileChangeEvents(item)
	case "webSearch":
		return events(event(worker.EventActivity, "Codex finished a web search.")), nil
	case "mcpToolCall", "dynamicToolCall":
		return events(event(worker.EventActivity, "Codex finished a tool call.")), nil
	case "plan":
		return events(event(worker.EventActivity, "Codex updated its plan.")), nil
	case "subAgentActivity", "collabAgentToolCall":
		return events(event(worker.EventActivity, "Codex finished delegated agent work.")), nil
	case "reasoning":
		return session.narrationEvents(item.Summary...), nil
	default:
		return nil, nil
	}
}

func (session *session) narrationEvents(texts ...string) []worker.Event {
	if session.outputContract == worker.OutputContractIntervention {
		return nil
	}
	result := make([]worker.Event, 0, len(texts))
	for _, value := range texts {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		result = append(result, worker.Event{
			Type:     worker.EventActivity,
			Text:     text,
			Activity: &worker.Activity{Kind: worker.ActivityKindNarration},
		})
	}
	return result
}

func (session *session) commandEvents(item threadItem) []worker.Event {
	command := strings.TrimSpace(item.Command)
	if command == "" {
		return events(event(worker.EventActivity, "Codex finished a command."))
	}
	text := "Codex ran command: " + command
	if item.ExitCode != nil {
		text = fmt.Sprintf("Codex ran command with exit code %d: %s", *item.ExitCode, command)
	}
	return []worker.Event{{
		Type: worker.EventActivity,
		Text: text,
		Activity: &worker.Activity{
			Kind: worker.ActivityKindCommand, Command: command,
			ExitCode: item.ExitCode, DurationMS: item.DurationMS,
		},
	}}
}

func (session *session) fileChangeEvents(item threadItem) ([]worker.Event, error) {
	if len(item.Changes) == 0 {
		return events(event(worker.EventActivity, "Codex finished a file change.")), nil
	}
	result := make([]worker.Event, 0, len(item.Changes))
	for _, change := range item.Changes {
		path, err := session.publicPath(change.Path)
		if err != nil {
			return nil, err
		}
		operation := worker.FileOperation("")
		oldPath := ""
		switch change.Kind.Type {
		case "add":
			operation = worker.FileOperationCreated
		case "delete":
			operation = worker.FileOperationDeleted
		case "update":
			operation = worker.FileOperationModified
			if change.Kind.MovePath != nil && strings.TrimSpace(*change.Kind.MovePath) != "" {
				oldPath = path
				path, err = session.publicPath(*change.Kind.MovePath)
				if err != nil {
					return nil, err
				}
				operation = worker.FileOperationRenamed
			}
		default:
			return nil, fmt.Errorf("%w: file change used kind %q", ErrProtocol, change.Kind.Type)
		}
		additions, deletions := unifiedDiffCounts(change.Diff)
		text := fmt.Sprintf("Codex %s %s (+%d/-%d).", operation, path, additions, deletions)
		result = append(result, worker.Event{
			Type: worker.EventActivity,
			Text: text,
			Activity: &worker.Activity{
				Kind: worker.ActivityKindFileChange, Operation: operation,
				Path: path, OldPath: oldPath, Additions: &additions, Deletions: &deletions,
			},
		})
	}
	return result, nil
}

func (session *session) publicPath(path string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "." || cleaned == "" {
		return "", fmt.Errorf("%w: file change omitted its path", ErrProtocol)
	}
	if filepath.IsAbs(cleaned) {
		relative, err := filepath.Rel(session.workingDirectory, cleaned)
		if err != nil {
			return "", fmt.Errorf("%w: resolve file change path: %v", ErrProtocol, err)
		}
		cleaned = relative
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("%w: file change path is outside the workspace", ErrProtocol)
	}
	return filepath.ToSlash(cleaned), nil
}

func unifiedDiffCounts(diff string) (int, int) {
	additions := 0
	deletions := 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			deletions++
		}
	}
	return additions, deletions
}

func events(item *worker.Event) []worker.Event {
	if item == nil {
		return nil
	}
	return []worker.Event{*item}
}

func (session *session) completedTurn(
	turn turnRecord,
) ([]worker.Event, bool, worker.Result, error) {
	switch turn.Status {
	case "completed":
		structured, disposition, publication, review, interventionEffect, toolchainProposal, err := session.completeStructuredResponse()
		if err != nil {
			return nil, false, worker.Result{}, err
		}
		return events(structured), true, worker.Result{
			Outcome:            worker.OutcomeCompleted,
			Disposition:        disposition,
			ProviderSessionID:  session.threadID,
			Summary:            session.summary(),
			Publication:        publication,
			Review:             review,
			InterventionEffect: interventionEffect,
			ToolchainProposal:  toolchainProposal,
		}, nil
	case "interrupted":
		return nil, true, worker.Result{
			Outcome:           worker.OutcomeStopped,
			ProviderSessionID: session.threadID,
			Summary:           "Codex turn was interrupted.",
		}, nil
	case "failed":
		return nil, true, worker.Result{
			Outcome:           worker.OutcomeFailed,
			ProviderSessionID: session.threadID,
			Summary:           "Codex reported that the turn failed.",
		}, nil
	default:
		return nil, false, worker.Result{}, fmt.Errorf(
			"%w: turn/completed used status %q",
			ErrProtocol,
			turn.Status,
		)
	}
}

func (session *session) completeStructuredResponse() (
	*worker.Event,
	worker.Disposition,
	*worker.ImplementationPublication,
	*worker.ReviewPublication,
	worker.InterventionEffect,
	*worker.ToolchainProposal,
	error,
) {
	if session.outputContract == "" {
		return nil, worker.DispositionSucceeded, nil, nil, "", nil, nil
	}
	session.mu.Lock()
	raw := session.pendingMessage
	session.mu.Unlock()
	if strings.TrimSpace(raw) == "" {
		return nil, "", nil, nil, "", nil, fmt.Errorf(
			"%w: structured turn omitted its final response", ErrProtocol,
		)
	}
	resolved, err := worker.ResolveStructuredOutput(session.outputContract, []byte(raw))
	if err != nil {
		return nil, "", nil, nil, "", nil, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	session.mu.Lock()
	session.lastAgentMessage = resolved.Event.Text
	session.mu.Unlock()
	return &resolved.Event, resolved.Disposition, resolved.Publication, resolved.Review,
		resolved.InterventionEffect, resolved.ToolchainProposal, nil
}

func (session *session) summary() string {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.lastAgentMessage != "" {
		return session.lastAgentMessage
	}
	return "Codex completed the turn."
}

func (session *session) currentTurnID() string {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.turnID
}

func (session *session) scopeError(threadID string, turnID string) error {
	return fmt.Errorf(
		"%w: notification scope thread=%q turn=%q, want thread=%q turn=%q",
		ErrProtocol,
		threadID,
		turnID,
		session.threadID,
		session.currentTurnID(),
	)
}

func decodeParams(message protocolMessage, destination any) error {
	if len(message.Params) == 0 {
		return fmt.Errorf("%w: %s omitted params", ErrProtocol, message.Method)
	}
	if err := json.Unmarshal(message.Params, destination); err != nil {
		return fmt.Errorf("%w: decode %s params", ErrProtocol, message.Method)
	}
	return nil
}

func event(eventType worker.EventType, text string) *worker.Event {
	return &worker.Event{Type: eventType, Text: text}
}
