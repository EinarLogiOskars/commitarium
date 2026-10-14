package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// wakingConversations records what woke the assistant.
type wakingConversations struct {
	*conversationStub
	woken     []string
	handovers []string
}

func (stub *wakingConversations) Wake(_ context.Context, scope, _, text string) error {
	stub.woken = append(stub.woken, scope+": "+text)
	return nil
}

func (stub *wakingConversations) HandOver(_ context.Context, scope string) error {
	stub.handovers = append(stub.handovers, scope)
	return nil
}

// Switching a project to building everything hands it to the assistant at
// once; switching back, or setting the mode it has, wakes no one.
func TestSwitchingToBuildingEverythingHandsTheProjectOver(t *testing.T) {
	for name, test := range map[string]struct {
		before, after project.AssistantMode
		handsOver     bool
	}{
		"assisting to building":   {project.AssistantModeAssists, project.AssistantModeBuilds, true},
		"a project from before":   {"", project.AssistantModeBuilds, true},
		"building to assisting":   {project.AssistantModeBuilds, project.AssistantModeAssists, false},
		"building, set once more": {project.AssistantModeBuilds, project.AssistantModeBuilds, false},
	} {
		t.Run(name, func(t *testing.T) {
			projects := &assistantModeProjects{recordingProjectService: &recordingProjectService{
				getByIDResult: project.Project{ID: "prj_app", AssistantMode: test.before},
			}}
			waker := &wakingConversations{conversationStub: &conversationStub{}}
			handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, waker)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/v1/projects/prj_app/assistant-mode",
				strings.NewReader(`{"assistant_mode":"`+string(test.after)+`"}`)))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			if handed := len(waker.handovers) == 1 && waker.handovers[0] == "prj_app"; handed != test.handsOver {
				t.Fatalf("handovers = %v, want handed over %t", waker.handovers, test.handsOver)
			}
		})
	}
}

// Building everything, a work order the user creates by hand wakes the
// assistant to take it over; assisting, or a session, does not.
func TestAWorkOrderTheUserAddsWakesTheAssistantThatBuilds(t *testing.T) {
	for name, test := range map[string]struct {
		mode  project.AssistantMode
		kind  feature.Kind
		wakes bool
	}{
		"building, a work order": {project.AssistantModeBuilds, feature.KindWorkOrder, true},
		"assisting":              {project.AssistantModeAssists, feature.KindWorkOrder, false},
		"building, a session":    {project.AssistantModeBuilds, feature.KindSession, false},
	} {
		t.Run(name, func(t *testing.T) {
			features := &recordingFeatureService{createResult: feature.Feature{
				ID: "fea_sort", ProjectID: "prj_app", Title: "Sort the shelf", State: feature.StateDraft, Kind: test.kind,
			}}
			projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_app", AssistantMode: test.mode}}
			waker := &wakingConversations{conversationStub: &conversationStub{}}
			handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, waker)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_app/features",
				strings.NewReader(`{"title":"Sort the shelf","kind":"`+string(test.kind)+`"}`)))
			if recorder.Code != http.StatusCreated {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			woke := len(waker.woken) == 1 && strings.Contains(waker.woken[0], "“Sort the shelf” (fea_sort)")
			if woke != test.wakes {
				t.Fatalf("woken = %v, want woken %t", waker.woken, test.wakes)
			}
		})
	}
}

// The user may change a card's agents as well as its models; the
// coordinator refuses a choice that cannot run as it does any other
// (ADR-027).
func TestApprovingACardWithOtherAgentsIsCheckedLikeAnyChoice(t *testing.T) {
	later := time.Now().Add(time.Hour)
	card := conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Reason: "Asked", Title: "Sort the shelf", Steps: "full",
			LeadAgent: "codex", LeadModel: "gpt-6-luna", ReviewerAgent: "claude", ReviewerModel: "claude-sonnet-5-5",
		},
	}
	for name, test := range map[string]struct {
		body   string
		limits []execution.LimitWait
		code   string
		lead   string
	}{
		"swapped agents and models": {
			body: `{"lead_agent":"claude","lead_model":"claude-sonnet-5-5","reviewer_agent":"codex","reviewer_model":"gpt-6-luna"}`,
			lead: "claude",
		},
		"an agent without its model": {body: `{"lead_agent":"claude"}`, code: "model_unavailable"},
		"an agent that is gone":      {body: `{"lead_agent":"codex-old"}`, code: "agent_not_found"},
		"an agent at its limit, started": {
			body:   `{"start":true,"lead_agent":"claude","lead_model":"claude-sonnet-5-5"}`,
			limits: []execution.LimitWait{{AgentID: "claude", ResetsAt: &later}}, code: "agent_at_limit",
		},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &conversationStub{action: card}
			features := &recordingFeatureService{createResult: feature.Feature{
				ID: "fea_sort", ProjectID: "prj_test", Title: "Sort the shelf", State: feature.StateDraft,
			}}
			projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
			executions := &waitingExecutions{recordingExecutionService: &recordingExecutionService{}, limitWaits: test.limits}
			handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, features, nil, executions, nil, nil, nil, nil, nil,
				offeredModels, nil, nil, stub, testAgents())
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
				"/api/v1/assistant/conversations/prj_test/actions/act_1/approve", strings.NewReader(test.body)))
			if test.code != "" {
				if !strings.Contains(recorder.Body.String(), test.code) || features.createCalls != 0 {
					t.Fatalf("status %d: %s (created %d), want %s", recorder.Code, recorder.Body.String(), features.createCalls, test.code)
				}
				return
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			chosen := features.receivedOverrides.AgentProviders
			if chosen == nil || chosen.LeadAgent != test.lead || chosen.Lead != project.AgentProviderClaude ||
				chosen.ReviewerAgent != "codex" || features.receivedOverrides.AgentModels.Lead != "claude-sonnet-5-5" {
				t.Fatalf("agents = %+v models = %+v", chosen, features.receivedOverrides.AgentModels)
			}
			if !strings.Contains(stub.resolvedResult, "The user chose lead agent claude") {
				t.Fatalf("result = %q", stub.resolvedResult)
			}
		})
	}
}
