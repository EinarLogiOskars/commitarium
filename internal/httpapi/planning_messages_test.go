package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestPlanningMessagesExposeOneOrderedCrossSessionFeed(t *testing.T) {
	now := time.Date(2026, time.September, 9, 21, 0, 0, 0, time.UTC)
	submitted := planningMessage(
		"run_plan", "sev_final", "ses_lead", "codex-lead", worker.RoleLead, 3,
		"Final agreed plan", now.Add(2*time.Second),
	)
	submitted.Event.Type = worker.EventPlanSubmitted
	executions := &recordingExecutionService{
		run: execution.Run{ID: "run_plan"},
		planning: []execution.PlanningMessage{
			planningMessage("run_plan", "sev_lead", "ses_lead", "codex-lead", worker.RoleLead, 1, "Lead proposal", now),
			planningMessage("run_plan", "sev_review", "ses_review", "codex-reviewer", worker.RoleReviewer, 2, "Reviewer response", now.Add(time.Second)),
			submitted,
		},
	}
	handler := New(nil, nil, nil, executions, nil, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet, "/api/v1/runs/run_plan/planning/messages", nil,
	))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response []planningMessageResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode planning messages: %v", err)
	}
	if len(response) != 3 || response[0].Role != worker.RoleLead ||
		response[1].Role != worker.RoleReviewer || response[1].Sequence != 2 ||
		response[2].Type != worker.EventPlanSubmitted || response[2].Text != "Final agreed plan" {
		t.Fatalf("unexpected planning response %+v", response)
	}
}

func TestPlanningMessageStreamResumesAfterLastMessage(t *testing.T) {
	now := time.Date(2026, time.September, 9, 21, 0, 0, 0, time.UTC)
	executions := &recordingExecutionService{
		run: execution.Run{ID: "run_plan"},
		planning: []execution.PlanningMessage{
			planningMessage("run_plan", "sev_lead", "ses_lead", "codex-lead", worker.RoleLead, 1, "Lead proposal", now),
			planningMessage("run_plan", "sev_review", "ses_review", "codex-reviewer", worker.RoleReviewer, 2, "Reviewer response", now.Add(time.Second)),
		},
	}
	handler := New(nil, nil, nil, executions, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_plan/planning/messages/stream", nil)
	request.Header.Set("Last-Event-ID", "sev_lead")
	recorder := httptest.NewRecorder()
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)
	handler.ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if strings.Contains(body, "id: sev_lead") || !strings.Contains(body, "id: sev_review") {
		t.Fatalf("unexpected planning stream %q", body)
	}
}

func planningMessage(
	runID, eventID, sessionID, agentID string,
	role worker.Role,
	sequence int64,
	text string,
	occurredAt time.Time,
) execution.PlanningMessage {
	return execution.PlanningMessage{
		RunID: runID, Sequence: sequence, AgentID: agentID, Role: role,
		Event: execution.Event{
			ID: eventID, SessionID: sessionID, Sequence: 1,
			Type: worker.EventMessage, Text: text, OccurredAt: occurredAt,
		},
		LinkedAt: occurredAt,
	}
}
