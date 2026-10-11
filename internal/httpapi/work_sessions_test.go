package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type sessionMessengerStub struct {
	*planningStarterStub
	runID   string
	command worker.Command
	err     error
}

func (s *sessionMessengerStub) SendSessionMessage(
	_ context.Context,
	runID string,
	command worker.Command,
) (execution.Run, error) {
	s.runID, s.command = runID, command
	return s.planningStarterStub.run, s.err
}

func TestSendSessionMessageStartsTheNextTurn(t *testing.T) {
	now := time.Now().UTC()
	run := execution.Run{
		ID: "run_session", FeatureID: "fea_session", Status: execution.RunStatusRunning,
		StartedAt: now, UpdatedAt: now,
	}
	features := &recordingFeatureService{getResult: feature.Feature{
		ID: "fea_session", ProjectID: "prj_test", Kind: feature.KindSession, State: feature.StateImplementing,
	}}
	executions := &recordingExecutionService{run: run, runs: []execution.Run{run}}
	messenger := &sessionMessengerStub{planningStarterStub: &planningStarterStub{run: run}}
	send := func(body, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost,
			"/api/v1/projects/prj_test/features/fea_session/messages", strings.NewReader(body))
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		recorder := httptest.NewRecorder()
		NewWithWorkspaceAndRealWorkflowService(nil, features, nil, executions, nil, nil, nil, messenger).
			ServeHTTP(recorder, request)
		return recorder
	}

	if recorder := send(`{"message":"Make the buttons blue."}`, "msg-1"); recorder.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if messenger.runID != "run_session" || messenger.command != (worker.Command{
		ID: "msg-1", Type: worker.CommandMessage, Message: "Make the buttons blue.",
	}) {
		t.Fatalf("unexpected message %q %+v", messenger.runID, messenger.command)
	}
	if recorder := send(`{"message":"x"}`, ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("message without a key: %d", recorder.Code)
	}
	messenger.err = orchestration.ErrSessionBusy
	if recorder := send(`{"message":"x"}`, "msg-2"); recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "session_busy") {
		t.Fatalf("busy session: %d %s", recorder.Code, recorder.Body.String())
	}
	features.getResult.Kind = feature.KindWorkOrder
	if recorder := send(`{"message":"x"}`, "msg-3"); recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "not_a_session") {
		t.Fatalf("message to a work order: %d %s", recorder.Code, recorder.Body.String())
	}
}
