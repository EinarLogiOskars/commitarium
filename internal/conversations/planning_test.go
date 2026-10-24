package conversations

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// plannedVision is a vision being planned that records the research noted
// on it.
type plannedVision struct {
	research []string
}

func (stub *plannedVision) Get(_ context.Context, projectID string) (vision.Vision, error) {
	return vision.Vision{ProjectID: projectID, Plan: featureartifact.VisionPlan{Statement: "Keep stock in step."},
		Planning: &vision.Planning{Status: vision.PlanningOpen, Decisions: []string{"Sync nightly"},
			Questions: []string{"Which shop leads?"}}}, nil
}

func (stub *plannedVision) AddResearch(_ context.Context, projectID, title, findings string) error {
	stub.research = append(stub.research, projectID+": "+title+": "+findings)
	return nil
}

// While a project's vision is planned, each turn says how to plan it,
// update_plan is sent without a card, and research is noted on the plan.
func TestAPlannedProjectIsPlannedWithTheUser(t *testing.T) {
	service, worker := newTestService(t)
	visions := &plannedVision{}
	service.config.Visions = visions
	// Opened on the vision, it plans rather than has the vision written.
	if _, err := service.SetFocus(t.Context(), "prj_test", &Focus{Kind: FocusVision}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Let's plan it", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if worker.puts[0].OutputContract != workerhttp.OutputContractAssistantPlanningTurn {
		t.Fatalf("contract = %q, want the planning turn's", worker.puts[0].OutputContract)
	}
	for _, want := range []string{"Planning mode.", "Every reply carries the plan", "propose build_plan", "Every third round, step back",
		"the plan being written in the planning workspace"} {
		if !strings.Contains(worker.puts[0].Instructions, want) {
			t.Fatalf("the planning turn lacks %q", want)
		}
	}
	if strings.Contains(worker.puts[0].Instructions, "Then propose write_vision") {
		t.Fatal("a planned vision is to be written by the vision writer")
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Noted.", AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantUpdatePlan, Reason: "Settled", Decisions: []string{"Sync nightly"}},
			{Type: featureartifact.AssistantBuildPlan, Reason: "Not yet"},
		}}})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	actions, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(actions) != 1 || actions[0].Type != featureartifact.AssistantUpdatePlan {
		t.Fatalf("sent at once = %+v, %v", actions, err)
	}

	helper := Helper{ID: "rsc_1", Kind: HelperResearch, Title: "Shopify's stock API", Agent: "codex"}
	startedHelper(t, service, helper)
	if err := service.helpersChanged(t.Context(), "prj_test", map[string]HelperState{
		"rsc_1": {Status: HelperProposes, Turn: 1, Text: "Per location.\n\nSources:\n- https://shopify.dev/"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(visions.research) != 1 || !strings.Contains(visions.research[0], "prj_test: Shopify's stock API: Per location.") {
		t.Fatalf("research = %v", visions.research)
	}
}

// Nothing is missing from a project being planned: the gap check waits for
// the user to build it.
func TestAPlannedProjectHasNoGaps(t *testing.T) {
	stored := record{Conversation: Conversation{Scope: "prj_test"}}
	if gaps := findGaps(projectState{Planning: true}, stored); len(gaps) != 0 {
		t.Fatalf("gaps = %+v", gaps)
	}
	if gaps := findGaps(projectState{}, stored); len(gaps) == 0 {
		t.Fatal("a project without a vision has no gaps")
	}
}

// The state shows a vision being planned as its plan.
func TestThePlanBeingWrittenIsDescribed(t *testing.T) {
	var builder strings.Builder
	writePlanning(&builder, vision.Vision{Plan: featureartifact.VisionPlan{Statement: "Keep stock in step.",
		Priorities: []featureartifact.VisionPriority{{Level: 0, Items: []featureartifact.VisionItem{{ID: "read", Title: "Read stock"}}}}},
		Planning: &vision.Planning{Status: vision.PlanningOpen, Decisions: []string{"Sync nightly"},
			Research: []vision.ResearchNote{{Title: "Shopify", Sources: []string{"https://shopify.dev/"}, AddedAt: time.Now()}}}})
	text := builder.String()
	for _, want := range []string{"Vision (being planned)", "Goal: Keep stock in step.", "- Sync nightly",
		"Open questions: none yet", "Shopify (https://shopify.dev/)", "Stack: not decided yet", "node-lts", "P0.1 Read stock"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the plan lacks %q:\n%s", want, text)
		}
	}
}
