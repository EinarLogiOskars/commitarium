package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestExecutionStoreOrdersPlanningMessagesAcrossSessionsWithoutCopyingText(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, lead := createExecutionRecords(t, db, store)
	now := lead.StartedAt.Add(time.Second)
	reviewer := execution.Session{
		ID: "ses_reviewer", RunID: run.ID, AgentID: "codex-reviewer",
		Role: worker.RoleReviewer, Status: execution.SessionStatusStarting,
		StartedAt: now, UpdatedAt: now,
	}
	if err := store.CreateSession(t.Context(), reviewer); err != nil {
		t.Fatalf("create reviewer: %v", err)
	}
	leadEvent, _, err := store.AppendEvent(t.Context(), execution.PendingEvent{
		ID: "sev_lead_plan", SessionID: lead.ID, Type: worker.EventMessage,
		Text: "Lead proposal", OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("append lead proposal: %v", err)
	}
	reviewerEvent, _, err := store.AppendEvent(t.Context(), execution.PendingEvent{
		ID: "sev_reviewer_response", SessionID: reviewer.ID, Type: worker.EventMessage,
		Text: "Reviewer response", OccurredAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("append reviewer response: %v", err)
	}

	first, created, err := store.LinkPlanningMessage(t.Context(), execution.PendingPlanningMessage{
		RunID: run.ID, EventID: leadEvent.ID, LinkedAt: now.Add(2 * time.Second),
	})
	if err != nil || !created || first.Sequence != 1 || first.AgentID != lead.AgentID {
		t.Fatalf("link lead proposal: message=%+v created=%t err=%v", first, created, err)
	}
	second, created, err := store.LinkPlanningMessage(t.Context(), execution.PendingPlanningMessage{
		RunID: run.ID, EventID: reviewerEvent.ID, LinkedAt: now.Add(3 * time.Second),
	})
	if err != nil || !created || second.Sequence != 2 || second.Role != worker.RoleReviewer {
		t.Fatalf("link reviewer response: message=%+v created=%t err=%v", second, created, err)
	}
	retried, created, err := store.LinkPlanningMessage(t.Context(), execution.PendingPlanningMessage{
		RunID: run.ID, EventID: leadEvent.ID, LinkedAt: now.Add(time.Hour),
	})
	if err != nil || created || retried != first {
		t.Fatalf("retry lead link: message=%+v created=%t err=%v", retried, created, err)
	}

	messages, err := store.ListPlanningMessages(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("list planning messages: %v", err)
	}
	if len(messages) != 2 || messages[0] != first || messages[1] != second {
		t.Fatalf("unexpected planning history %+v", messages)
	}
	var storedText string
	if err := db.QueryRowContext(
		t.Context(), `SELECT text FROM session_events WHERE id = ?`, reviewerEvent.ID,
	).Scan(&storedText); err != nil || storedText != reviewerEvent.Text {
		t.Fatalf("authoritative session text changed: text=%q err=%v", storedText, err)
	}
}

func TestExecutionStoreRejectsPlanningMessageFromAnotherRun(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, lead := createExecutionRecords(t, db, store)
	event, _, err := store.AppendEvent(t.Context(), execution.PendingEvent{
		ID: "sev_plan", SessionID: lead.ID, Type: worker.EventMessage,
		Text: "Plan", OccurredAt: lead.StartedAt,
	})
	if err != nil {
		t.Fatalf("append plan: %v", err)
	}
	if _, _, err := store.LinkPlanningMessage(t.Context(), execution.PendingPlanningMessage{
		RunID: "run_other", EventID: event.ID, LinkedAt: run.StartedAt,
	}); !errors.Is(err, execution.ErrPlanningMessageConflict) {
		t.Fatalf("expected cross-run conflict, got %v", err)
	}
}
