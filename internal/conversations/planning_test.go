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
	for _, want := range []string{"Planning mode.", "every reply carries it in plan", "propose build_plan", "ask whatever you need",
		"This is a checklist, not an agenda", "plan.covered", "purpose (what it is for and who uses it)", "Cards are a tool, not a questionnaire",
		"the card always offers the user's own answer", "Opening: Commitarium's note as planning starts", `"Suggest some ideas" (suggested)`,
		"How apps run in Commitarium", "Where the app runs decides how it gets settings and secrets", "Never ask what the user, the vision, or earlier rounds already settled",
		"don't push before then", "the plan being written in the planning workspace"} {
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
		Planning: &vision.Planning{Status: vision.PlanningOpen, Decisions: []string{"Sync nightly"}, Covered: []string{"purpose", "data"},
			Research: []vision.ResearchNote{{Title: "Shopify", Sources: []string{"https://shopify.dev/"}, AddedAt: time.Now()}}}})
	text := builder.String()
	for _, want := range []string{"Vision (being planned)", "Goal: Keep stock in step.", "- Sync nightly",
		"Covered: purpose, data; still to cover: features, look, details, priorities, stack",
		"Open questions: none yet", "Shopify (https://shopify.dev/)", "Stack: not decided yet", "node-lts", "P0.1 Read stock"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the plan lacks %q:\n%s", want, text)
		}
	}
	builder.Reset()
	writePlanning(&builder, vision.Vision{Planning: &vision.Planning{Status: vision.PlanningPaused}})
	if !strings.Contains(builder.String(), "Vision (planning stopped)") {
		t.Fatalf("paused planning:\n%s", builder.String())
	}
}

// chatVision is a vision being planned in one chat, or in none.
type chatVision struct {
	chat  string
	round bool
}

func (stub *chatVision) Get(_ context.Context, projectID string) (vision.Vision, error) {
	return vision.Vision{ProjectID: projectID, Plan: featureartifact.VisionPlan{Statement: "Keep stock in step."},
		Planning: &vision.Planning{Status: vision.PlanningOpen, Chat: stub.chat, Round: stub.round, RoundPriority: 2}}, nil
}

// Only the chat the project is planned in plans (ADR-039); the others hear
// where it is. Planning with no chat, as stored before, is every chat's.
func TestPlanningBelongsToItsChat(t *testing.T) {
	service, worker := newChatsService(t)
	ctx := t.Context()
	planning, err := service.CreateChat(ctx, "prj_test", "Plan the shop")
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateChat(ctx, "prj_test", "Sign-in bug")
	if err != nil {
		t.Fatal(err)
	}
	visions := &chatVision{chat: planning.Chat}
	service.config.Visions = visions
	send := func(chat, key string) workerhttp.PutAttemptRequest {
		t.Helper()
		if _, err := service.Send(InChat(ctx, chat), "prj_test", "Hello", key); err != nil {
			t.Fatal(err)
		}
		worker.mu.Lock()
		defer worker.mu.Unlock()
		return worker.puts[len(worker.puts)-1]
	}

	put := send(planning.Chat, "msg-1")
	if put.OutputContract != workerhttp.OutputContractAssistantPlanningTurn || !strings.Contains(put.Instructions, "Planning mode.") ||
		strings.Contains(put.Instructions, "being planned with the user in their chat") {
		t.Fatalf("the planning chat's turn: %s\n%s", put.OutputContract, put.Instructions)
	}
	put = send(other.Chat, "msg-2")
	if put.OutputContract != workerhttp.OutputContractAssistantTurn || strings.Contains(put.Instructions, "Planning mode") ||
		!strings.Contains(put.Instructions, "being planned with the user in their chat “Plan the shop”") ||
		!strings.Contains(put.Instructions, "until the user presses Build it") {
		t.Fatalf("another chat's turn: %s\n%s", put.OutputContract, put.Instructions)
	}

	// Planning stored before it had a chat, or in a chat that is gone,
	// plans in every chat.
	for _, chat := range []string{"", "chat_ffffffffffffffff"} {
		visions.chat = chat
		third, err := service.CreateChat(ctx, "prj_test", "")
		if err != nil {
			t.Fatal(err)
		}
		if put := send(third.Chat, "msg-"+third.Chat); put.OutputContract != workerhttp.OutputContractAssistantPlanningTurn {
			t.Fatalf("planning in %q: contract = %s", chat, put.OutputContract)
		}
	}

	// A next round has its own agenda.
	visions.chat, visions.round = "", true
	fourth, err := service.CreateChat(ctx, "prj_test", "")
	if err != nil {
		t.Fatal(err)
	}
	put = send(fourth.Chat, "msg-round")
	for _, want := range []string{"Planning mode, next round.", "round (what this round is for: the change",
		"from P2 (what the round needs first) to P3", "only where the round changes them", "This is a checklist, not an agenda",
		"only when the round's idea itself brings them in", "Name the app in what you ask",
		"Opening: Commitarium's note as planning starts", "three or four concrete directions"} {
		if !strings.Contains(put.Instructions, want) {
			t.Fatalf("the round lacks %q:\n%s", want, put.Instructions)
		}
	}
	if strings.Contains(put.Instructions, "purpose (what it is for") || strings.Contains(roundInstructions(2), "for whom") {
		t.Fatal("a round has the new project's agenda")
	}
}

// In Developer mode a project chat that is not planning may suggest plan
// mode with start_planning; elsewhere the suggestion is neither described
// nor kept.
func TestPlanModeIsSuggestedOnlyOutsidePlanning(t *testing.T) {
	service, worker := newTestService(t)
	ctx := t.Context()
	turn := func(key string) (string, []Action) {
		t.Helper()
		if _, err := service.Send(ctx, "prj_test", "Build a team dashboard with Slack alerts", key); err != nil {
			t.Fatal(err)
		}
		instructions := worker.puts[len(worker.puts)-1].Instructions
		worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
			Summary: "Let's plan it.", AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
				{Type: featureartifact.AssistantStartPlanning, Reason: "Several screens and an outside service"},
			}}})
		conversation, err := service.Get(ctx, "prj_test")
		if err != nil {
			t.Fatal(err)
		}
		var suggested []Action
		for _, action := range conversation.Actions {
			if action.Type == featureartifact.AssistantStartPlanning && action.MessageID == conversation.Messages[len(conversation.Messages)-1].ID {
				suggested = append(suggested, action)
			}
		}
		return instructions, suggested
	}

	if instructions, suggested := turn("msg-1"); strings.Contains(instructions, "propose start_planning") || len(suggested) != 0 {
		t.Fatalf("without Developer mode: suggested %v", suggested)
	}
	if err := service.SetDeveloperMode(ctx, "prj_test", true); err != nil {
		t.Fatal(err)
	}
	instructions, suggested := turn("msg-2")
	for _, want := range []string{"propose start_planning", "several screens or parts, an outside service",
		"Build small, clear requests as usual", "do not propose it again for that request"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("the turn lacks %q", want)
		}
	}
	if len(suggested) != 1 || suggested[0].Status != ActionProposed {
		t.Fatalf("suggested = %+v", suggested)
	}
	service.config.Visions = &chatVision{}
	if instructions, suggested := turn("msg-3"); strings.Contains(instructions, "propose start_planning") || len(suggested) != 0 {
		t.Fatalf("while this chat plans: suggested %v", suggested)
	}
}

// /plan and a message: planning's note waits in the chat without starting a
// turn, and the user's message that follows carries it.
func TestAToldNoteRidesAlongWithTheNextMessage(t *testing.T) {
	service, worker := newChatsService(t)
	ctx := t.Context()
	chat, err := service.CreateChat(ctx, "prj_test", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.TellChat(ctx, "prj_test", chat.Chat, "Planning the project with the assistant.",
		"The user switched plan mode on."); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.nudge:
		t.Fatal("telling a chat looked for a turn at once")
	default:
	}
	if _, err := service.Send(InChat(ctx, chat.Chat), "prj_test", "A weekly digest", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if len(worker.puts) != 1 || !strings.Contains(worker.puts[0].Instructions, "The user switched plan mode on.") ||
		!strings.Contains(worker.puts[0].Instructions, "A weekly digest") {
		t.Fatalf("puts = %d", len(worker.puts))
	}
}

// Planning's one firm rule is in both briefs, and the state shows the
// written plan once drafted (PLAN_MODE_PLAN.md PM6).
func TestPlanningBriefsAskForTheWrittenPlan(t *testing.T) {
	for _, brief := range []string{planningInstructions, roundInstructions(2)} {
		if !strings.Contains(brief, "Planning ends with a written plan in commit-sized slices") ||
			!strings.Contains(brief, "build_plan before a draft is refused") {
			t.Fatalf("brief lacks the written plan: %s", brief)
		}
	}
	stored := vision.Vision{Plan: featureartifact.VisionPlan{Statement: "Goal"}, Planning: &vision.Planning{Status: vision.PlanningOpen}}
	var talking strings.Builder
	writePlanning(&talking, stored)
	if !strings.Contains(talking.String(), "Written plan: no draft yet") {
		t.Fatalf("talking = %s", talking.String())
	}
	stored.Planning.Phase, stored.Planning.Document, stored.Planning.DocumentPath = vision.PhaseDrafted, "# Plan\n- [ ] 1. Lists", "docs/plans/plan.md"
	var drafted strings.Builder
	writePlanning(&drafted, stored)
	if !strings.Contains(drafted.String(), "Written plan (drafted, at docs/plans/plan.md") || !strings.Contains(drafted.String(), "- [ ] 1. Lists") {
		t.Fatalf("drafted = %s", drafted.String())
	}
}
