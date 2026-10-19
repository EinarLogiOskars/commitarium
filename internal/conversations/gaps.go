package conversations

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// The gap check (ADR-027): building everything, the assistant owns the
// project but acts only when woken. So the watcher looks, in code and without
// a model call, for what a project still lacks to run (a vision, a stack,
// autopilot on the vision's open items, work orders nobody starts, and for an
// app a preview that runs) and wakes the assistant once with the list. Each
// gap is told at most once per gapEvery, at most maxGapTells times in a row
// while it stays, and never while the assistant answers, has events or
// messages waiting, or its agent is at its usage limit. What it told is kept
// in the conversation record.

const (
	gapEvery    = 10 * time.Minute
	maxGapTells = 3
)

type gapKind string

const (
	gapVision    gapKind = "vision"
	gapStack     gapKind = "stack"
	gapAutopilot gapKind = "autopilot"
	gapWaiting   gapKind = "waiting_work"
	gapPreview   gapKind = "preview"
)

// gapTold is when a gap was last told, and how often in a row.
type gapTold struct {
	At    time.Time `json:"at"`
	Count int       `json:"count"`
}

type gap struct {
	Kind gapKind
	Text string
}

// VisionSessions tells whether a project's vision is being written, and
// reading the open session stores a vision the assistant has finished.
type VisionSessions interface {
	OpenSession(ctx context.Context, projectID string) (vision.Session, error)
}

// projectState is what the gap check reads of a project.
type projectState struct {
	App bool
	// Vision is nil while none is written; Writing while one is.
	Vision  *featureartifact.VisionPlan
	Writing bool
	// StackReady when the stack is configured; SettingUp while the project's
	// setup work order runs.
	StackReady, SettingUp bool
	HasCode, HasCompose   bool
	Features              []feature.Feature
	// Runs are the newest runs of the unfinished work orders.
	Runs map[string]execution.Run
}

// gapCheck wakes a project's assistant with what the project lacks, where
// it builds everything.
func (watcher Watcher) gapCheck(ctx context.Context, scope string) {
	service := watcher.Service
	current, err := service.config.Projects.GetByID(ctx, scope)
	if err != nil || !current.AssistantBuilds() || watcher.Visions == nil || watcher.Toolchains == nil {
		return
	}
	stored, err := service.readLocked(scope)
	if err != nil || !stored.free() {
		return
	}
	settings, err := service.Readiness(ctx)
	if err != nil {
		return
	}
	state, err := watcher.projectState(ctx, current)
	if err != nil {
		log.Printf("assistant gap check for %s: %v", scope, err)
		return
	}
	found := findGaps(state, stored)
	if len(dueGaps(stored, found, service.now())) > 0 && watcher.AgentLimited != nil &&
		watcher.AgentLimited(ctx, settings.Agent) {
		// Its turn would fail on the limit; the gaps are told once it resets.
		return
	}
	if err := service.tellGaps(scope, found); err != nil {
		log.Printf("assistant gaps for %s: %v", scope, err)
	}
}

// free reports whether the assistant has nothing on its plate: no turn
// running, no events or messages waiting, and no new app's launch, which
// moves on by itself (launch.go).
func (stored record) free() bool {
	return stored.Status != StatusRunning && len(stored.PendingEvents) == 0 && len(stored.Queued) == 0 &&
		!stored.launching() && stored.SignIn == nil
}

// launching reports whether a new app's launch has not yet reached building.
func (stored record) launching() bool {
	return stored.Launch != nil && stored.Launch.Stage != LaunchBuilding
}

func (watcher Watcher) projectState(ctx context.Context, current project.Project) (projectState, error) {
	state := projectState{App: current.App, Runs: map[string]execution.Run{}}
	plan, err := watcher.Visions.Get(ctx, current.ID)
	switch {
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

// findGaps lists what a project still lacks to run, in the order the
// assistant fixes it.
func findGaps(state projectState, stored record) []gap {
	gaps := make([]gap, 0)
	if state.Vision == nil && !state.Writing {
		gaps = append(gaps, gap{gapVision, "The project has no vision yet. Propose write_vision with what it is for, " +
			"from the user's words, your notes, and the repository."})
	}
	if !state.StackReady && !state.SettingUp && (state.Vision != nil || state.HasCode) {
		gaps = append(gaps, gap{gapStack, "Its stack is not set up. Propose set_up_stack: it sets the project up on " +
			"the stack the vision recommends, or detects the stack of the code."})
	}
	if state.Vision != nil && state.StackReady && !stored.Goal.active() {
		if level, open := openItems(*state.Vision, state.Features); open > 0 {
			gaps = append(gaps, gap{gapAutopilot, fmt.Sprintf("The vision has %d open %s and autopilot is off. "+
				"Propose start_autopilot for P%d, the first priority with open items.", open, plural(open, "item", "items"), level)})
		}
	}
	if state.StackReady {
		if waiting := waitingWork(state.Features, stored); len(waiting) > 0 {
			gaps = append(gaps, gap{gapWaiting, "Nothing runs, and these work orders wait: " + strings.Join(waiting, ", ") +
				". Start them (start_work_order with the agents and models you choose), one after the other where " +
				"they change the same part of the app."})
		}
	}
	if state.App && state.HasCode {
		if text := previewGap(state, stored); text != "" {
			gaps = append(gaps, gap{gapPreview, text})
		}
	}
	return gaps
}

// openItems counts the vision's open items, those without a merged work
// order, and names the first priority that has any.
func openItems(plan featureartifact.VisionPlan, features []feature.Feature) (int, int) {
	merged := map[string]bool{}
	for _, item := range features {
		merged[item.ID] = item.State == feature.StateCompleted
	}
	levels := make([]int, 0, len(plan.Priorities))
	for _, priority := range plan.Priorities {
		levels = append(levels, priority.Level)
	}
	sort.Ints(levels)
	first, open := -1, 0
	for _, level := range levels {
		for _, item := range priorityItems(plan, level) {
			if item.Done || merged[item.WorkOrderID] {
				continue
			}
			open++
			if first < 0 {
				first = level
			}
		}
	}
	return first, open
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
	for _, action := range stored.Actions {
		if action.WorkOrderID != workOrderID {
			continue
		}
		switch {
		case action.Status == ActionProposed:
			return true
		case action.Status == ActionDismissed && (stored.BuildsSince == nil || action.UpdatedAt.After(*stored.BuildsSince)):
			return true
		}
	}
	return false
}

// previewGap says what keeps an app's preview from running, as far as the
// coordinator knows: no compose file, setup work that is stuck, or a failure
// the desktop reported that no merged preview work has fixed. Work that sets
// the preview up and runs is no gap.
func previewGap(state projectState, stored record) string {
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
		return fmt.Sprintf("The work setting the app's preview up, “%s” (%s), is stuck: %s. Propose "+
			"continue_work_order with guidance that fixes it.", item.Title, item.ID, orNothing(firstLine(run.Reason)))
	}
	if !state.HasCompose {
		return "The app's preview is not set up: the repository has no compose file. Propose set_up_preview."
	}
	if stored.PreviewFailedAt != nil && !latestMerged.After(*stored.PreviewFailedAt) {
		return "The app's preview failed to start, and nothing fixes it yet. Propose set_up_preview with what fails."
	}
	return ""
}

func orNothing(reason string) string {
	if reason == "" {
		return "no reason given"
	}
	return reason
}

// dueGaps are the gaps to tell now.
func dueGaps(stored record, found []gap, now time.Time) []gap {
	due := make([]gap, 0, len(found))
	for _, item := range found {
		told, seen := stored.Gaps[string(item.Kind)]
		if !seen || (now.Sub(told.At) >= gapEvery && told.Count < maxGapTells) {
			due = append(due, item)
		}
	}
	return due
}

// tellGaps wakes the assistant with the gaps that are due, and forgets those
// that closed, so one that opens again is told at once.
func (service *Service) tellGaps(scope string, found []gap) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil || !stored.free() {
		return err
	}
	open := map[string]bool{}
	for _, item := range found {
		open[string(item.Kind)] = true
	}
	changed := false
	for kind := range stored.Gaps {
		if !open[kind] {
			delete(stored.Gaps, kind)
			changed = true
		}
	}
	now := service.now()
	due := dueGaps(stored, found, now)
	if len(due) == 0 {
		if changed {
			return service.write(stored)
		}
		return nil
	}
	if stored.Gaps == nil {
		stored.Gaps = map[string]gapTold{}
	}
	texts, kinds := make([]string, 0, len(due)), make([]string, 0, len(due))
	for _, item := range due {
		told := stored.Gaps[string(item.Kind)]
		stored.Gaps[string(item.Kind)] = gapTold{At: now, Count: told.Count + 1}
		texts = append(texts, item.Text)
		kinds = append(kinds, gapNames[item.Kind])
	}
	stored.addEvent("The assistant picks up what the project still needs: "+strings.Join(kinds, ", ")+".", now)
	stored.PendingEvents = append(stored.PendingEvents, "You build everything here, and the project still needs this "+
		"to run:\n  - "+strings.Join(texts, "\n  - ")+"\nDo what you can now; a step that depends on another waits for it.")
	stored.UpdatedAt = now
	if err := service.write(stored); err != nil {
		return err
	}
	service.nudgeWatcher()
	return nil
}

// gapNames say each gap in the user's words.
var gapNames = map[gapKind]string{
	gapVision:    "a vision",
	gapStack:     "its stack set up",
	gapAutopilot: "building the vision's open items",
	gapWaiting:   "starting the waiting work orders",
	gapPreview:   "a preview that runs",
}
