package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type modelChangerStub struct {
	*planningStarterStub
	runID  string
	models project.AgentModels
	now    bool
}

func (stub *modelChangerStub) ChangeModels(_ context.Context, runID string, models project.AgentModels, now bool) (execution.Run, error) {
	stub.runID, stub.models, stub.now = runID, models, now
	return stub.planningStarterStub.run, nil
}

func TestChangeRunModelsStopsNowOrAtTheNextTurn(t *testing.T) {
	now := time.Now().UTC()
	run := execution.Run{
		ID: "run_models", FeatureID: "fea_models", Status: execution.RunStatusRunning,
		AgentModels: project.AgentModels{Lead: "expensive", Reviewer: "reviewer"},
		StartedAt:   now, UpdatedAt: now,
	}
	changer := &modelChangerStub{planningStarterStub: &planningStarterStub{run: run}}
	send := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		NewWithWorkspaceAndRealWorkflowService(nil, &recordingFeatureService{}, nil,
			&recordingExecutionService{run: run, runs: []execution.Run{run}}, nil, nil, nil, changer,
		).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_models/models", strings.NewReader(body)))
		return recorder
	}
	if recorder := send(`{"lead_model":"cheap","when":"now"}`); recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if changer.runID != "run_models" || changer.models.Lead != "cheap" || !changer.now {
		t.Fatalf("change = %q %+v now=%t", changer.runID, changer.models, changer.now)
	}
	if recorder := send(`{"lead_model":"cheap","when":"next_turn"}`); recorder.Code != http.StatusOK || changer.now {
		t.Fatalf("next turn: %d now=%t", recorder.Code, changer.now)
	}
	if recorder := send(`{"lead_model":"cheap","when":"later"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unknown timing was accepted: %d", recorder.Code)
	}
	if recorder := send(`{"when":"now"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("a change without a model was accepted: %d", recorder.Code)
	}
}
