package conversations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
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
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].Title != "Show the log" {
		t.Fatalf("autopilot takes over %+v err=%v", apply, err)
	}
	conversation, _ = service.Get(t.Context(), "prj_test")
	if last := conversation.Messages[len(conversation.Messages)-1].Text; !strings.Contains(last, "claude-fable-5-1") {
		t.Fatalf("the premium action was not left for the user: %q", last)
	}
	if again, _ := service.autopilotActions(t.Context(), "prj_test"); len(again) != 0 {
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
	if goals, err := service.ActiveGoals(t.Context()); err != nil || goals["prj_test"].Title != "Finish P0" {
		t.Fatalf("active goals = %+v err=%v", goals, err)
	}
	conversation, err := service.StopGoal(t.Context(), "prj_test")
	if err != nil || conversation.Goal.Status != GoalStopped {
		t.Fatalf("stop = %+v err=%v", conversation.Goal, err)
	}
	if apply, _ := service.autopilotActions(t.Context(), "prj_test"); len(apply) != 0 {
		t.Fatalf("a stopped autopilot applied %+v", apply)
	}
	if goals, _ := service.ActiveGoals(t.Context()); len(goals) != 0 {
		t.Fatalf("a stopped goal is still listed: %+v", goals)
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
		apply, err := service.autopilotActions(t.Context(), "prj_test")
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

// Autopilot keeps a blocked goal item to itself until the assistant has
// answered without continuing it, or its continues run out.
func TestAutopilotHandlesABlockerUntilItGivesUp(t *testing.T) {
	service, worker := newTestService(t)
	plan := featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{{ID: "list", Title: "Show the log", FeatureID: "fea_list"}}},
	}}
	service.config.Visions = &visionStub{plan: plan}
	service.config.Features = &featuresStub{features: []feature.Feature{{ID: "fea_list", State: feature.StateImplementing}}}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	running := work{Title: "Show the log", State: feature.StateImplementing, RunID: "run_1",
		RunStatus: execution.RunStatusRunning}
	current := map[string]work{"fea_list": running}
	snapshot := func(context.Context, string) (view, error) { return view{Work: current, Plan: &plan}, nil }
	observe := func() {
		if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
			t.Fatal(err)
		}
	}
	answer := func(actions ...featureartifact.AssistantAction) {
		worker.finish(workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "On it.",
			AssistantTurn: &featureartifact.AssistantTurn{Actions: actions},
		})
		if _, err := service.Get(t.Context(), "prj_test"); err != nil {
			t.Fatal(err)
		}
	}
	block := func() time.Time {
		blockedAt := time.Now()
		current["fea_list"] = work{Title: "Show the log", State: feature.StateImplementing, RunID: "run_1",
			RunStatus: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindBlocker}
		observe()
		return blockedAt
	}
	observe()
	answer()
	if service.AutopilotHandles(t.Context(), "prj_test", "fea_other", time.Now()) {
		t.Fatal("autopilot claims a work order outside its goal")
	}

	// The assistant hears of the block, then proposes a continue autopilot applies.
	blockedAt := block()
	if !service.AutopilotHandles(t.Context(), "prj_test", "fea_list", blockedAt) {
		t.Fatal("a blocker the assistant has yet to answer was not autopilot's")
	}
	answer(featureartifact.AssistantAction{
		Type: featureartifact.AssistantContinueWorkOrder, Reason: "Blocked", WorkOrderID: "fea_list", Guidance: "Fix it.",
	})
	if !service.AutopilotHandles(t.Context(), "prj_test", "fea_list", blockedAt) {
		t.Fatal("a continue waiting to be applied was not autopilot's")
	}
	if apply, err := service.autopilotActions(t.Context(), "prj_test"); err != nil || len(apply) != 1 {
		t.Fatalf("autopilot applied %+v err=%v", apply, err)
	}

	// It blocks again and the assistant answers without a continue: the user's.
	current["fea_list"] = running
	observe()
	blockedAt = block()
	answer()
	if service.AutopilotHandles(t.Context(), "prj_test", "fea_list", blockedAt) {
		t.Fatal("a blocker the assistant answered without a continue stayed autopilot's")
	}
}

func TestTheAssistantHearsWhatBecameOfItsActions(t *testing.T) {
	text := recentActions([]Action{
		{AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantContinueWorkOrder, WorkOrderID: "fea_log"},
			Status: ActionApplied, AutoTried: true, Result: "Continued “Show the log” with guidance."},
		{AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantCreateWorkOrder, Title: "Sync"},
			Status: ActionProposed},
	})
	if !strings.Contains(text, "applied by autopilot — Continued “Show the log” with guidance.") ||
		!strings.Contains(text, `"Sync" at 00:00 UTC: waiting for the user's approval`) {
		t.Fatalf("recent actions = %q", text)
	}
}

func TestAutopilotCreatesOneWorkOrderPerItem(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 1, Items: []featureartifact.VisionItem{{ID: "goal", Title: "Calorie goal"}, {ID: "repeat", Title: "Repeat meals"}}},
	}}}
	service.config.Features = &featuresStub{}
	// A draft card proposed before autopilot, and the same item again in its
	// first turn.
	if _, err := service.Send(t.Context(), "prj_test", "Plan P1.1", "msg-1"); err != nil {
		t.Fatal(err)
	}
	create := func(title string) featureartifact.AssistantAction {
		return featureartifact.AssistantAction{Type: featureartifact.AssistantCreateWorkOrder, Reason: "Next", Title: title,
			VisionItemID: "goal", Steps: "quick"}
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Drafted.", AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{create("Goal, first")}}})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartGoal(t.Context(), "prj_test", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Go", "msg-2"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Starting.", AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{create("Goal, again")}}})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].Title != "Goal, first" {
		t.Fatalf("autopilot applied %+v err=%v", apply, err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if last := conversation.Messages[len(conversation.Messages)-1].Text; !strings.Contains(last, "already has a work order") {
		t.Fatalf("the second create was not left for the user: %q", last)
	}
}

func TestAutopilotClosesOnlyItsOwnDuplicates(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 1, Items: []featureartifact.VisionItem{{ID: "goal", Title: "Calorie goal", FeatureID: "fea_real"}}},
	}}}
	service.config.Features = &featuresStub{features: []feature.Feature{{ID: "fea_real", State: feature.StateImplementing}}}
	if _, err := service.StartGoal(t.Context(), "prj_test", 1); err != nil {
		t.Fatal(err)
	}
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	closeCard := func(id, workOrder string) Action {
		return Action{ID: id, Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCloseWorkOrder, Reason: "Duplicate", WorkOrderID: workOrder}}
	}
	stored.Actions = append(stored.Actions,
		Action{ID: "act_made_real", Status: ActionApplied, AutoTried: true, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, VisionItemID: "goal", WorkOrderID: "fea_real"}},
		Action{ID: "act_made_dup", Status: ActionApplied, AutoTried: true, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, VisionItemID: "goal", WorkOrderID: "fea_dup"}},
		closeCard("act_close_dup", "fea_dup"),
		closeCard("act_close_real", "fea_real"),
		closeCard("act_close_users", "fea_users"),
	)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].ID != "act_close_dup" {
		t.Fatalf("autopilot closes %+v err=%v", apply, err)
	}
}
