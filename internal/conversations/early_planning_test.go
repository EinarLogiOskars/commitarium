package conversations

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// filtersAfterFoundation is a project that builds everything, with a
// foundation and filters whose start waits on it.
func filtersAfterFoundation(t *testing.T, steps string) (*Service, *featuresStub) {
	t.Helper()
	service, _ := newTestService(t)
	service.config.Projects = gapProjects{mode: project.AssistantModeBuilds}
	features := &featuresStub{features: []feature.Feature{
		{ID: "fea_base", Title: "Lay the foundation", State: feature.StatePlanning},
		{ID: "fea_filters", Title: "Add filters", State: feature.StateReady},
	}}
	service.config.Features = features
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	stored.Actions = append(stored.Actions, Action{ID: "act_filters", Status: ActionProposed,
		AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantStartWorkOrder,
			WorkOrderID: "fea_filters", After: "fea_base", Steps: steps}})
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	return service, features
}

// A work order that waits on another starts to plan once that one is
// implementing, takes no slot while it plans, and is held before it
// implements until that one has merged (ADR-037).
func TestAQueuedWorkOrderPlansEarlyAndHoldsBeforeImplementing(t *testing.T) {
	service, features := filtersAfterFoundation(t, "reviewed_plan")
	if apply, _ := service.autopilotActions(t.Context(), "prj_test"); len(apply) != 0 {
		t.Fatalf("started while the foundation still plans: %+v", apply)
	}
	features.features[0].State = feature.StateImplementing
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].ID != "act_filters" || !apply[0].PlansEarly {
		t.Fatalf("applied %+v err=%v, want the filters to plan early", apply, err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if last := conversation.Messages[len(conversation.Messages)-1]; !strings.Contains(last.Text,
		"to plan now: it implements once “Lay the foundation” has merged") {
		t.Fatalf("last note = %q", last.Text)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", "act_filters", ActionApplied, "Started", Created{}); err != nil {
		t.Fatal(err)
	}
	features.features[1].State = feature.StatePlanning

	reason, held := service.ImplementationHold(t.Context(), "prj_test", "fea_filters")
	if !held || reason != "Planned; implements once “Lay the foundation” has merged." {
		t.Fatalf("hold = %q, %v", reason, held)
	}
	if running := service.running(t.Context(), "prj_test", map[string]bool{"fea_filters": true}); running != 1 {
		t.Fatalf("running = %d, want only the foundation: planning early takes no slot", running)
	}
	if _, held := service.ImplementationHold(t.Context(), "prj_test", "fea_base"); held {
		t.Fatal("work that did not plan early is held")
	}

	features.features[0].State = feature.StateCompleted
	if reason, held := service.ImplementationHold(t.Context(), "prj_test", "fea_filters"); held {
		t.Fatalf("still held after the foundation merged: %q", reason)
	}

	// Implementing takes a slot, so with every slot taken it waits for one.
	for index := range DefaultMaxRunning {
		features.features = append(features.features, feature.Feature{
			ID: fmt.Sprintf("fea_%d", index), Title: "Other", State: feature.StateImplementing})
	}
	reason, held = service.ImplementationHold(t.Context(), "prj_test", "fea_filters")
	if !held || !strings.HasPrefix(reason, "Planned; implements when a slot frees: 4 work orders") {
		t.Fatalf("hold with every slot taken = %q, %v", reason, held)
	}
}

// Quick work has no plan to make early: it starts once the work it follows
// has merged, as before.
func TestQuickWorkDoesNotPlanEarly(t *testing.T) {
	service, features := filtersAfterFoundation(t, "quick")
	features.features[0].State = feature.StateImplementing
	if apply, _ := service.autopilotActions(t.Context(), "prj_test"); len(apply) != 0 {
		t.Fatalf("quick work started before the foundation merged: %+v", apply)
	}
	features.features[0].State = feature.StateCompleted
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].PlansEarly {
		t.Fatalf("applied %+v err=%v, want an ordinary start", apply, err)
	}
}

type releases struct{ runs []string }

func (released *releases) release(_ context.Context, runID string) error {
	released.runs = append(released.runs, runID)
	return nil
}

// The watcher releases a held run once what held it changed, and leaves it
// while it still holds; the hold is the run's own, so a watcher that starts
// after a restart picks it up the same way.
func TestTheWatcherReleasesHeldWorkOnceItsDependencyMerged(t *testing.T) {
	service, features := filtersAfterFoundation(t, "planned")
	features.features[0].State = feature.StateImplementing
	if apply, _ := service.autopilotActions(t.Context(), "prj_test"); len(apply) != 1 {
		t.Fatalf("applied %+v", apply)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", "act_filters", ActionApplied, "Started", Created{}); err != nil {
		t.Fatal(err)
	}
	features.features[1].State = feature.StatePlanning
	held := execution.Run{ID: "run_filters", FeatureID: "fea_filters", Status: execution.RunStatusWaitingForUser,
		WaitKind: execution.RunWaitKindPhaseCheckpoint, Reason: execution.ImplementationHoldReason("Lay the foundation")}
	released := &releases{}
	watcher := Watcher{Service: service, Features: features,
		Runs:                  runsStub{runs: map[string][]execution.Run{"fea_filters": {held}}},
		ReleaseImplementation: released.release}

	watcher.releaseHeld(t.Context(), "prj_test")
	if len(released.runs) != 0 {
		t.Fatalf("released while the foundation is unmerged: %v", released.runs)
	}
	// A restart: a new service and watcher over the same stored records.
	restarted, err := NewService(service.config)
	if err != nil {
		t.Fatal(err)
	}
	features.features[0].State = feature.StateCompleted
	watcher.Service = restarted
	watcher.releaseHeld(t.Context(), "prj_test")
	if !slices.Equal(released.runs, []string{"run_filters"}) {
		t.Fatalf("released %v, want the filters once the foundation merged", released.runs)
	}
}

// A held run shows in the conversation in plain words, without waking the
// assistant: it implements on its own.
func TestHeldWorkIsNotedWithoutWakingTheAssistant(t *testing.T) {
	item := work{Title: "Add filters", State: feature.StatePlanning, RunID: "run_filters",
		RunStatus: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindPhaseCheckpoint,
		Reason: execution.ImplementationHoldReason("Lay the foundation")}
	event, ok := item.event()
	if !ok || event.Wake || event.Text != "\"Add filters\" is planned and implements once “Lay the foundation” has merged." {
		t.Fatalf("event = %+v, %v", event, ok)
	}
}

// Build plans lay a shared foundation first and keep features independent
// of one another, wherever a plan is written (ADR-037).
func TestBuildPlansPutTheFoundationFirst(t *testing.T) {
	for _, phrase := range []string{
		"Make the first item the shared foundation",
		"data types and API shapes",
		"without the features themselves",
		"so none of them waits on another",
	} {
		if !strings.Contains(featureartifact.WidePlan, phrase) {
			t.Fatalf("the plan rule lacks %q", phrase)
		}
	}
	if !strings.Contains(planningInstructions, featureartifact.WidePlan) {
		t.Fatal("the planning workspace does not plan wide")
	}
	goal := goalInstructions(&Goal{Status: GoalActive, Title: "Finish P0"})
	if !strings.Contains(goal, "never on a sibling that only adds to the same page") ||
		!strings.Contains(goal, "plans early") {
		t.Fatalf("the goal's instructions do not say how to wait: %q", goal)
	}
	if !strings.Contains(buildsInstructions, "quick for small, low-risk items (keyboard shortcuts, empty states, copy)") {
		t.Fatal("building everything does not choose steps by risk")
	}
}
