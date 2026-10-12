package conversations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type visionStub struct{ plan featureartifact.VisionPlan }

func (stub *visionStub) Get(context.Context, string) (vision.Vision, error) {
	return vision.Vision{Plan: stub.plan}, nil
}

type featuresStub struct{ features []feature.Feature }

func (stub *featuresStub) List(context.Context, string) ([]feature.Feature, error) {
	return stub.features, nil
}

func TestAutopilotDrivesAGoalAndEndsWhenItIsDone(t *testing.T) {
	plan := featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{
			{ID: "log", Title: "Log a meal", FeatureID: "fea_log"},
			{ID: "list", Title: "Show the log"},
			{ID: "edit", Title: "Correct entries", FeatureID: "fea_edit"},
			{ID: "old", Title: "Already there", Done: true},
		}},
		{Level: 1, Items: []featureartifact.VisionItem{{ID: "goal", Title: "Calorie goal"}}},
	}}
	visions := &visionStub{plan: plan}
	features := &featuresStub{features: []feature.Feature{
		{ID: "fea_log", State: feature.StateCompleted}, {ID: "fea_edit", State: feature.StateDraft},
	}}
	service, worker := newTestService(t)
	service.config.Visions, service.config.Features = visions, features

	if _, err := service.StartGoal(t.Context(), "prj_test", 5); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a goal with nothing to do started: %v", err)
	}
	conversation, err := service.StartGoal(t.Context(), "prj_test", 0)
	if err != nil {
		t.Fatal(err)
	}
	goal := conversation.Goal
	if goal == nil || goal.Status != GoalActive || len(goal.Items) != 2 ||
		goal.Items[0].Label != "P0.2" || goal.Items[1].WorkOrderID != "fea_edit" {
		t.Fatalf("goal = %+v", goal)
	}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); !errors.Is(err, ErrGoalActive) {
		t.Fatalf("a second goal started: %v", err)
	}

	// The first look wakes the assistant to start on the goal.
	current := map[string]work{"fea_log": {State: feature.StateCompleted}, "fea_edit": {State: feature.StateDraft}}
	snapshot := func(context.Context, string) (view, error) { return view{Work: current, Plan: &visions.plan}, nil }
	if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
		t.Fatal(err)
	}
	if len(worker.puts) != 1 || !strings.Contains(worker.puts[0].Instructions, "Autopilot is on") ||
		!strings.Contains(worker.puts[0].Instructions, "P0.2 Show the log (item list): open") {
		t.Fatalf("the goal did not start a turn: %+v", worker.puts)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Starting P0.",
		AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantCreateWorkOrder, Reason: "Next", Title: "Show the log", VisionItemID: "list", Steps: "quick"},
			{Type: featureartifact.AssistantStartWorkOrder, Reason: "Next", WorkOrderID: "fea_edit", LeadModel: "claude-fable-5-1"},
			{Type: featureartifact.AssistantCreateWorkOrder, Reason: "Aside", Title: "Dark mode", Steps: "quick"},
		}},
	})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions("prj_test")
	if err != nil || len(apply) != 1 || apply[0].Title != "Show the log" {
		t.Fatalf("autopilot takes over %+v err=%v", apply, err)
	}
	conversation, _ = service.Get(t.Context(), "prj_test")
	if last := conversation.Messages[len(conversation.Messages)-1].Text; !strings.Contains(last, "claude-fable-5-1") {
		t.Fatalf("the premium action was not left for the user: %q", last)
	}
	if again, _ := service.autopilotActions("prj_test"); len(again) != 0 {
		t.Fatalf("autopilot took an action over twice: %+v", again)
	}

	// Both items merge: the goal ends and the assistant reports.
	visions.plan.Priorities[0].Items[1].FeatureID = "fea_list"
	current["fea_list"] = work{State: feature.StateCompleted}
	current["fea_edit"] = work{State: feature.StateCompleted}
	if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
		t.Fatal(err)
	}
	conversation, _ = service.Get(t.Context(), "prj_test")
	if conversation.Goal.Status != GoalDone || conversation.Goal.EndedAt == nil || len(worker.puts) != 2 ||
		!strings.Contains(worker.puts[1].Instructions, "Autopilot finished: Finish P0 is done.") {
		t.Fatalf("the goal did not end: %+v puts=%d", conversation.Goal, len(worker.puts))
	}
	if strings.Contains(worker.puts[1].Instructions, "Autopilot is on.") {
		t.Fatal("the report turn still says autopilot is on")
	}
}

func TestStoppingAutopilotLeavesActionsForTheUser(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{{ID: "list", Title: "Show the log"}}},
	}}}
	service.config.Features = &featuresStub{}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	conversation, err := service.StopGoal(t.Context(), "prj_test")
	if err != nil || conversation.Goal.Status != GoalStopped {
		t.Fatalf("stop = %+v err=%v", conversation.Goal, err)
	}
	if apply, _ := service.autopilotActions("prj_test"); len(apply) != 0 {
		t.Fatalf("a stopped autopilot applied %+v", apply)
	}
}

func TestAutopilotContinuesABlockedWorkOrderAtMostTwice(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{{ID: "list", Title: "Show the log", FeatureID: "fea_list"}}},
	}}}
	service.config.Features = &featuresStub{features: []feature.Feature{{ID: "fea_list", State: feature.StateImplementing}}}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	continues := 0
	for turn := 1; turn <= 3; turn++ {
		if _, err := service.Send(t.Context(), "prj_test", "Go on", "msg-"+string(rune('0'+turn))); err != nil {
			t.Fatal(err)
		}
		worker.finish(workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Retrying.",
			AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
				{Type: featureartifact.AssistantContinueWorkOrder, Reason: "Blocked", WorkOrderID: "fea_list", Guidance: "Fix it."},
			}},
		})
		if _, err := service.Get(t.Context(), "prj_test"); err != nil {
			t.Fatal(err)
		}
		apply, err := service.autopilotActions("prj_test")
		if err != nil {
			t.Fatal(err)
		}
		continues += len(apply)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if continues != 2 || !strings.Contains(conversation.Messages[len(conversation.Messages)-1].Text, "yours to approve") {
		t.Fatalf("continues = %d, last = %q", continues, conversation.Messages[len(conversation.Messages)-1].Text)
	}
}
