package conversations

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// Events (ASSISTANT_PLAN.md phase 3): the coordinator watches the work in
// each project whose conversation the user has started, notes what happened
// in the conversation, and wakes the assistant for what matters, one turn at
// a time. Events that arrive while it answers wait for its next turn.

type RunReader interface {
	RunsForFeature(ctx context.Context, featureID string) ([]execution.Run, error)
}

type LimitWaitReader interface {
	LimitWaitForRun(ctx context.Context, runID string) (execution.LimitWait, error)
}

type RetryWaitReader interface {
	RetryWaitForRun(ctx context.Context, runID string) (execution.RetryWait, error)
}

// Event is something that happened in a project. Wake asks for the
// assistant's turn; otherwise it is only noted in the conversation.
type Event struct {
	Text string
	Wake bool
}

// Watcher turns changes in the projects' work into events.
type Watcher struct {
	Service  *Service
	Features FeatureLister
	Runs     RunReader
	// Visions lets the watcher follow an autopilot goal, and Apply runs the
	// actions autopilot takes over, as the user's approval would.
	Visions VisionReader
	Apply   func(ctx context.Context, scope, actionID string) error
	// SetUp sets a new app up on the stack its vision recommends, as the
	// project's own setup does (launch.go).
	SetUp func(ctx context.Context, projectID string, presets []string, frameworks string) error
	// LimitWaits, when set, tells a usage-limit wait from a blocker: it
	// resumes on its own, so it does not wake the assistant.
	LimitWaits LimitWaitReader
	// RetryWaits, when set, tells an interrupted turn that retries on its own
	// from a blocker, so it neither wakes the assistant nor spends
	// autopilot's continues.
	RetryWaits RetryWaitReader
	// Toolchains and VisionSessions let the gap check see a project's stack
	// and a vision being written; VisionSessions also lets each look store a
	// vision the assistant has finished writing. AgentLimited, when set, tells it the
	// assistant's agent is at its usage limit (gaps.go).
	Toolchains     ToolchainReader
	VisionSessions VisionSessions
	AgentLimited   func(ctx context.Context, agentID string) bool
}

// watchEvery is how often the watcher looks at the projects' work.
const watchEvery = 20 * time.Second

// Run watches until ctx ends.
func (watcher Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(watchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			watcher.Tick(ctx)
		case <-watcher.Service.nudge:
			watcher.Tick(ctx)
		}
	}
}

// Tick looks once at every watched project.
func (watcher Watcher) Tick(ctx context.Context) {
	for _, scope := range watcher.Service.projectScopes() {
		watcher.settleVision(ctx, scope)
		if err := watcher.Service.observe(ctx, scope, watcher.snapshot); err != nil {
			log.Printf("assistant events for %s: %v", scope, err)
		}
		watcher.launch(ctx, scope)
		watcher.autopilot(ctx, scope)
		watcher.gapCheck(ctx, scope)
	}
}

// settleVision stores a vision the assistant has finished writing: the
// vision service stores it only when its conversation is read, and nothing
// else here reads it while the Vision page is closed. A new app's launch,
// autopilot and the assistant's turns then see the vision once it is written.
func (watcher Watcher) settleVision(ctx context.Context, scope string) {
	if watcher.VisionSessions != nil {
		_, _ = watcher.VisionSessions.OpenSession(ctx, scope)
	}
}

// autopilot applies the actions autopilot takes over. One that fails stays
// a card for the user, with a note saying why.
func (watcher Watcher) autopilot(ctx context.Context, scope string) {
	if watcher.Apply == nil {
		return
	}
	actions, err := watcher.Service.autopilotActions(ctx, scope)
	if err != nil {
		log.Printf("autopilot for %s: %v", scope, err)
		return
	}
	for _, action := range actions {
		err := watcher.Apply(ctx, scope, action.ID)
		if err := watcher.Service.autopilotOutcome(scope, action, err); err != nil {
			log.Printf("autopilot outcome for %s: %v", scope, err)
		}
	}
}

// maxRefusals bounds how often in a row the assistant hears that autopilot
// could not apply its action, so a choice it cannot fix comes back to the
// user instead of spending turns.
const maxRefusals = 2

// autopilotOutcome records what became of an action autopilot applied. A
// refused one stays a card for the user, with a note saying why, and wakes
// the assistant with the reason so it can choose again (ADR-027: the
// coordinator refuses agents that cannot run, but never picks others).
func (service *Service) autopilotOutcome(scope string, action Action, applyErr error) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return err
	}
	now := service.now()
	if applyErr == nil {
		changed := stored.Refusals != 0
		stored.Refusals = 0
		if action.HoldStart {
			// The work order exists as a draft; queue its start behind the
			// work it follows.
			for _, applied := range stored.Actions {
				if applied.ID != action.ID || applied.WorkOrderID == "" {
					continue
				}
				start := applied.AssistantAction
				start.Type = featureartifact.AssistantStartWorkOrder
				stored.Actions = append(stored.Actions, Action{
					ID: action.ID + "_start", MessageID: action.MessageID, AssistantAction: start,
					Status: ActionProposed, Queued: true, UpdatedAt: now,
				})
				changed = true
				break
			}
		}
		if !changed {
			return nil
		}
		stored.UpdatedAt = now
		return service.write(stored)
	}
	// A card the user approves later starts what it creates.
	for index := range stored.Actions {
		if stored.Actions[index].ID == action.ID {
			stored.Actions[index].HoldStart = false
		}
	}
	name := actionName(action.AssistantAction)
	stored.addEvent(fmt.Sprintf("Autopilot could not apply %s: %v. It waits for your approval.", name, applyErr), now)
	stored.Refusals++
	if stored.Refusals <= maxRefusals {
		stored.PendingEvents = append(stored.PendingEvents, fmt.Sprintf(
			"Commitarium could not apply your action %s: %v. Propose it again with a choice that can run, "+
				"or tell the user plainly what is needed.", name, applyErr))
		service.nudgeWatcher()
	}
	stored.UpdatedAt = now
	return service.write(stored)
}

// work is a work order or session as the watcher sees it.
type work struct {
	Title, Reason, LimitAgent string
	Session                   bool
	AutoMerge                 bool
	Retrying                  bool
	State                     feature.State
	RunID                     string
	RunStatus                 execution.RunStatus
	WaitKind                  execution.RunWaitKind
}

func (item work) signature() string {
	if item.State.IsTerminal() || item.RunID == "" {
		return string(item.State)
	}
	signature := strings.Join([]string{
		string(item.State), item.RunID, string(item.RunStatus), string(item.WaitKind), item.LimitAgent,
	}, "|")
	// Only when retrying, so the signatures already stored stay the same.
	if item.Retrying {
		signature += "|retrying"
	}
	return signature
}

// view is a project as the watcher sees it: its work by work order, and
// its vision plan when it has one.
type view struct {
	Work map[string]work
	Plan *featureartifact.VisionPlan
}

func (watcher Watcher) snapshot(ctx context.Context, projectID string) (view, error) {
	seen := view{}
	if watcher.Visions != nil {
		if current, err := watcher.Visions.Get(ctx, projectID); err == nil {
			seen.Plan = &current.Plan
		}
	}
	features, err := watcher.Features.List(ctx, projectID)
	if err != nil {
		return view{}, err
	}
	seen.Work = make(map[string]work, len(features))
	for _, item := range features {
		// Background work is Commitarium's own, except setting the preview
		// up, which the assistant can start and must hear about (ADR-028).
		if item.Background && !item.PreviewCheck {
			continue
		}
		current := work{Title: item.Title, Session: item.Kind == feature.KindSession, State: item.State}
		if !item.State.IsTerminal() {
			runs, err := watcher.Runs.RunsForFeature(ctx, item.ID)
			if err != nil {
				return view{}, err
			}
			if run, ok := newest(runs); ok {
				current.RunID, current.RunStatus, current.WaitKind, current.Reason = run.ID, run.Status, run.WaitKind, run.Reason
				current.AutoMerge = run.MergePolicy == project.MergePolicyAutoAfterGates
				if watcher.LimitWaits != nil && run.Status == execution.RunStatusWaitingForUser &&
					run.WaitKind == execution.RunWaitKindBlocker {
					if wait, err := watcher.LimitWaits.LimitWaitForRun(ctx, run.ID); err == nil {
						current.LimitAgent = wait.AgentID
					}
				}
				if watcher.RetryWaits != nil && run.Status == execution.RunStatusWaitingForUser &&
					run.WaitKind == execution.RunWaitKindBlocker && !run.Paused {
					if wait, err := watcher.RetryWaits.RetryWaitForRun(ctx, run.ID); err == nil {
						current.Retrying = wait.DueAt != nil
					}
				}
			}
		}
		seen.Work[item.ID] = current
	}
	return seen, nil
}

func newest(runs []execution.Run) (execution.Run, bool) {
	if len(runs) == 0 {
		return execution.Run{}, false
	}
	latest := runs[0]
	for _, run := range runs[1:] {
		if run.StartedAt.After(latest.StartedAt) {
			latest = run
		}
	}
	return latest, true
}

// changes are the events between what was seen before and now. The first
// look at a project only records it.
func changes(previous map[string]string, current map[string]work) []Event {
	if previous == nil {
		return nil
	}
	ids := make([]string, 0, len(current))
	for id := range current {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	events := make([]Event, 0)
	for _, id := range ids {
		item := current[id]
		before, seen := previous[id]
		if !seen || before == item.signature() {
			continue
		}
		if event, ok := item.event(); ok {
			events = append(events, event)
		}
	}
	return events
}

func (item work) event() (Event, bool) {
	name := fmt.Sprintf("%q", item.Title)
	if item.Session {
		name = "The session " + name
	}
	reason := firstLine(item.Reason)
	with := func(text string) string {
		if reason == "" {
			return text + "."
		}
		return text + ": " + reason
	}
	switch {
	case item.State == feature.StateCompleted:
		return Event{Text: name + " merged.", Wake: true}, true
	case item.State.IsTerminal():
		return Event{}, false
	case item.State == feature.StateReady && item.RunStatus == execution.RunStatusStopped:
		// Returned to Ready to start over: under autopilot it starts again.
		return Event{Text: name + " is back in Ready to start over.", Wake: true}, true
	case item.RunStatus == execution.RunStatusFailed:
		return Event{Text: with(name + " failed"), Wake: true}, true
	case item.RunStatus != execution.RunStatusWaitingForUser:
		return Event{}, false
	case item.LimitAgent != "":
		return Event{Text: name + " is waiting for " + item.LimitAgent + "'s usage limit to reset."}, true
	case item.Retrying:
		return Event{Text: name + " was interrupted and is retrying automatically."}, true
	}
	switch item.WaitKind {
	case execution.RunWaitKindPaused:
		return Event{}, false
	case execution.RunWaitKindClarification:
		if item.Session {
			return Event{}, false
		}
		return Event{Text: name + " is waiting for the user's answer.", Wake: true}, true
	case execution.RunWaitKindMergeGate:
		if item.AutoMerge {
			// Its gates run and it merges on its own.
			return Event{}, false
		}
		return Event{Text: name + " is ready to merge and waits for the user's approval.", Wake: true}, true
	case execution.RunWaitKindBlocker:
		return Event{Text: with(name + " is blocked"), Wake: true}, true
	default:
		return Event{Text: with(name + " is waiting for the user"), Wake: true}, true
	}
}

// nudgeWatcher asks the watcher to look now rather than at its next tick.
func (service *Service) nudgeWatcher() {
	select {
	case service.nudge <- struct{}{}:
	default:
	}
}

// projectScopes are the projects whose conversation exists.
func (service *Service) projectScopes() []string {
	entries, err := os.ReadDir(filepath.Join(service.config.Root, "conversations"))
	if err != nil {
		return nil
	}
	scopes := make([]string, 0, len(entries))
	for _, entry := range entries {
		scope, ok := strings.CutSuffix(entry.Name(), ".json")
		if ok && scope != TopLevel && scopePattern.MatchString(scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// observe records a project's work, notes what changed, and wakes the
// assistant for it when it is free.
func (service *Service) observe(
	ctx context.Context, scope string, snapshot func(context.Context, string) (view, error),
) error {
	seen, err := snapshot(ctx, scope)
	if err != nil {
		return err
	}
	current := seen.Work
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return err
	}
	events := changes(stored.Watched, current)
	watched := make(map[string]string, len(current))
	for id, item := range current {
		watched[id] = item.signature()
	}
	changed := !sameSignatures(stored.Watched, watched)
	stored.Watched = watched
	now := service.now()
	if stored.Goal.active() && seen.Plan != nil {
		before := fmt.Sprint(stored.Goal.Items)
		if stored.Goal.progress(*seen.Plan, current) {
			stored.Goal.Status, stored.Goal.EndedAt = GoalDone, &now
			events = append(events, Event{Text: "Autopilot finished: " + stored.Goal.Title + " is done.", Wake: true})
		}
		changed = changed || before != fmt.Sprint(stored.Goal.Items) || !stored.Goal.active()
	}
	for _, event := range events {
		stored.Messages = append(stored.Messages, Message{
			ID: messageID(stored, len(stored.Messages)), Role: "event", Text: event.Text, OccurredAt: now,
		})
		if event.Wake {
			stored.PendingEvents = append(stored.PendingEvents, event.Text)
		}
		stored.UpdatedAt = now
	}
	if stored.Status == StatusRunning {
		// refresh writes what it folds in.
		if stored, err = service.refresh(ctx, stored); err != nil || stored.Status == StatusRunning {
			if err == nil && (changed || len(events) > 0) {
				err = service.write(stored)
			}
			return err
		}
		changed = true
	}
	if len(stored.PendingEvents) == 0 && len(stored.Queued) == 0 {
		if changed || len(events) > 0 {
			return service.write(stored)
		}
		return nil
	}
	settings, err := service.Readiness(ctx)
	if err != nil || settings.Agent == "" || settings.Model == "" {
		return service.write(stored)
	}
	return service.startWaitingTurn(ctx, stored, settings)
}

// startWaitingTurn starts the turn for what waited while the assistant was
// busy: the user's queued messages, and the events it has yet to hear.
func (service *Service) startWaitingTurn(ctx context.Context, stored record, settings Settings) error {
	key := fmt.Sprintf("events:%d", stored.Turn+1)
	if len(stored.Queued) == 0 {
		prompt := eventsPrompt(stored.PendingEvents) +
			"\n\nTell the user briefly what this means and what comes next. Propose actions only when they help; " +
			"the user approves them as usual, unless you build everything here. When nothing needs the user, say so " +
			"in a line."
		stored.PendingEvents = nil
		_, err := service.startTurn(ctx, stored, settings, key, "event", prompt)
		return err
	}
	texts := make([]string, 0, len(stored.Queued))
	now := service.now()
	for _, queued := range stored.Queued {
		texts = append(texts, queued.Text)
		stored.Messages = append(stored.Messages, Message{
			ID: messageID(stored, len(stored.Messages)), Role: "user", Text: queued.Text, OccurredAt: now,
		})
	}
	stored.Queued = nil
	prompt := "The user says:\n" + strings.Join(texts, "\n\n")
	if len(stored.PendingEvents) > 0 {
		prompt = eventsPrompt(stored.PendingEvents) + "\n\n" + prompt
		stored.PendingEvents = nil
	}
	_, err := service.startTurn(ctx, stored, settings, "queued:"+key, "user", prompt)
	return err
}

func sameSignatures(a, b map[string]string) bool {
	if len(a) != len(b) || (a == nil) != (b == nil) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// Report tells a project's assistant about something Commitarium saw outside
// the project's work, such as an app that failed to start, and wakes it. A
// project whose conversation was never started has nobody to tell.
func (service *Service) Report(_ context.Context, scope, text string) error {
	text = strings.TrimSpace(text)
	if scope == TopLevel || !scopePattern.MatchString(scope) || text == "" {
		return ErrNotFound
	}
	service.mu.Lock()
	if _, err := os.Stat(service.path(scope)); errors.Is(err, os.ErrNotExist) {
		service.mu.Unlock()
		return nil
	}
	stored, err := service.read(scope)
	if err != nil {
		service.mu.Unlock()
		return err
	}
	// The same report from two windows is told once.
	if count := len(stored.Messages); count > 0 && stored.Messages[count-1].Role == "event" &&
		stored.Messages[count-1].Text == text {
		service.mu.Unlock()
		return nil
	}
	now := service.now()
	stored.addEvent(text, now)
	stored.PendingEvents = append(stored.PendingEvents, text)
	if strings.HasPrefix(text, PreviewFailure) {
		stored.PreviewFailedAt = &now
	}
	stored.UpdatedAt = now
	err = service.write(stored)
	service.mu.Unlock()
	if err == nil {
		service.nudgeWatcher()
	}
	return err
}

// PreviewFailure starts what the assistant hears when the app's preview
// failed to start, so the gap check knows it fails until a fix merges.
const PreviewFailure = "The app's preview failed to start"

// Wake tells a project's assistant about something the user did that it
// acts on where it builds everything (ADR-027), such as a work order the user
// created by hand, and wakes it for one turn. Unlike Report, it starts the
// project's conversation when there is none yet: building everything, the
// assistant owns the project whether or not the user opened it. note is the
// line the user sees; text what the assistant hears.
func (service *Service) Wake(ctx context.Context, scope, note, text string) error {
	return service.wake(ctx, scope, note, text, false)
}

// HandOver records that the user switched the project to building
// everything and wakes the assistant to take the project over. Cards the
// user dismissed before no longer stand: switching hands the work over.
func (service *Service) HandOver(ctx context.Context, scope string) error {
	return service.wake(ctx, scope, "You handed the project to the assistant: it builds everything from here.",
		"The user switched this project to Builds everything: you own it now. Take it over: write the vision "+
			"if there is none, set up the stack, start autopilot on the vision's open items, and start or queue "+
			"the waiting work orders, the user's too. Cards the user dismissed before this switch no longer "+
			"stand. Commitarium reports what is still missing as you go.", true)
}

func (service *Service) wake(ctx context.Context, scope, note, text string, handOver bool) error {
	text = strings.TrimSpace(text)
	if scope == TopLevel || !scopePattern.MatchString(scope) || text == "" {
		return ErrNotFound
	}
	if err := service.checkScope(ctx, scope); err != nil {
		return err
	}
	service.mu.Lock()
	stored, err := service.read(scope)
	if err != nil {
		service.mu.Unlock()
		return err
	}
	now := service.now()
	if handOver {
		stored.BuildsSince = &now
		// What was missing is told afresh to the new owner.
		stored.Gaps = nil
	}
	stored.addEvent(note, now)
	stored.PendingEvents = append(stored.PendingEvents, text)
	stored.UpdatedAt = now
	err = service.write(stored)
	service.mu.Unlock()
	if err == nil {
		service.nudgeWatcher()
	}
	return err
}

func eventsPrompt(events []string) string {
	return "Commitarium reports what happened in the project since your last turn:\n- " + strings.Join(events, "\n- ")
}
