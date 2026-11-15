package conversations

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// Facts and the idle look (ADR-041): what a project lacks to run (a vision,
// a stack, a preview that runs, work nobody starts) is told to Mayka as plain
// facts in her state, at every level, never as steps to take. Under Build
// everything the watcher also looks, in code and without a model call, for a
// project where nothing runs, nothing waits on the user, and the vision
// still has open items, and wakes her once with what is open. It wakes her
// again only after something changed: the open items, a work order's state,
// or the preview. What it last told is kept as a fingerprint in the
// project's shared state, so when she answers that nothing is left to do
// (the user said to skip an item, say), it stays quiet.

// maxIdleItems bounds the open items the idle look names.
const maxIdleItems = 12

// VisionSessions tells whether a project's vision is being written, and
// reading the open session stores a vision the assistant has finished.
type VisionSessions interface {
	OpenSession(ctx context.Context, projectID string) (vision.Session, error)
}

// projectState is what the facts and the idle look read of a project.
type projectState struct {
	App bool
	// Vision is nil while none is written; Writing while one is, and
	// Planning while the user plans it with the assistant.
	Vision            *featureartifact.VisionPlan
	Writing, Planning bool
	// StackReady when the stack is configured; SettingUp while the project's
	// setup work order runs.
	StackReady, SettingUp bool
	HasCode, HasCompose   bool
	Features              []feature.Feature
	// Runs are the newest runs of the unfinished background work orders.
	Runs map[string]execution.Run
}

// fact is one thing a project lacks to run, told as it is.
type fact struct {
	Kind string
	Text string
}

const (
	factVision  = "vision"
	factStack   = "stack"
	factOpen    = "open_items"
	factWaiting = "waiting_work"
	factPreview = "preview"
)

// Facts says what a project lacks to run, as plain facts for Mayka's state
// (StateDescriber.Facts); none for the top level or a project it cannot
// read.
func (watcher Watcher) Facts(ctx context.Context, scope string) []string {
	if scope == TopLevel || watcher.Visions == nil || watcher.Toolchains == nil {
		return nil
	}
	current, err := watcher.Service.config.Projects.GetByID(ctx, scope)
	if err != nil {
		return nil
	}
	stored, err := watcher.Service.readEvery(scope)
	if err != nil {
		return nil
	}
	state, err := watcher.projectState(ctx, current)
	if err != nil {
		return nil
	}
	texts := make([]string, 0)
	for _, item := range projectFacts(state, stored) {
		texts = append(texts, item.Text)
	}
	return texts
}

// idleLook wakes a project's assistant once when, under Build everything,
// nothing runs or waits on the user and the vision still has open items.
func (watcher Watcher) idleLook(ctx context.Context, scope string) {
	service := watcher.Service
	current, err := service.config.Projects.GetByID(ctx, scope)
	if err != nil || !current.BuildsEverything() || watcher.Visions == nil || watcher.Toolchains == nil {
		return
	}
	stored, err := service.readEvery(scope)
	if err != nil || !stored.free() || stored.waitsOnUser() {
		return
	}
	if chats, err := service.chatsOf(scope); err != nil || service.busy(scope, chats) {
		// Another chat's turn is on it already.
		return
	}
	state, err := watcher.projectState(ctx, current)
	if err != nil {
		log.Printf("assistant idle look for %s: %v", scope, err)
		return
	}
	if state.Vision == nil || state.active() {
		return
	}
	open := openItems(*state.Vision, state.Features, stored.Goal.dropped())
	if len(open) == 0 {
		return
	}
	fingerprint := idleFingerprint(open, state, stored)
	if fingerprint == stored.IdleLook {
		return
	}
	settings, err := service.Readiness(ctx)
	if err != nil {
		return
	}
	if watcher.AgentLimited != nil && watcher.AgentLimited(ctx, settings.Agent) {
		// Her turn would fail on the limit; she hears of it once it resets.
		return
	}
	if err := service.tellIdle(scope, fingerprint, idlePrompt(open, projectFacts(state, stored))); err != nil {
		log.Printf("assistant idle look for %s: %v", scope, err)
	}
}

// free reports whether the assistant has nothing on its plate: no turn
// running, no events or messages waiting, and no new app's launch, which
// moves on by itself (launch.go).
func (stored record) free() bool {
	return stored.Status != StatusRunning && len(stored.PendingEvents) == 0 && len(stored.Queued) == 0 &&
		!stored.launching() && stored.SignIn == nil
}

// waitsOnUser reports whether a card waits for the user, or one of her
// actions waits to apply (a queued or held start among them).
func (stored record) waitsOnUser() bool {
	return slices.ContainsFunc(stored.everyAction(), func(action Action) bool {
		return action.Status == ActionProposed
	})
}

// launching reports whether a new app's launch has not yet reached building.
func (stored record) launching() bool {
	return stored.Launch != nil && stored.Launch.Stage != LaunchBuilding
}

// active reports whether any of the project's work runs or waits past its
// start: planned, implemented, reviewed, or waiting to merge.
func (state projectState) active() bool {
	return slices.ContainsFunc(state.Features, func(item feature.Feature) bool {
		switch item.State {
		case feature.StatePlanning, feature.StateImplementing, feature.StateReviewing, feature.StateReadyToMerge:
			return true
		}
		return false
	})
}

// tellIdle wakes the assistant with what is open, once per fingerprint.
func (service *Service) tellIdle(scope, fingerprint, prompt string) error {
	stored, done, err := service.open(scope, "")
	if err != nil {
		return err
	}
	defer done()
	if !stored.free() || stored.IdleLook == fingerprint {
		return nil
	}
	now := service.now()
	stored.IdleLook = fingerprint
	stored.addEvent("Nothing is running; Mayka looks at what the vision still has open.", now)
	stored.PendingEvents = append(stored.PendingEvents, prompt)
	stored.UpdatedAt = now
	if err := service.write(stored); err != nil {
		return err
	}
	service.nudgeWatcher()
	return nil
}

// idlePrompt says what the idle look found, as facts.
func idlePrompt(open []GoalItem, facts []fact) string {
	lines := make([]string, 0, len(open))
	for index, item := range open {
		if index == maxIdleItems {
			lines = append(lines, fmt.Sprintf("and %d more", len(open)-maxIdleItems))
			break
		}
		lines = append(lines, fmt.Sprintf("%s “%s” (item %s)", item.Label, item.Title, item.ID))
	}
	prompt := "Nothing is running, nothing waits for the user, and the vision still has open items:\n  - " +
		strings.Join(lines, "\n  - ")
	others := make([]string, 0, len(facts))
	for _, item := range facts {
		if item.Kind != factOpen {
			others = append(others, item.Text)
		}
	}
	if len(others) > 0 {
		prompt += "\nThe project also lacks:\n  - " + strings.Join(others, "\n  - ")
	}
	return prompt + "\nCommitarium tells you this once; it tells you again only after something changes."
}

// idleFingerprint is what the idle look last told: the open items, every
// work order's state, and when the preview last failed.
func idleFingerprint(open []GoalItem, state projectState, stored record) string {
	parts := make([]string, 0, len(open)+len(state.Features)+1)
	for _, item := range open {
		parts = append(parts, "item:"+item.ID)
	}
	for _, item := range state.Features {
		parts = append(parts, "work:"+item.ID+"="+string(item.State))
	}
	sort.Strings(parts)
	if stored.PreviewFailedAt != nil {
		parts = append(parts, "preview:"+stored.PreviewFailedAt.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, ";")
}

func (watcher Watcher) projectState(ctx context.Context, current project.Project) (projectState, error) {
	state := projectState{App: current.App, Runs: map[string]execution.Run{}}
	plan, err := watcher.Visions.Get(ctx, current.ID)
	switch {
	case err == nil && (plan.Planning.Open() || plan.Planning.Paused()):
		// Being planned with the user, or paused: nothing is missing until
		// they build it.
		state.Planning = true
	case err == nil:
		state.Vision = &plan.Plan
	case errors.Is(err, vision.ErrNotFound):
		if watcher.VisionSessions != nil {
			_, err := watcher.VisionSessions.OpenSession(ctx, current.ID)
			state.Writing = err == nil
		}
	default:
		return projectState{}, err
	}
	manifest, err := watcher.Toolchains.Get(ctx, current.ID)
	if err != nil {
		return projectState{}, err
	}
	state.StackReady = manifest.Status == toolchain.StatusConfigured
	if current.ForgejoRepository != nil {
		// A repository that cannot be read counts as empty: no stack or preview
		// to set up from its code.
		if overview, err := watcher.Service.config.Projects.GetRepositoryOverview(ctx, current.ID); err == nil {
			state.HasCode, state.HasCompose = overview.HasCode(), overview.HasCompose()
		}
	}
	if state.Features, err = watcher.Features.List(ctx, current.ID); err != nil {
		return projectState{}, err
	}
	for _, item := range state.Features {
		if item.State.IsTerminal() {
			continue
		}
		if item.Title == projectSetupTitle {
			state.SettingUp = true
		}
		if item.Background || item.PreviewCheck {
			runs, err := watcher.Runs.RunsForFeature(ctx, item.ID)
			if err != nil {
				return projectState{}, err
			}
			if run, ok := newest(runs); ok {
				state.Runs[item.ID] = run
			}
		}
	}
	return state, nil
}

// projectFacts lists what a project still lacks to run, as it is.
func projectFacts(state projectState, stored record) []fact {
	facts := make([]fact, 0)
	if state.Planning {
		return facts
	}
	if state.Vision == nil && !state.Writing {
		facts = append(facts, fact{factVision, "The project has no vision yet."})
	}
	if !state.StackReady && !state.SettingUp && !stored.designing() && (state.Vision != nil || state.HasCode) {
		text := "Its stack is not set up."
		if state.Vision != nil && (state.Vision.Stack == nil || len(state.Vision.Stack.Presets) == 0) {
			text = "Its stack is not set up, and the vision names no stack."
		}
		facts = append(facts, fact{factStack, text})
	}
	if state.Vision != nil && !state.active() && !stored.waitsOnUser() {
		if open := openItems(*state.Vision, state.Features, stored.Goal.dropped()); len(open) > 0 {
			facts = append(facts, fact{factOpen, fmt.Sprintf("The vision has %d open %s, and no work runs or waits to start.",
				len(open), plural(len(open), "item", "items"))})
		}
	}
	if state.StackReady {
		if waiting := waitingWork(state.Features, stored); len(waiting) > 0 {
			facts = append(facts, fact{factWaiting, "Nothing runs, and these work orders are not started: " +
				strings.Join(waiting, ", ") + "."})
		}
	}
	if state.App && state.HasCode {
		if text := previewFact(state, stored); text != "" {
			facts = append(facts, fact{factPreview, text})
		}
	}
	return facts
}

// openItems lists the vision's open items, in priority order: those without
// a merged work order, leaving out the goal items Mayka dropped.
func openItems(plan featureartifact.VisionPlan, features []feature.Feature, dropped map[string]bool) []GoalItem {
	merged := map[string]bool{}
	for _, item := range features {
		merged[item.ID] = item.State == feature.StateCompleted
	}
	levels := make([]int, 0, len(plan.Priorities))
	for _, priority := range plan.Priorities {
		levels = append(levels, priority.Level)
	}
	sort.Ints(levels)
	open := make([]GoalItem, 0)
	for _, level := range slices.Compact(levels) {
		for _, item := range priorityItems(plan, level) {
			if item.Done || merged[item.WorkOrderID] || dropped[item.ID] {
				continue
			}
			open = append(open, item)
		}
	}
	return open
}

// waitingWork names the drafts and ready work orders nothing starts, while
// nothing in the project runs (a run that merges wakes the assistant anyway,
// and it may hold work back on purpose meanwhile). A work order an action of
// the assistant waits on is the assistant's already, and one whose card the
// user dismissed while it built everything the user turned down. A card
// dismissed while it assisted does not count: switching hands the work over.
func waitingWork(features []feature.Feature, stored record) []string {
	waiting := make([]string, 0)
	for _, item := range features {
		if item.Background || item.PreviewCheck || item.Kind == feature.KindSession {
			continue
		}
		switch item.State {
		case feature.StateDraft, feature.StateReady:
		case feature.StatePlanning, feature.StateImplementing, feature.StateReviewing, feature.StateReadyToMerge:
			return nil
		default:
			continue
		}
		if !stored.handles(item.ID) {
			waiting = append(waiting, fmt.Sprintf("“%s” (%s, %s)", item.Title, item.ID, item.State))
		}
	}
	return waiting
}

// handles reports whether one of the assistant's actions on a work order
// waits, one of its helpers works on it, or the user turned one down since
// the assistant builds everything.
func (stored record) handles(workOrderID string) bool {
	if stored.writing(workOrderID) {
		return true
	}
	for _, action := range stored.everyAction() {
		if action.WorkOrderID != workOrderID {
			continue
		}
		switch {
		case action.Status == ActionProposed:
			return true
		case action.Status == ActionDismissed && (stored.BuildEverythingSince == nil || action.UpdatedAt.After(*stored.BuildEverythingSince)):
			return true
		}
	}
	return false
}

// previewFact says what keeps an app's preview from running, as far as the
// coordinator knows: no compose file, setup work that is stuck, or a failure
// the desktop reported that no merged preview work has fixed. Work that sets
// the preview up and runs is no fact to tell.
func previewFact(state projectState, stored record) string {
	var latestMerged time.Time
	for _, item := range state.Features {
		if !item.Background && !item.PreviewCheck {
			continue
		}
		if item.State == feature.StateCompleted && item.UpdatedAt.After(latestMerged) {
			latestMerged = item.UpdatedAt
		}
		if item.State.IsTerminal() {
			continue
		}
		run, started := state.Runs[item.ID]
		stuck := started && (run.Status == execution.RunStatusFailed ||
			(run.Status == execution.RunStatusWaitingForUser && run.WaitKind == execution.RunWaitKindBlocker))
		if !stuck {
			return ""
		}
		if stored.handles(item.ID) {
			return ""
		}
		return fmt.Sprintf("The work setting the app's preview up, “%s” (%s), is stuck: %s.",
			item.Title, item.ID, strings.TrimSuffix(orNothing(firstLine(run.Reason)), "."))
	}
	if !state.HasCompose {
		return "The app's preview is not set up: the repository has no compose file."
	}
	if stored.PreviewFailedAt != nil && !latestMerged.After(*stored.PreviewFailedAt) {
		return "The app's preview failed to start, and no preview work has merged since."
	}
	return ""
}

func orNothing(reason string) string {
	if reason == "" {
		return "no reason given"
	}
	return reason
}
