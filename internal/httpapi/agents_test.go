package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type recordingAgentService struct {
	agents    map[string]agent.Agent
	deleteErr error
}

func (s *recordingAgentService) List(context.Context) ([]agent.Agent, error) {
	list := make([]agent.Agent, 0, len(s.agents))
	for _, a := range s.agents {
		list = append(list, a)
	}
	return list, nil
}

func (s *recordingAgentService) Get(_ context.Context, id string) (agent.Agent, error) {
	if a, ok := s.agents[id]; ok {
		return a, nil
	}
	return agent.Agent{}, agent.ErrNotFound
}

func (s *recordingAgentService) Create(_ context.Context, name string, provider project.AgentProvider) (agent.Agent, error) {
	if !provider.IsValid() {
		return agent.Agent{}, agent.ErrInvalid
	}
	created := agent.Agent{ID: agent.IDFromName(name), Name: name, Provider: provider, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.agents[created.ID] = created
	return created, nil
}

func (s *recordingAgentService) Rename(_ context.Context, id, name string) (agent.Agent, error) {
	a, ok := s.agents[id]
	if !ok {
		return agent.Agent{}, agent.ErrNotFound
	}
	a.Name = name
	s.agents[id] = a
	return a, nil
}

func (s *recordingAgentService) Delete(_ context.Context, id string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.agents, id)
	return nil
}

func TestAgentRoutes(t *testing.T) {
	agents := &recordingAgentService{agents: map[string]agent.Agent{}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, agents)
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}
	created := serve(http.MethodPost, "/api/v1/agents", `{"name":"Claude Max","provider":"claude"}`)
	var body agentResponse
	if err := json.NewDecoder(created.Body).Decode(&body); err != nil || created.Code != http.StatusCreated ||
		body.ID != "claude-max" || body.Provider != "claude" {
		t.Fatalf("create: status=%d body=%+v err=%v", created.Code, body, err)
	}
	if got := serve(http.MethodPost, "/api/v1/agents", `{"name":"Gemini","provider":"gemini"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("unsupported provider status=%d", got.Code)
	}
	if got := serve(http.MethodPatch, "/api/v1/agents/claude-max", `{"name":"Claude Max (work)"}`); got.Code != http.StatusOK ||
		agents.agents["claude-max"].Name != "Claude Max (work)" {
		t.Fatalf("rename status=%d agents=%+v", got.Code, agents.agents)
	}
	agents.deleteErr = agent.ErrInUse
	if got := serve(http.MethodDelete, "/api/v1/agents/claude-max", ""); got.Code != http.StatusConflict {
		t.Fatalf("delete in use status=%d", got.Code)
	}
	agents.deleteErr = nil
	if got := serve(http.MethodDelete, "/api/v1/agents/claude-max", ""); got.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", got.Code)
	}
}

// signInExecutions has one agent that needs a sign-in, with two runs waiting.
type signInExecutions struct {
	*recordingExecutionService
}

func (signInExecutions) AgentSignIns(context.Context) ([]execution.AgentSignIn, error) {
	return []execution.AgentSignIn{{AgentID: "claude-max", Since: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}}, nil
}

func (signInExecutions) RunsAwaitingLogin(context.Context, string) ([]string, error) {
	return []string{"run_1", "run_2"}, nil
}

// The agents list says which agent needs a sign-in and what waits on it, not
// "verified" credentials (LOGIN_RECOVERY_PLAN.md).
func TestTheAgentsListSaysWhoNeedsASignIn(t *testing.T) {
	agents := &recordingAgentService{agents: map[string]agent.Agent{
		"claude-max": {ID: "claude-max", Name: "Claude Max", Provider: project.AgentProviderClaude},
		"codex":      {ID: "codex", Name: "Codex", Provider: project.AgentProviderCodex},
	}}
	executions := signInExecutions{&recordingExecutionService{}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(nil, nil, nil, executions, nil, nil, nil, nil, nil, nil, nil, nil, agents)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil))
	var listed []agentResponse
	if err := json.NewDecoder(recorder.Body).Decode(&listed); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("list: status=%d err=%v", recorder.Code, err)
	}
	for _, item := range listed {
		switch item.ID {
		case "claude-max":
			if item.NeedsSignIn == nil || item.NeedsSignIn.Waiting != 2 {
				t.Fatalf("claude-max = %+v", item)
			}
		case "codex":
			if item.NeedsSignIn != nil {
				t.Fatalf("codex needs no sign-in: %+v", item)
			}
		}
	}
}
