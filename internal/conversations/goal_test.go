package conversations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
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

// Autopilot recovers one work order at most maxAutoContinues times across its
// blockers; the coordinator bounds each failure itself (ADR-032).
func TestAutopilotRecoversABlockedWorkOrderBoundedTimes(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{{ID: "list", Title: "Show the log", FeatureID: "fea_list"}}},
	}}}
	service.config.Features = &featuresStub{features: []feature.Feature{{ID: "fea_list", State: feature.StateImplementing}}}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	continues := 0
	for turn := 1; turn <= maxAutoContinues+1; turn++ {
		if _, err := service.Send(t.Context(), "prj_test", "Go on", fmt.Sprintf("msg-%d", turn)); err != nil {
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
	if continues != maxAutoContinues || !strings.Contains(conversation.Messages[len(conversation.Messages)-1].Text, "yours to approve") {
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
	}, nil)
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

// modeProjectsStub is a project with an app flag and an assistant mode.
type modeProjectsStub struct {
	projectsStub
	app  bool
	mode project.AssistantMode
}

func (stub modeProjectsStub) GetByID(ctx context.Context, id string) (project.Project, error) {
	stored, err := stub.projectsStub.GetByID(ctx, id)
	stored.App, stored.AssistantMode = stub.app, stub.mode
	return stored, err
}

// Building everything, the assistant owns the whole project and takes over
// work orders the user creates too (ADR-027); only assisting leaves them to
// the user.
func TestBuildingEverythingTakesOverTheUsersWorkOrders(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Projects = modeProjectsStub{app: true, mode: project.AssistantModeBuilds}
	service.config.Features = &featuresStub{features: []feature.Feature{
		{ID: "fea_users", Title: "Prevent logging future nights", State: feature.StateDraft, CreatedBy: feature.CreatedByUser},
		{ID: "fea_ours", Title: "Weekly chart", State: feature.StateDraft, CreatedBy: feature.CreatedByAssistant},
	}}
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	card := func(id string, workOrder string) Action {
		return Action{ID: id, Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantStartWorkOrder, Reason: "The draft has been waiting", WorkOrderID: workOrder}}
	}
	stored.Actions = append(stored.Actions, card("act_start_users", "fea_users"), card("act_start_ours", "fea_ours"))
	stored.Watched = map[string]string{"fea_users": "draft", "fea_ours": "draft"}
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	if !service.AutopilotHandles(t.Context(), "prj_test", "fea_users", time.Now()) {
		t.Fatal("building everything, autopilot does not handle the user's work order")
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 2 {
		t.Fatalf("building everything applies %+v err=%v, want both starts", apply, err)
	}

	service.config.Projects = modeProjectsStub{app: true, mode: project.AssistantModeAssists}
	if service.AutopilotHandles(t.Context(), "prj_test", "fea_users", time.Now()) {
		t.Fatal("assisting, autopilot claims the user's work order")
	}
}

// How the assistant works follows the project's setting, not its app flag:
// building everything applies its actions; assisting leaves them as cards.
func TestHowTheAssistantWorksFollowsTheProjectsSetting(t *testing.T) {
	for _, test := range []struct {
		name    string
		project modeProjectsStub
		applies bool
	}{
		{"an app that assists", modeProjectsStub{app: true, mode: project.AssistantModeAssists}, false},
		{"a project that builds", modeProjectsStub{mode: project.AssistantModeBuilds}, true},
		{"an app from before the setting", modeProjectsStub{app: true}, false},
		{"a project from before the setting", modeProjectsStub{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, worker := newTestService(t)
			service.config.Projects = test.project
			service.config.Features = &featuresStub{}
			if _, err := service.Send(t.Context(), "prj_test", "Make the buttons blue", "msg-1"); err != nil {
				t.Fatal(err)
			}
			instructions := worker.puts[0].Instructions
			if builds := strings.Contains(instructions, "The user chose that you build everything here"); builds != test.applies {
				t.Fatalf("builds instructions = %t, want %t", builds, test.applies)
			}
			// Building everything, it reports in a line or two and asks only
			// what is the user's to decide.
			if short := strings.Contains(instructions, "report it in one or two short lines") &&
				strings.Contains(instructions, "never say that nothing needs the user"); short != test.applies {
				t.Fatalf("short updates = %t, want %t", short, test.applies)
			}
			if app := strings.Contains(instructions, "This project is an app the user uses"); app != test.project.app {
				t.Fatalf("app instructions = %t, want %t", app, test.project.app)
			}
			worker.finish(workerhttp.TerminalResult{
				Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Making them blue.",
				AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
					{Type: featureartifact.AssistantCreateWorkOrder, Reason: "Asked", Title: "Blue buttons", Steps: "quick"},
				}},
			})
			if _, err := service.Get(t.Context(), "prj_test"); err != nil {
				t.Fatal(err)
			}
			apply, err := service.autopilotActions(t.Context(), "prj_test")
			if err != nil || (len(apply) == 1) != test.applies {
				t.Fatalf("applied %+v err=%v, want applied %t", apply, err, test.applies)
			}
		})
	}
}

// A goal the user starts hands the assistant the work orders its items have
// then; one the user makes for an item later stays theirs.
func TestAGoalHandsOverTheWorkOrdersItStartsWith(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{
			{ID: "log", Title: "Log a night", FeatureID: "fea_handed"}, {ID: "chart", Title: "Chart the week"},
		}},
	}}}
	service.config.Features = &featuresStub{features: []feature.Feature{
		{ID: "fea_handed", Title: "Log a night", State: feature.StateDraft},
		{ID: "fea_later", Title: "Chart the week", State: feature.StateDraft},
	}}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Goal.Handed) != 1 || stored.Goal.Handed[0] != "fea_handed" {
		t.Fatalf("handed = %v", stored.Goal.Handed)
	}
	// The user then makes the second item's work order by hand.
	stored.Goal.Items[1].WorkOrderID = "fea_later"
	start := func(id, workOrder string) Action {
		return Action{ID: id, Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantStartWorkOrder, Reason: "Next", WorkOrderID: workOrder}}
	}
	stored.Actions = append(stored.Actions, start("act_handed", "fea_handed"), start("act_later", "fea_later"))
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].ID != "act_handed" {
		t.Fatalf("autopilot applies %+v err=%v", apply, err)
	}
}

type creatorMarkerStub struct{ marked map[string][]string }

func (stub *creatorMarkerStub) MarkCreatedByAssistant(_ context.Context, projectID string, ids []string) error {
	stub.marked[projectID] = append(stub.marked[projectID], ids...)
	return nil
}

// Work orders from before their creator was stored are the assistant's when
// its conversation shows it created them.
func TestTheAssistantsEarlierWorkOrdersAreMarkedAsItsOwn(t *testing.T) {
	service, _ := newTestService(t)
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string, status ActionStatus, workOrder string) Action {
		return Action{ID: id, Status: status, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Title: id, WorkOrderID: workOrder}}
	}
	stored.Actions = append(stored.Actions,
		create("act_applied", ActionApplied, "fea_made"),
		create("act_waiting", ActionProposed, ""),
		create("act_dismissed", ActionDismissed, ""),
		Action{ID: "act_start", Status: ActionApplied, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantStartWorkOrder, WorkOrderID: "fea_users"}},
	)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	marker := &creatorMarkerStub{marked: map[string][]string{}}
	if err := service.MarkCreatedWork(t.Context(), marker); err != nil {
		t.Fatal(err)
	}
	if len(marker.marked) != 1 || len(marker.marked["prj_test"]) != 1 || marker.marked["prj_test"][0] != "fea_made" {
		t.Fatalf("marked = %v", marker.marked)
	}
}

// Sending work back to Ready throws it away, so autopilot leaves it to the
// user (ADR-032).
func TestAutopilotLeavesBackToReadyToTheUser(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{{ID: "list", Title: "Show the log", FeatureID: "fea_list"}}},
	}}}
	service.config.Features = &featuresStub{features: []feature.Feature{{ID: "fea_list", State: feature.StateImplementing}}}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Go on", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Starting over.",
		AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantRecoverWorkOrder, Reason: "Unusable", WorkOrderID: "fea_list", How: "back_to_ready"},
		}},
	})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	conversation, _ := service.Get(t.Context(), "prj_test")
	if err != nil || len(apply) != 0 || !strings.Contains(conversation.Messages[len(conversation.Messages)-1].Text, "needs your approval") {
		t.Fatalf("apply = %+v err=%v last=%q", apply, err, conversation.Messages[len(conversation.Messages)-1].Text)
	}
}

// Where the assistant builds everything, it may start stuck work over itself,
// as it may close and replace it.
func TestAutopilotSendsWorkBackToReadyWhereItBuildsEverything(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Projects = modeProjectsStub{app: true, mode: project.AssistantModeBuilds}
	service.config.Features = &featuresStub{features: []feature.Feature{{ID: "fea_list", State: feature.StateImplementing}}}
	if _, err := service.Send(t.Context(), "prj_test", "Go on", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Starting over.",
		AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantRecoverWorkOrder, Reason: "Unusable", WorkOrderID: "fea_list", How: "back_to_ready"},
		}},
	})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].How != "back_to_ready" {
		t.Fatalf("apply = %+v err=%v", apply, err)
	}
}

// replacementSetup starts a goal whose one item is delivered by fea_old, and
// proposes the assistant's replacement: a fresh work order for the item, and
// closing the old one, in that order.
func replacementSetup(t *testing.T, old feature.Feature, exhausted bool) *Service {
	t.Helper()
	service, _ := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{{ID: "edit", Title: "Edit and delete single items", FeatureID: old.ID}}},
	}}}
	service.config.Features = &featuresStub{features: []feature.Feature{old}}
	service.config.RecoveryExhausted = func(_ context.Context, workOrderID string) bool {
		return exhausted && workOrderID == old.ID
	}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	stored.Actions = append(stored.Actions,
		Action{ID: "act_rebuild", Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Reason: "Replace it", Title: "Edit and delete single items (rebuild)",
			VisionItemID: "edit", Steps: "quick"}},
		Action{ID: "act_close_old", Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCloseWorkOrder, Reason: "Recovery ran out", WorkOrderID: old.ID}},
	)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	return service
}

func TestAutopilotReplacesAGoalWorkOrderWhoseRecoveryRanOut(t *testing.T) {
	old := feature.Feature{ID: "fea_old", Title: "Edit and delete single items", State: feature.StateImplementing}
	service := replacementSetup(t, old, true)
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 2 || apply[0].ID != "act_close_old" || apply[1].ID != "act_rebuild" || apply[1].HoldStart {
		t.Fatalf("autopilot applies %+v err=%v, want the close, then the replacement created and started", apply, err)
	}
	// The watcher applies them in order; the goal item follows the new one.
	if _, err := service.Resolve(t.Context(), "prj_test", "act_close_old", ActionApplied, "Closed.", Created{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", "act_rebuild", ActionApplied, "Created and started.",
		Created{WorkOrderID: "fea_new", ProjectID: "prj_test"}); err != nil {
		t.Fatal(err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if item := conversation.Goal.Items[0]; item.WorkOrderID != "fea_new" {
		t.Fatalf("the goal item follows %q, want the replacement", item.WorkOrderID)
	}
}

func TestAutopilotDoesNotCloseAHealthyGoalWorkOrder(t *testing.T) {
	old := feature.Feature{ID: "fea_old", Title: "Edit and delete single items", State: feature.StateImplementing}
	service := replacementSetup(t, old, false)
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 0 {
		t.Fatalf("autopilot applies %+v err=%v, want nothing", apply, err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if last := conversation.Messages[len(conversation.Messages)-1].Text; !strings.Contains(last, "already has a work order") {
		t.Fatalf("the replacement was not left for the user: %q", last)
	}
}

func TestAutopilotLeavesClosingTheUsersOwnWorkOrderToTheUser(t *testing.T) {
	users := feature.Feature{ID: "fea_users", Title: "Edit and delete single items", State: feature.StateImplementing,
		CreatedBy: feature.CreatedByUser}
	service := replacementSetup(t, feature.Feature{ID: "fea_gone"}, true)
	service.config.Features = &featuresStub{features: []feature.Feature{users}}
	service.config.RecoveryExhausted = func(context.Context, string) bool { return true }
	// The user created the item's work order by hand after the goal started.
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	stored.Goal.Items[0].WorkOrderID = users.ID
	stored.Actions[len(stored.Actions)-1].WorkOrderID = users.ID
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 0 {
		t.Fatalf("autopilot applies %+v err=%v, want nothing", apply, err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	var text strings.Builder
	for _, message := range conversation.Messages {
		text.WriteString(message.Text + "\n")
	}
	if !strings.Contains(text.String(), "“Edit and delete single items” is your work order") {
		t.Fatalf("closing the user's work order was not left for them: %q", text.String())
	}
}

func TestAClosedWorkOrderNoLongerClaimsItsVisionItem(t *testing.T) {
	old := feature.Feature{ID: "fea_old", Title: "Edit and delete single items", State: feature.StateCancelled}
	service := replacementSetup(t, old, false)
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	// An earlier create autopilot applied for the item, now closed.
	stored.Actions = append(stored.Actions[:len(stored.Actions)-1], Action{ID: "act_made_old", Status: ActionApplied,
		AutoTried: true, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, VisionItemID: "edit", WorkOrderID: old.ID}})
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].ID != "act_rebuild" {
		t.Fatalf("autopilot applies %+v err=%v, want the replacement", apply, err)
	}
}

// blockedReplacement is replacementSetup in a project in the given mode,
// whose old work order is blocked (or not) without its recovery running out.
func blockedReplacement(t *testing.T, old feature.Feature, mode project.AssistantMode, blocked bool) *Service {
	t.Helper()
	service := replacementSetup(t, old, false)
	service.config.Projects = modeProjectsStub{mode: mode}
	service.config.Blocked = func(_ context.Context, workOrderID string) bool {
		return blocked && workOrderID == old.ID
	}
	return service
}

// Building everything, a blocked goal work order is replaced in one move:
// the close and the replacement both apply, and the replacement starts.
func TestAutopilotReplacesABlockedGoalWorkOrderWhereItBuildsEverything(t *testing.T) {
	old := feature.Feature{ID: "fea_old", Title: "Edit and delete single items", State: feature.StateImplementing,
		CreatedBy: feature.CreatedByAssistant}
	service := blockedReplacement(t, old, project.AssistantModeBuilds, true)
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 2 || apply[0].ID != "act_close_old" || apply[1].ID != "act_rebuild" || apply[1].HoldStart {
		t.Fatalf("autopilot applies %+v err=%v, want the close, then the replacement created and started", apply, err)
	}
}

func TestAutopilotDoesNotCloseAHealthyGoalWorkOrderWhereItBuildsEverything(t *testing.T) {
	old := feature.Feature{ID: "fea_old", Title: "Edit and delete single items", State: feature.StateImplementing,
		CreatedBy: feature.CreatedByAssistant}
	service := blockedReplacement(t, old, project.AssistantModeBuilds, false)
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 0 {
		t.Fatalf("autopilot applies %+v err=%v, want nothing", apply, err)
	}
}

func TestAutopilotLeavesReplacingTheUsersBlockedWorkOrderToTheUser(t *testing.T) {
	old := feature.Feature{ID: "fea_old", Title: "Edit and delete single items", State: feature.StateImplementing,
		CreatedBy: feature.CreatedByUser}
	service := blockedReplacement(t, old, project.AssistantModeBuilds, true)
	// The user created it after the goal started, so the goal did not hand
	// it over.
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	stored.Goal.Handed = nil
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 0 {
		t.Fatalf("autopilot applies %+v err=%v, want nothing", apply, err)
	}
}

// Assisting, a blocked work order's close and replacement wait for the user.
func TestAutopilotLeavesABlockedReplacementToTheUserWhereItAssists(t *testing.T) {
	old := feature.Feature{ID: "fea_old", Title: "Edit and delete single items", State: feature.StateImplementing,
		CreatedBy: feature.CreatedByAssistant}
	service := blockedReplacement(t, old, project.AssistantModeAssists, true)
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 0 {
		t.Fatalf("autopilot applies %+v err=%v, want nothing", apply, err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	for _, action := range conversation.Actions {
		if action.Status != ActionProposed {
			t.Fatalf("%s is %s, want it waiting for the user", action.ID, action.Status)
		}
	}
}

// Building everything, autopilot starts every work order that is ready at
// once, up to the safety cap; the rest wait for a slot, with a plain note, and
// start when running work reaches the merge queue or ends (ADR-036).
func TestAutopilotStartsWorkSideBySideUpToTheSafetyCap(t *testing.T) {
	service, _ := newTestService(t)
	// An older saved ceiling no longer lowers the cap.
	if err := writeJSON(service.settingsPath(), Settings{Agent: "claude", Model: "claude-sonnet-5-5", MaxRunning: 4}); err != nil {
		t.Fatal(err)
	}
	if settings, err := service.Settings(t.Context()); err != nil || settings.MaxRunning != 0 {
		t.Fatalf("settings = %+v err=%v, want the old max_running dropped", settings, err)
	}
	service.config.Projects = gapProjects{mode: project.AssistantModeBuilds}
	features := &featuresStub{}
	service.config.Features = features
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= SafetyCap+2; index++ {
		id := fmt.Sprintf("fea_%02d", index)
		features.features = append(features.features, feature.Feature{ID: id, Title: "Screen " + id, State: feature.StateReady})
		stored.Actions = append(stored.Actions, Action{ID: "act_" + id, Status: ActionProposed,
			AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantStartWorkOrder, WorkOrderID: id}})
	}
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != SafetyCap {
		t.Fatalf("applied %d err=%v, want %d at once", len(apply), err, SafetyCap)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if last := conversation.Messages[len(conversation.Messages)-1]; !strings.Contains(last.Text,
		"16 work orders already run here, the most at once") {
		t.Fatalf("last note = %q", last.Text)
	}
	for index := range SafetyCap {
		features.features[index].State = feature.StateImplementing
	}
	if apply, _ := service.autopilotActions(t.Context(), "prj_test"); len(apply) != 0 {
		t.Fatalf("started beyond the safety cap: %+v", apply)
	}
	// One reaches the merge queue: its slot frees.
	features.features[0].State = feature.StateReadyToMerge
	apply, err = service.autopilotActions(t.Context(), "prj_test")
	if next := fmt.Sprintf("fea_%02d", SafetyCap+1); err != nil || len(apply) != 1 || apply[0].WorkOrderID != next {
		t.Fatalf("applied %+v err=%v, want %s in the freed slot", apply, err, next)
	}
}

// An action autopilot leaves for the user says so on itself, for the app to
// show, and stops saying so once it is applied.
func TestAnActionLeftForTheUserIsMarkedUntilApplied(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{{ID: "list", Title: "Show the log", FeatureID: "fea_list"}}},
	}}}
	service.config.Features = &featuresStub{features: []feature.Feature{{ID: "fea_list", State: feature.StateImplementing}}}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Go on", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Starting over.",
		AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantRecoverWorkOrder, Reason: "Unusable", WorkOrderID: "fea_list", How: "back_to_ready"},
		}},
	})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.autopilotActions(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	left := conversation.Actions[len(conversation.Actions)-1]
	if !left.LeftForUser {
		t.Fatalf("left = %+v", left)
	}
	applied, err := service.Resolve(t.Context(), "prj_test", left.ID, ActionApplied, "Sent back to Ready.", Created{})
	if err != nil {
		t.Fatal(err)
	}
	if action := applied.Actions[len(applied.Actions)-1]; action.LeftForUser || action.Status != ActionApplied {
		t.Fatalf("applied = %+v", action)
	}
}
