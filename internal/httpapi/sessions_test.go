package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type recordingExecutionService struct {
	run          execution.Run
	runErr       error
	sessions     []execution.Session
	sessionsErr  error
	session      execution.Session
	sessionErr   error
	events       []execution.Event
	eventsErr    error
	subscription <-chan execution.Event
	unsubscribed int
	requestedID  string
	eventSession string
	planning     []execution.PlanningMessage
	planningLive <-chan execution.PlanningMessage
}

func (s *recordingExecutionService) GetRun(
	_ context.Context,
	_ string,
) (execution.Run, error) {
	return s.run, s.runErr
}

func (s *recordingExecutionService) SessionsForRun(
	_ context.Context,
	_ string,
) ([]execution.Session, error) {
	return s.sessions, s.sessionsErr
}

func (s *recordingExecutionService) SubscribeSessionEvents(
	_ string,
) (<-chan execution.Event, func()) {
	if s.subscription == nil {
		s.subscription = make(chan execution.Event)
	}
	return s.subscription, func() { s.unsubscribed++ }
}

func (s *recordingExecutionService) GetSession(
	_ context.Context,
	id string,
) (execution.Session, error) {
	s.requestedID = id
	return s.session, s.sessionErr
}

func (s *recordingExecutionService) EventsForSession(
	_ context.Context,
	sessionID string,
) ([]execution.Event, error) {
	s.eventSession = sessionID
	return s.events, s.eventsErr
}

func (s *recordingExecutionService) PlanningMessagesForRun(
	_ context.Context,
	_ string,
) ([]execution.PlanningMessage, error) {
	return s.planning, nil
}

func (s *recordingExecutionService) SubscribePlanningMessages(
	_ string,
) (<-chan execution.PlanningMessage, func()) {
	if s.planningLive == nil {
		s.planningLive = make(chan execution.PlanningMessage)
	}
	return s.planningLive, func() {}
}

type recordingSessionController struct {
	sessionID  string
	command    worker.Command
	result     execution.Command
	err        error
	goal       string
	goalActor  workflow.Actor
	goalKey    string
	goalResult workflow.Event
	goalErr    error
}

func (c *recordingSessionController) AcceptGoal(
	_ context.Context,
	sessionID string,
	goal string,
	actor workflow.Actor,
	idempotencyKey string,
) (workflow.Event, error) {
	c.sessionID = sessionID
	c.goal = goal
	c.goalActor = actor
	c.goalKey = idempotencyKey
	return c.goalResult, c.goalErr
}

func (c *recordingSessionController) SendCommand(
	_ context.Context,
	sessionID string,
	command worker.Command,
) (execution.Command, error) {
	c.sessionID = sessionID
	c.command = command
	return c.result, c.err
}

func TestGetSessionAndEvents(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 1, 0, 0, 0, time.UTC)
	executions := &recordingExecutionService{
		session: execution.Session{
			ID: "ses_test", RunID: "run_test", AgentID: "agt_codex",
			Role: worker.RoleCoder, Status: execution.SessionStatusRunning,
			StartedAt: fixedTime, UpdatedAt: fixedTime,
		},
		events: []execution.Event{{
			ID: "sev_test", SessionID: "ses_test", Sequence: 1,
			Type: worker.EventActivity, Text: "editing files", OccurredAt: fixedTime,
		}},
	}
	handler := New(nil, nil, nil, executions, nil, nil)

	sessionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sessionRecorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/sessions/ses_test",
		nil,
	))
	if sessionRecorder.Code != http.StatusOK {
		t.Fatalf("expected session status %d, got %d", http.StatusOK, sessionRecorder.Code)
	}
	var sessionBody sessionResponse
	if err := json.NewDecoder(sessionRecorder.Body).Decode(&sessionBody); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if sessionBody.ID != "ses_test" || sessionBody.Status != execution.SessionStatusRunning {
		t.Errorf("unexpected session response %+v", sessionBody)
	}

	eventsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(eventsRecorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/sessions/ses_test/events",
		nil,
	))
	if eventsRecorder.Code != http.StatusOK {
		t.Fatalf("expected events status %d, got %d", http.StatusOK, eventsRecorder.Code)
	}
	var eventBody []sessionEventResponse
	if err := json.NewDecoder(eventsRecorder.Body).Decode(&eventBody); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(eventBody) != 1 ||
		eventBody[0].Sequence != 1 ||
		eventBody[0].Text != "editing files" {
		t.Errorf("unexpected event response %+v", eventBody)
	}
}

func TestSendSessionCommand(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 1, 0, 0, 0, time.UTC)
	appliedAt := fixedTime.Add(time.Second)
	executions := &recordingExecutionService{session: execution.Session{ID: "ses_test"}}
	controller := &recordingSessionController{result: execution.Command{
		ID: "cmd_test", SessionID: "ses_test", Type: worker.CommandMessage,
		Message: "Please use the simpler option.", Status: execution.CommandStatusApplied,
		RequestedAt: fixedTime, AppliedAt: &appliedAt,
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/sessions/ses_test/commands",
		strings.NewReader(`{"type":"message","message":"Please use the simpler option."}`),
	)
	request.Header.Set("Idempotency-Key", "cmd_test")
	recorder := httptest.NewRecorder()

	New(nil, nil, nil, executions, controller, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if controller.sessionID != "ses_test" ||
		controller.command.ID != "cmd_test" ||
		controller.command.Type != worker.CommandMessage ||
		controller.command.Message != "Please use the simpler option." {
		t.Errorf("unexpected routed command %+v for %q", controller.command, controller.sessionID)
	}
	var body sessionCommandResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode command response: %v", err)
	}
	if body.Status != execution.CommandStatusApplied || body.AppliedAt == nil {
		t.Errorf("unexpected command response %+v", body)
	}
}

func TestSessionEndpointsMapExpectedErrors(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		path          string
		body          string
		key           string
		sessionErr    error
		controllerErr error
		status        int
		code          string
	}{
		{name: "missing session", method: http.MethodGet, path: "/api/v1/sessions/missing", sessionErr: execution.ErrNotFound, status: 404, code: "session_not_found"},
		{name: "missing command key", method: http.MethodPost, path: "/api/v1/sessions/ses_test/commands", body: `{"type":"pause"}`, status: 400, code: "idempotency_key_required"},
		{name: "inactive session", method: http.MethodPost, path: "/api/v1/sessions/ses_test/commands", body: `{"type":"pause"}`, key: "cmd", controllerErr: orchestration.ErrSessionNotActive, status: 409, code: "session_not_active"},
		{name: "invalid state", method: http.MethodPost, path: "/api/v1/sessions/ses_test/commands", body: `{"type":"pause"}`, key: "cmd", controllerErr: orchestration.ErrCommandNotAllowed, status: 409, code: "command_not_allowed"},
		{name: "continuation workspace conflict", method: http.MethodPost, path: "/api/v1/sessions/ses_test/commands", body: `{"type":"message","message":"Continue"}`, key: "cmd", controllerErr: fmt.Errorf("verify continuation: %w", workspace.ErrCheckoutConflict), status: 409, code: "implementation_continuation_not_ready"},
		{name: "continuation Forgejo unavailable", method: http.MethodPost, path: "/api/v1/sessions/ses_test/commands", body: `{"type":"message","message":"Continue"}`, key: "cmd", controllerErr: project.ErrForgejoUnavailable, status: 503, code: "forgejo_unavailable"},
		{name: "key conflict", method: http.MethodPost, path: "/api/v1/sessions/ses_test/commands", body: `{"type":"pause"}`, key: "cmd", controllerErr: execution.ErrCommandConflict, status: 409, code: "idempotency_conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executions := &recordingExecutionService{
				session:    execution.Session{ID: "ses_test"},
				sessionErr: test.sessionErr,
			}
			controller := &recordingSessionController{err: test.controllerErr}
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Idempotency-Key", test.key)
			recorder := httptest.NewRecorder()
			New(nil, nil, nil, executions, controller, nil).ServeHTTP(recorder, request)

			if recorder.Code != test.status {
				t.Fatalf("expected status %d, got %d", test.status, recorder.Code)
			}
			var body errorResponse
			if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != test.code {
				t.Errorf("expected code %q, got %q", test.code, body.Error.Code)
			}
		})
	}
}
