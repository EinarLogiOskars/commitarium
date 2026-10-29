package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// planningVisions records how a vision is planned.
type planningVisions struct {
	VisionService
	goals     []string
	chats     []string
	updates   []vision.PlanningUpdate
	finished  int
	paused    int
	discarded int
	// described are the descriptions a new app's vision is written from.
	described []string
	// fail fails writing or planning a vision while set.
	fail error
}

func (stub *planningVisions) Start(_ context.Context, _, _, _ string, _ vision.Mode, message, _ string) (vision.Session, bool, error) {
	if stub.fail != nil {
		return vision.Session{}, false, stub.fail
	}
	stub.described = append(stub.described, message)
	return vision.Session{ID: "vis_1"}, true, nil
}

func (stub *planningVisions) Get(context.Context, string) (vision.Vision, error) {
	return vision.Vision{}, vision.ErrNotFound
}

func (stub *planningVisions) StartPlanning(_ context.Context, projectID, goal, chat string) (vision.Vision, bool, error) {
	if stub.fail != nil {
		return vision.Vision{}, false, stub.fail
	}
	stub.goals = append(stub.goals, goal)
	stub.chats = append(stub.chats, chat)
	return vision.Vision{ProjectID: projectID, Planning: &vision.Planning{Status: vision.PlanningOpen}}, len(stub.goals) == 1, nil
}

func (stub *planningVisions) UpdatePlanning(_ context.Context, projectID string, update vision.PlanningUpdate) (vision.Vision, error) {
	stub.updates = append(stub.updates, update)
	return vision.Vision{ProjectID: projectID}, nil
}

func (stub *planningVisions) PausePlanning(_ context.Context, projectID string) (vision.Vision, error) {
	stub.paused++
	return vision.Vision{ProjectID: projectID, Planning: &vision.Planning{Status: vision.PlanningPaused}}, nil
}

func (stub *planningVisions) FinishPlanning(_ context.Context, projectID string) (vision.Vision, error) {
	stub.finished++
	return vision.Vision{ProjectID: projectID}, nil
}

func (stub *planningVisions) DiscardPlanning(_ context.Context, projectID string) (vision.Vision, bool, error) {
	stub.discarded++
	if stub.discarded > 1 {
		return vision.Vision{}, true, nil
	}
	return vision.Vision{ProjectID: projectID, Plan: featureartifact.VisionPlan{Statement: "As before"}}, false, nil
}

// launchingConversations records the launches started.
type launchingConversations struct {
	*plannedConversations
	launched []string
}

func (stub *launchingConversations) StartLaunch(_ context.Context, scope string) (conversations.Conversation, error) {
	stub.launched = append(stub.launched, scope)
	return conversations.Conversation{Scope: scope}, nil
}

func planningStub(action featureartifact.AssistantAction) *launchingConversations {
	return &launchingConversations{plannedConversations: &plannedConversations{briefConversations: &briefConversations{
		conversationStub: &conversationStub{
			settings: conversations.Settings{Agent: "claude", Model: "claude-sonnet-5-5"},
			action:   conversations.Action{ID: "act_1", Status: conversations.ActionProposed, AssistantAction: action},
		}, notes: map[string]string{}}, woken: map[string]string{}}}
}

// Planning starts once and wakes the project's assistant; the assistant's
// update_plan reaches the plan at once, and build_plan finishes it and hands
// it to autopilot.
func TestAVisionIsPlannedThroughTheAssistant(t *testing.T) {
	stub := planningStub(featureartifact.AssistantAction{
		Type: featureartifact.AssistantUpdatePlan, Reason: "Settled", Description: "Keep stock in step.",
		Decisions: []string{"Sync nightly"}, StackPresets: []string{"go"}, Frameworks: "Go with SQLite",
		PlanItems: []featureartifact.PlanItem{{Priority: 0, Title: "Read stock"}},
	})
	visions := &planningVisions{}
	modes := &assistantModeProjects{recordingProjectService: &recordingProjectService{
		getByIDResult: project.Project{ID: "prj_shop", Name: "Shop sync"}}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(modes, &recordingFeatureService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub, testAgents(), visions)
	call := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return recorder
	}
	for range 2 {
		if recorder := call("/api/v1/projects/prj_shop/vision/planning", `{"goal":"Keep stock in step.","chat":"main"}`); recorder.Code != http.StatusOK {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
	}
	if strings.Join(visions.chats, ",") != "main,main" {
		t.Fatalf("planned in chats %v", visions.chats)
	}
	if len(stub.woken) != 1 || !strings.Contains(stub.woken["prj_shop"], "plan mode on") {
		t.Fatalf("woken = %v", stub.woken)
	}
	if recorder := call("/api/v1/projects/prj_shop/vision/planning/pause", ``); recorder.Code != http.StatusOK ||
		visions.paused != 1 || !strings.Contains(recorder.Body.String(), `"paused"`) {
		t.Fatalf("pause: status %d: %s", recorder.Code, recorder.Body.String())
	}

	if recorder := call("/api/v1/assistant/conversations/prj_shop/actions/act_1/approve", `{}`); recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	update := visions.updates[0]
	if update.Goal != "Keep stock in step." || strings.Join(update.Decisions, "|") != "Sync nightly" ||
		update.Questions == nil || update.Stack == nil || update.Stack.Frameworks != "Go with SQLite" || len(update.Items) != 1 {
		t.Fatalf("update = %+v", update)
	}

	stub.action.AssistantAction = featureartifact.AssistantAction{Type: featureartifact.AssistantBuildPlan, Reason: "The user said go"}
	if recorder := call("/api/v1/assistant/conversations/prj_shop/actions/act_1/approve", `{}`); recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if visions.finished != 1 || modes.received != project.AssistantModeBuilds || strings.Join(stub.launched, ",") != "prj_shop" {
		t.Fatalf("finished=%d mode=%q launched=%v", visions.finished, modes.received, stub.launched)
	}
	// Building it themselves, the user's project keeps how it works.
	modes.received = ""
	if recorder := call("/api/v1/projects/prj_shop/vision/planning/finish", `{"autopilot":false}`); recorder.Code != http.StatusOK ||
		visions.finished != 2 || modes.received != "" || len(stub.launched) != 1 {
		t.Fatalf("status %d, finished=%d mode=%q", recorder.Code, visions.finished, modes.received)
	}
}

// planningInChat is a vision already planned in one chat.
type planningInChat struct {
	*planningVisions
	chat string
}

func (stub *planningInChat) Get(_ context.Context, projectID string) (vision.Vision, error) {
	return vision.Vision{ProjectID: projectID, Planning: &vision.Planning{Status: vision.PlanningOpen, Chat: stub.chat}}, nil
}

// The assistant's start_planning card turns plan mode on in the chat it is
// in, as the plan switch does, with the project's name as the goal when it
// names none; planning already open in that chat only settles the card.
func TestStartPlanningTurnsPlanModeOnInItsChat(t *testing.T) {
	stub := planningStub(featureartifact.AssistantAction{Type: featureartifact.AssistantStartPlanning, Reason: "Several screens to settle"})
	stub.action.Chat = "chat_1"
	visions := &planningInChat{planningVisions: &planningVisions{}, chat: "chat_2"}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_shop", Name: "Shop sync"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub, testAgents(), visions)
	approve := func() {
		t.Helper()
		stub.action.Status = conversations.ActionProposed
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/v1/assistant/conversations/prj_shop/actions/act_1/approve", strings.NewReader(`{}`)))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
	}

	// Planned in another chat, it moves here.
	approve()
	if strings.Join(visions.chats, ",") != "chat_1" || strings.Join(visions.goals, ",") != "Shop sync" ||
		stub.resolvedStatus != conversations.ActionApplied || stub.resolvedResult != "Started plan mode in this chat." {
		t.Fatalf("chats=%v goals=%v result=%q %q", visions.chats, visions.goals, stub.resolvedStatus, stub.resolvedResult)
	}
	// The card had no goal: the assistant opens by asking what to plan.
	if woken := stub.woken["prj_shop"]; !strings.Contains(woken, "plan this project") ||
		!strings.Contains(woken, "They did not say what to plan.") {
		t.Fatalf("woken = %v", stub.woken)
	}

	// Its own goal starts the round.
	stub.action.Description, stub.action.Chat = "A weekly digest for the team", "chat_3"
	visions.goals, visions.chats = nil, nil
	approve()
	if strings.Join(visions.goals, ",") != "A weekly digest for the team" ||
		!strings.Contains(stub.woken["prj_shop"], "They came with something to plan: “A weekly digest for the team”.") {
		t.Fatalf("goals = %v woken = %v", visions.goals, stub.woken)
	}

	// Already planning in this chat, the card only settles.
	visions.chat = "chat_3"
	approve()
	if len(visions.goals) != 1 || stub.resolvedStatus != conversations.ActionApplied ||
		stub.resolvedResult != "Plan mode was already on in this chat." {
		t.Fatalf("goals=%v result=%q", visions.goals, stub.resolvedResult)
	}
}

// Planning opens on the idea the user came with, or, without one, by asking
// what to plan.
func TestPlanningWakeCarriesTheIdea(t *testing.T) {
	for _, round := range []bool{false, true} {
		_, text := planningWake(round, "", false)
		if !strings.Contains(text, "They did not say what to plan.") || !strings.Contains(text, "opening says") ||
			!strings.Contains(text, "clearly ask for something") || !strings.Contains(text, "is resumed") {
			t.Fatalf("no idea (round %v): %s", round, text)
		}
		_, text = planningWake(round, "  Weekly\n digest ", false)
		if !strings.Contains(text, "They came with something to plan: “Weekly digest”.") || strings.Contains(text, "did not say") {
			t.Fatalf("an idea (round %v): %s", round, text)
		}
		_, text = planningWake(round, "", true)
		if !strings.Contains(text, "their message with this note says what") || strings.Contains(text, "did not say") {
			t.Fatalf("with a message (round %v): %s", round, text)
		}
	}
	if note, text := planningWake(true, "x", false); !strings.Contains(note, "next round") || !strings.Contains(text, "what is built so far") {
		t.Fatalf("round: %s %s", note, text)
	}
}

// tellingConversations records what chats are told without a turn.
type tellingConversations struct {
	*launchingConversations
	told map[string]string
}

func (stub *tellingConversations) TellChat(_ context.Context, _, chat, note, _ string) error {
	stub.told[chat] = note
	return nil
}

// Discard switches plan mode off without the plan: the vision comes back as
// it was (or removed, when the session made it), and the planning chat's
// assistant hears that the plan is gone.
func TestDiscardingPlanningPutsTheVisionBack(t *testing.T) {
	stub := &tellingConversations{launchingConversations: planningStub(featureartifact.AssistantAction{}), told: map[string]string{}}
	visions := &planningInChat{planningVisions: &planningVisions{}, chat: "chat_2"}
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_shop", Name: "Shop sync"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{},
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub, testAgents(), visions)
	discard := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/v1/projects/prj_shop/vision/planning/discard", strings.NewReader(``)))
		return recorder
	}
	recorder := discard()
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"statement":"As before"`) ||
		!strings.Contains(recorder.Body.String(), `"removed":false`) {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if stub.told["chat_2"] != "Discarded the plan." {
		t.Fatalf("told = %v", stub.told)
	}
	if recorder := discard(); recorder.Code != http.StatusOK ||
		!strings.Contains(recorder.Body.String(), `{"vision":null,"removed":true}`) {
		t.Fatalf("removed: status %d: %s", recorder.Code, recorder.Body.String())
	}
}
