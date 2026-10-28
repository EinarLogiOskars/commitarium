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
	goals    []string
	updates  []vision.PlanningUpdate
	finished int
	paused   int
	// described are the descriptions a new app's vision is written from.
	described []string
}

func (stub *planningVisions) Start(_ context.Context, _, _, _ string, _ vision.Mode, message, _ string) (vision.Session, bool, error) {
	stub.described = append(stub.described, message)
	return vision.Session{ID: "vis_1"}, true, nil
}

func (stub *planningVisions) Get(context.Context, string) (vision.Vision, error) {
	return vision.Vision{}, vision.ErrNotFound
}

func (stub *planningVisions) StartPlanning(_ context.Context, projectID, goal string) (vision.Vision, bool, error) {
	stub.goals = append(stub.goals, goal)
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
		if recorder := call("/api/v1/projects/prj_shop/vision/planning", `{"goal":"Keep stock in step."}`); recorder.Code != http.StatusOK {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
	}
	if len(stub.woken) != 1 || !strings.Contains(stub.woken["prj_shop"], "planning workspace") {
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
