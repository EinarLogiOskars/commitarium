package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestAcceptGoal(t *testing.T) {
	acceptedAt := time.Date(2026, time.September, 9, 15, 0, 0, 0, time.UTC)
	payload, err := workflow.EncodeGoalAcceptedPayload("Ship CSV export.", "ses_lead")
	if err != nil {
		t.Fatalf("encode goal acceptance: %v", err)
	}
	executions := &recordingExecutionService{session: execution.Session{ID: "ses_lead"}}
	controller := &recordingSessionController{goalResult: workflow.Event{
		ID: "evt_goal", AggregateID: "fea_test", Type: workflow.EventTypeGoalAccepted,
		Actor:      workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID},
		OccurredAt: acceptedAt, Sequence: 3, PayloadVersion: workflow.GoalAcceptedPayloadVersion,
		IdempotencyKey: "accept-1", Payload: payload,
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/sessions/ses_lead/goal-acceptance",
		strings.NewReader(`{"goal":"Ship CSV export."}`),
	)
	request.Header.Set("Idempotency-Key", "accept-1")
	recorder := httptest.NewRecorder()

	New(nil, nil, nil, executions, controller, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if controller.sessionID != "ses_lead" || controller.goal != "Ship CSV export." ||
		controller.goalKey != "accept-1" ||
		controller.goalActor != (workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID}) {
		t.Fatalf("unexpected acceptance request: %+v", controller)
	}
	var body acceptGoalResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode goal acceptance response: %v", err)
	}
	if body.FeatureID != "fea_test" || body.SessionID != "ses_lead" ||
		body.Goal != "Ship CSV export." || body.EventID != "evt_goal" ||
		body.Sequence != 3 || body.AcceptedAt != acceptedAt {
		t.Fatalf("unexpected goal acceptance response %+v", body)
	}
}

func TestAcceptGoalRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		key        string
		sessionErr error
		goalErr    error
		status     int
		code       string
	}{
		{name: "missing session", body: `{"goal":"Ship it"}`, key: "accept-1", sessionErr: execution.ErrNotFound, status: 404, code: "session_not_found"},
		{name: "missing key", body: `{"goal":"Ship it"}`, status: 400, code: "idempotency_key_required"},
		{name: "invalid JSON", body: `{`, key: "accept-1", status: 400, code: "invalid_json"},
		{name: "blank goal", body: `{"goal":" "}`, key: "accept-1", goalErr: workflow.ErrInvalidGoalAcceptance, status: 400, code: "goal_required"},
		{name: "key conflict", body: `{"goal":"Ship it"}`, key: "accept-1", goalErr: workflow.ErrIdempotencyConflict, status: 409, code: "idempotency_conflict"},
		{name: "already accepted", body: `{"goal":"Ship it"}`, key: "accept-1", goalErr: workflow.ErrGoalAlreadyAccepted, status: 409, code: "goal_already_accepted"},
		{name: "wrong boundary", body: `{"goal":"Ship it"}`, key: "accept-1", goalErr: workflow.ErrGoalAcceptanceNotAllowed, status: 409, code: "goal_acceptance_not_allowed"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executions := &recordingExecutionService{
				session: execution.Session{ID: "ses_lead"}, sessionErr: test.sessionErr,
			}
			controller := &recordingSessionController{goalErr: test.goalErr}
			request := httptest.NewRequest(
				http.MethodPost, "/api/v1/sessions/ses_lead/goal-acceptance",
				strings.NewReader(test.body),
			)
			request.Header.Set("Idempotency-Key", test.key)
			recorder := httptest.NewRecorder()
			New(nil, nil, nil, executions, controller, nil).ServeHTTP(recorder, request)

			response := recorder.Result()
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("expected status %d, got %d", test.status, response.StatusCode)
			}
			var body errorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != test.code {
				t.Fatalf("expected error code %q, got %q", test.code, body.Error.Code)
			}
			if errors.Is(test.sessionErr, execution.ErrNotFound) && controller.sessionID != "" {
				t.Fatal("controller was called for a missing session")
			}
		})
	}
}
