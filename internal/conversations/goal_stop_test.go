package conversations

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestStoppingAGoalLeavesItsPendingActionsAsCards(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Projects = gapProjects{mode: project.AssistantModeAssists}
	service.config.Visions = &visionStub{plan: featureartifact.VisionPlan{Priorities: []featureartifact.VisionPriority{
		{Level: 0, Items: []featureartifact.VisionItem{{ID: "list", Title: "Show the log", FeatureID: "fea_list"}}},
	}}}
	service.config.Features = &featuresStub{features: []feature.Feature{{ID: "fea_list", State: feature.StateDraft, CreatedBy: feature.CreatedByAssistant}}}
	if _, err := service.StartGoal(t.Context(), "prj_test", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "go", "m1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Starting.",
		AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantStartWorkOrder, Reason: "Go", WorkOrderID: "fea_list", LeadAgent: "a", LeadModel: "m"},
		}},
	})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StopGoal(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	if len(apply) != 0 {
		t.Fatalf("stopped goal under Ask first still applied %d actions", len(apply))
	}
}

func TestAContinueSheAsksAboutIsTheUsersNotAutopilots(t *testing.T) {
	for _, mode := range []project.AssistantMode{project.AssistantModeBuilds, project.AssistantModeAssists} {
		for _, take := range []bool{false, true} {
			service, worker := newTestService(t)
			service.config.Projects = gapProjects{mode: mode}
			service.config.Features = &featuresStub{features: []feature.Feature{
				{ID: "fea_list", State: feature.StateImplementing, CreatedBy: feature.CreatedByAssistant},
			}}
			current := map[string]work{"fea_list": {Title: "Show the log", State: feature.StateImplementing,
				RunID: "run_1", RunStatus: execution.RunStatusRunning}}
			snapshot := func(context.Context, string) (view, error) { return view{Work: current}, nil }
			if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
				t.Fatal(err)
			}
			blockedAt := time.Now()
			current["fea_list"] = work{Title: "Show the log", State: feature.StateImplementing, RunID: "run_1",
				RunStatus: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindBlocker}
			if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
				t.Fatal(err)
			}
			{
				if _, err := service.Send(t.Context(), "prj_test", "what now?", fmt.Sprintf("m_%v", take)); err != nil {
					t.Fatal(err)
				}
				worker.finish(workerhttp.TerminalResult{
					Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Here.",
					AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{{
						Type: featureartifact.AssistantContinueWorkOrder, Reason: "Blocked", WorkOrderID: "fea_list",
						Guidance: "Fix it.", Take: take,
					}}},
				})
				if _, err := service.Get(t.Context(), "prj_test"); err != nil {
					t.Fatal(err)
				}
				if got := service.AutopilotHandles(t.Context(), "prj_test", "fea_list", blockedAt); got != take {
					t.Fatalf("%s, take %v: autopilot handles = %v", mode, take, got)
				}
			}
		}
	}
}
