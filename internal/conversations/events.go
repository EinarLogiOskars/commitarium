package conversations

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
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
// in a chat, and wakes the assistant there for what matters, one turn at a
// time per chat. An event goes to the chat whose action created or started
// its work order, autopilot's to the chat that started the goal, and the
// rest to the most recently used chat (ADR-038). Events that arrive while a
// chat's turn runs wait for its next turn.

type RunReader interface {
	RunsForFeature(ctx context.Context, featureID string) ([]execution.Run, error)
}

type LimitWaitReader interface {
	LimitWaitForRun(ctx context.Context, runID string) (execution.LimitWait, error)
}

type RetryWaitReader interface {
	RetryWaitForRun(ctx context.Context, runID string) (execution.RetryWait, error)
}

// RecoveryReader reads the recoveries tried on a run (ADR-032).
type RecoveryReader interface {
	RunRecoveries(ctx context.Context, runID string) ([]execution.Recovery, error)
}

// Event is something that happened in a project. Wake asks for the
// assistant's turn; otherwise it is only noted in the conversation, where
// the drawer's dot shows it, and the assistant reads the project's current
// state on its next turn anyway. News wakes it only where it drives the work
// (an autopilot goal, or building everything): there it moves on from it;
// elsewhere it would only repeat it.
type Event struct {
	Text string
	Wake bool
	News bool
	// WorkOrderID is the work order it is about, which decides its chat.
	WorkOrderID string
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
	// Recoveries, when set, tells a work order's first round limit, which
	// the assistant handles, from one after it did, which is the user's.
	Recoveries RecoveryReader
	// Toolchains and VisionSessions let the gap check see a project's stack
	// and a vision being written; VisionSessions also lets each look store a
	// vision the assistant has finished writing. AgentLimited, when set, tells it the
	// assistant's agent is at its usage limit (gaps.go).
	Toolchains     ToolchainReader
	VisionSessions VisionSessions
	AgentLimited   func(ctx context.Context, agentID string) bool
	// Helpers, when set, reads the helpers the assistant started, so their
	// questions and results come back to it (helpers.go).
	Helpers HelperReader
	// ReleaseImplementation, when set, has a run that planned early look
	// again at whether it may implement (ADR-037).
	ReleaseImplementation func(ctx context.Context, runID string) error
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
	watcher.Service.RetryDueSignIns(ctx)
	for _, scope := range watcher.Service.projectScopes() {
		watcher.settleVision(ctx, scope)
		watcher.followHelpers(ctx, scope)
		if err := watcher.Service.observe(ctx, scope, watcher.snapshot); err != nil {
			log.Printf("assistant events for %s: %v", scope, err)
		}
		watcher.launch(ctx, scope)
		watcher.releaseHeld(ctx, scope)
		watcher.autopilot(ctx, scope)
		watcher.gapCheck(ctx, scope)
	}
	// The top level has no project's work, but research it started comes
	// back to it, and is sent without a card (research.go).
	watcher.followHelpers(ctx, TopLevel)
	watcher.autopilot(ctx, TopLevel)
	if err := watcher.Service.wakeChats(ctx, TopLevel); err != nil {
		log.Printf("assistant events for %s: %v", TopLevel, err)
	}
}

// wakeChats folds in each of a scope's chats' finished turns, and starts
// the turn for what waits in each: events, and the user's messages queued
// while the assistant answered.
func (service *Service) wakeChats(ctx context.Context, scope string) error {
	chats, err := service.chatsOf(scope)
	if err != nil {
		return err
	}
	for _, chat := range chats {
		if err := service.wakeIfWaiting(ctx, scope, chat.ID); err != nil {
			return err
		}
	}
	return nil
}

// wakeIfWaiting folds in a chat's finished turn and starts the turn for what
// waits in it.
func (service *Service) wakeIfWaiting(ctx context.Context, scope, chat string) error {
	stored, done, err := service.open(scope, chat)
	if err != nil {
		return err
	}
	defer done()
	if stored.Status == StatusRunning {
		// refresh writes what it folds in.
		if stored, err = service.refresh(ctx, stored); err != nil || stored.Status == StatusRunning {
			return err
		}
	}
	if (len(stored.PendingEvents) == 0 && len(stored.Queued) == 0) || stored.SignIn != nil {
		return nil
	}
	settings, err := service.Readiness(ctx)
	if err != nil || settings.Agent == "" || settings.Model == "" {
		return err
	}
	return service.startWaitingTurn(ctx, stored, settings)
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

// releaseHeld has each run that planned early and waits to implement look
// again once what held it changed: the work it follows merged, or a slot
// freed (ADR-037). The wait is the run's own, so this picks it up after a
// restart too.
func (watcher Watcher) releaseHeld(ctx context.Context, scope string) {
	if watcher.ReleaseImplementation == nil || watcher.Runs == nil {
		return
	}
	stored, err := watcher.Service.readEvery(scope)
	if err != nil {
		return
	}
	_, _, states, _ := watcher.Service.workOrders(ctx, scope)
	for workOrderID := range stored.earlyPlanners() {
		if states[workOrderID] != feature.StatePlanning {
			continue
		}
		runs, err := watcher.Runs.RunsForFeature(ctx, workOrderID)
		if err != nil {
			continue
		}
		run, ok := newest(runs)
		if !ok || run.Paused || run.Status != execution.RunStatusWaitingForUser ||
			run.WaitKind != execution.RunWaitKindPhaseCheckpoint || !execution.HeldBeforeImplementation(run.Reason) {
			continue
		}
		if reason, _ := watcher.Service.ImplementationHold(ctx, scope, workOrderID); reason == run.Reason {
			continue
		}
		if err := watcher.ReleaseImplementation(ctx, run.ID); err != nil {
			log.Printf("release %s to implement: %v", workOrderID, err)
		}
	}
}

// autopilot applies the actions autopilot takes over. One that fails stays
// a card for the user, with a note saying why, unless it failed because it
// was no longer needed.
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
		if err != nil && watcher.Service.settleIfStale(ctx, scope, action) {
			// Refused only because its work order got there first: nothing
			// waits for the user.
			continue
		}
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
	// The chat that proposed it hears what became of it.
	chat, err := service.chatOfAction(context.Background(), scope, action.ID)
	if errors.Is(err, ErrNotFound) {
		chat = ""
	} else if err != nil {
		return err
	}
	stored, done, err := service.open(scope, chat)
	if err != nil {
		return err
	}
	defer done()
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
	// A card the user approves later starts what it creates; until then it
	// is left for them.
	for index := range stored.Actions {
		if stored.Actions[index].ID == action.ID {
			stored.Actions[index].HoldStart = false
			stored.Actions[index].PlansEarly = false
			stored.Actions[index].LeftForUser = true
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
	// RoundCapHandled says the assistant already handled a round limit of
	// the work order.
	RoundCapHandled bool
	State           feature.State
	RunID           string
	RunStatus       execution.RunStatus
	WaitKind        execution.RunWaitKind
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
	if item.RoundCapHandled {
		signature += "|round-cap-handled"
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
				if watcher.Recoveries != nil && run.Status == execution.RunStatusWaitingForUser &&
					run.WaitKind == execution.RunWaitKindRoundCap {
					for _, earlier := range runs {
						if recoveries, err := watcher.Recoveries.RunRecoveries(ctx, earlier.ID); err == nil &&
							execution.RoundCapHandled(recoveries) {
							current.RoundCapHandled = true
						}
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
			event.WorkOrderID = id
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
		return Event{Text: name + " merged.", Wake: true, News: true}, true
	case item.State.IsTerminal():
		return Event{}, false
	case item.State == feature.StateReady && item.RunStatus == execution.RunStatusStopped:
		// Returned to Ready to start over: under autopilot it starts again.
		return Event{Text: name + " is back in Ready to start over.", Wake: true, News: true}, true
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
			// Its gates run and it merges on its own, when its turn in the
			// merge queue comes (ADR-036).
			if place, found := execution.MergeQueuePlace(item.Reason); found {
				return Event{Text: name + " is waiting to merge, " + place + "."}, true
			}
			return Event{}, false
		}
		return Event{Text: name + " is ready to merge and waits for the user's approval.", Wake: true}, true
	case execution.RunWaitKindBlocker:
		return Event{Text: with(name + " is blocked"), Wake: true}, true
	case execution.RunWaitKindRoundCap:
		if item.RoundCapHandled {
			return Event{Text: name + " reached its round limit again after you handled it once, so it is the " +
				"user's now: do not continue it again. Tell them plainly, in a sentence or two, the one point its " +
				"agents cannot settle, and ask for their decision.", Wake: true}, true
		}
		return Event{Text: name + " reached its round limit: its agents could not agree. Handle it before the " +
			"user: read their last messages in the state, then continue it with guidance that settles the open " +
			"point, or send it back to Ready (see How you handle round limits). Ask the user only for a choice " +
			"that is truly theirs.", Wake: true}, true
	case execution.RunWaitKindPhaseCheckpoint:
		// Planned early, it implements on its own once nothing holds it
		// (ADR-037).
		if what, held := execution.ImplementationHoldWhat(item.Reason); held {
			return Event{Text: name + " is planned and implements " + what}, true
		}
		return Event{Text: with(name + " is waiting for the user"), Wake: true}, true
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

// projectScopes are the projects whose conversation exists: a folder of
// chats, or a single conversation stored before chats.
func (service *Service) projectScopes() []string {
	entries, err := os.ReadDir(filepath.Join(service.config.Root, "conversations"))
	if err != nil {
		return nil
	}
	scopes := make([]string, 0, len(entries))
	for _, entry := range entries {
		scope, ok := entry.Name(), entry.IsDir()
		if !ok {
			scope, ok = strings.CutSuffix(entry.Name(), ".json")
		}
		if ok && scope != TopLevel && scopePattern.MatchString(scope) && !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// observe records a project's work, notes what changed in the chat each
// change goes to, and wakes the assistant there when it is free.
func (service *Service) observe(
	ctx context.Context, scope string, snapshot func(context.Context, string) (view, error),
) error {
	seen, err := snapshot(ctx, scope)
	if err != nil {
		return err
	}
	current := seen.Work
	driving := service.assistantBuilds(ctx, scope)
	routed := map[string][]Event{}
	err = service.updateShared(scope, func(shared *sharedState) bool {
		events := changes(shared.Watched, current)
		watched := make(map[string]string, len(current))
		for id, item := range current {
			watched[id] = item.signature()
		}
		changed := !sameSignatures(shared.Watched, watched)
		shared.Watched = watched
		chats, err := service.chatsLocked(scope)
		if err != nil {
			return changed
		}
		for _, event := range events {
			chat := routeWorkOrder(chats, shared.Goal, event.WorkOrderID)
			routed[chat] = append(routed[chat], event)
		}
		driving = driving || shared.Goal.active()
		if shared.Goal.active() && seen.Plan != nil {
			before := fmt.Sprint(shared.Goal.Items)
			if shared.Goal.progress(*seen.Plan, current) {
				now := service.now()
				shared.Goal.Status, shared.Goal.EndedAt = GoalDone, &now
				chat := ""
				if shared.Goal.Chat != "" && slices.ContainsFunc(chats, func(state chatState) bool {
					return state.ID == shared.Goal.Chat && !state.Archived
				}) {
					chat = shared.Goal.Chat
				}
				routed[chat] = append(routed[chat], Event{Text: "Autopilot finished: " + shared.Goal.Title + " is done.", Wake: true})
			}
			changed = changed || before != fmt.Sprint(shared.Goal.Items) || !shared.Goal.active()
		}
		return changed
	})
	if err != nil {
		return err
	}
	chats := make([]string, 0, len(routed))
	for chat := range routed {
		chats = append(chats, chat)
	}
	slices.Sort(chats)
	for _, chat := range chats {
		if err := service.tell(scope, chat, routed[chat], driving); err != nil {
			return err
		}
	}
	return service.wakeChats(ctx, scope)
}

// tell notes events in a chat ("" for the most recently used one), and has
// those that need the assistant wait for its next turn there.
func (service *Service) tell(scope, chat string, events []Event, driving bool) error {
	stored, done, err := service.open(scope, chat)
	if err != nil {
		return err
	}
	defer done()
	now := service.now()
	for _, event := range events {
		stored.addEvent(event.Text, now)
		if event.Wake && (!event.News || driving) {
			stored.PendingEvents = append(stored.PendingEvents, event.Text)
		}
	}
	stored.UpdatedAt = now
	return service.write(stored)
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
		texts = append(texts, withAttachments(queued.Text, queued.Attachments))
		stored.Messages = append(stored.Messages, Message{
			ID: messageID(stored, len(stored.Messages)), Role: "user", Text: queued.Text,
			Attachments: queued.Attachments, OccurredAt: now,
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
	if chats, err := service.chatsOf(scope); err != nil || len(chats) == 0 {
		return err
	}
	stored, done, err := service.open(scope, "")
	if err != nil {
		return err
	}
	// A new app's preview fails until its setup creates the stack; the launch
	// moves along by itself, so the assistant is not woken over it.
	if strings.HasPrefix(text, PreviewFailure) && stored.launching() {
		done()
		return nil
	}
	// The same report from two windows is told once.
	if count := len(stored.Messages); count > 0 && stored.Messages[count-1].Role == "event" &&
		stored.Messages[count-1].Text == text {
		done()
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
	done()
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
	return service.wake(ctx, scope, "", note, text, false)
}

// WakeChat wakes the assistant in one of the scope's chats, as Wake does
// in its most recently used one.
func (service *Service) WakeChat(ctx context.Context, scope, chat, note, text string) error {
	if chat == "" {
		return ErrNotFound
	}
	return service.wake(ctx, scope, chat, note, text, false)
}

// TellChat notes something in one of the scope's chats, as WakeChat does,
// for the assistant to hear with its next turn there, without starting one:
// the user's message that follows at once starts it (ADR-039: /plan and a
// message). If none comes, the watcher starts it at its next look.
func (service *Service) TellChat(ctx context.Context, scope, chat, note, text string) error {
	if chat == "" {
		return ErrNotFound
	}
	return service.wakeQuietly(ctx, scope, chat, note, text, false, false)
}

// HandOver records that the user switched the project to building
// everything and wakes the assistant to take the project over. Cards the
// user dismissed before no longer stand: switching hands the work over.
func (service *Service) HandOver(ctx context.Context, scope string) error {
	return service.wake(ctx, scope, "", "You handed the project to Mayka: she builds everything from here.",
		"The user switched this project to Builds everything: you own it now. Take it over: write the vision "+
			"if there is none, set up the stack, start autopilot on the vision's open items, and start or queue "+
			"the waiting work orders, the user's too. Cards the user dismissed before this switch no longer "+
			"stand. Commitarium reports what is still missing as you go.", true)
}

func (service *Service) wake(ctx context.Context, scope, chat, note, text string, handOver bool) error {
	return service.wakeQuietly(ctx, scope, chat, note, text, handOver, true)
}

// wakeQuietly is wake, which looks for the turn at once only with nudge.
func (service *Service) wakeQuietly(ctx context.Context, scope, chat, note, text string, handOver, nudge bool) error {
	text = strings.TrimSpace(text)
	if scope == TopLevel || !scopePattern.MatchString(scope) || text == "" {
		return ErrNotFound
	}
	if err := service.checkScope(ctx, scope); err != nil {
		return err
	}
	if chat != "" {
		var err error
		if chat, err = service.chatFor(InChat(ctx, chat), scope); err != nil {
			return err
		}
	}
	stored, done, err := service.open(scope, chat)
	if err != nil {
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
	done()
	if err == nil && nudge {
		service.nudgeWatcher()
	}
	return err
}

func eventsPrompt(events []string) string {
	return "Commitarium reports what happened in the project since your last turn:\n- " + strings.Join(events, "\n- ")
}
