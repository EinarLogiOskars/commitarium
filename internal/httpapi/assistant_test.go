package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/assistant"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type recordingWorkOrderAssistant struct {
	agent     string
	model     string
	acceptKey string
	acceptErr error
}

func (a *recordingWorkOrderAssistant) Start(_ context.Context, _, featureID, agentID, model string) (assistant.Session, bool, error) {
	a.agent, a.model = agentID, model
	return assistant.Session{ID: "ast_test", FeatureID: featureID, Agent: agentID, Model: model, Status: assistant.StatusRunning}, true, nil
}

func (a *recordingWorkOrderAssistant) Get(context.Context, string, string) (assistant.Session, error) {
	return assistant.Session{}, assistant.ErrNotFound
}

func (a *recordingWorkOrderAssistant) Reply(context.Context, string, string, string, string) (assistant.Session, bool, error) {
	return assistant.Session{}, false, assistant.ErrNotReady
}

func (a *recordingWorkOrderAssistant) AcceptBrief(_ context.Context, _, featureID, key string) (feature.Feature, error) {
	a.acceptKey = key
	return feature.Feature{ID: featureID, State: feature.StateReady}, a.acceptErr
}

func (a *recordingWorkOrderAssistant) Reopen(_ context.Context, _, featureID, _ string) (feature.Feature, error) {
	return feature.Feature{ID: featureID, State: feature.StateDraft}, nil
}

func TestWorkOrderAssistantRoutes(t *testing.T) {
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", State: feature.StateDraft,
		AgentProviders: project.AgentProviders{Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex},
		AgentModels:    project.AgentModels{Lead: "claude-opus-5-5", Reviewer: "gpt-6"},
	}}
	clarifier := &recordingWorkOrderAssistant{}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		nil, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, clarifier,
	)
	serve := func(method, path, body, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	started := serve(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/assistant", "", "")
	if started.Code != http.StatusCreated || clarifier.agent != "claude" || clarifier.model != "claude-opus-5-5" {
		t.Fatalf("start: status=%d agent=%q model=%q", started.Code, clarifier.agent, clarifier.model)
	}
	var session assistantSessionResponse
	if err := json.NewDecoder(started.Body).Decode(&session); err != nil || session.Status != "running" {
		t.Fatalf("start body=%+v err=%v", session, err)
	}
	if got := serve(http.MethodGet, "/api/v1/projects/prj_test/features/fea_test/assistant", "", ""); got.Code != http.StatusNotFound {
		t.Fatalf("missing session status=%d", got.Code)
	}
	if got := serve(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/assistant/messages", `{"message":"Hi"}`, "reply-1"); got.Code != http.StatusConflict {
		t.Fatalf("busy reply status=%d", got.Code)
	}
	if got := serve(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/accept", "", ""); got.Code != http.StatusBadRequest {
		t.Fatalf("accept without a key status=%d", got.Code)
	}
	accepted := serve(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/accept", "", "accept-1")
	var body featureResponse
	if err := json.NewDecoder(accepted.Body).Decode(&body); err != nil || accepted.Code != http.StatusOK ||
		body.State != feature.StateReady || clarifier.acceptKey != "accept-1" {
		t.Fatalf("accept: status=%d body=%+v err=%v", accepted.Code, body, err)
	}
	clarifier.acceptErr = assistant.ErrNotReady
	if got := serve(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/accept", "", "accept-2"); got.Code != http.StatusConflict {
		t.Fatalf("accept without a brief status=%d", got.Code)
	}
}
