package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestWorkflowStoreAcceptsGoalAtomicallyAndIdempotently(t *testing.T) {
	db, executions := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, executions)
	acceptanceTime := run.StartedAt.Add(time.Minute)
	moveExecutionToUserWait(t, executions, run, session, acceptanceTime)
	store := NewWorkflowStore(db)
	features := NewFeatureStore(db)
	acceptance := workflow.GoalAcceptance{
		EventID: "evt_goal", FeatureID: run.FeatureID, SessionID: session.ID,
		Goal:       "Export reports as CSV.\n\n- Include a header row.",
		Actor:      workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"},
		OccurredAt: acceptanceTime.Add(time.Minute), IdempotencyKey: "accept-goal-1",
	}

	event, err := store.AcceptGoal(t.Context(), acceptance)
	if err != nil {
		t.Fatalf("accept goal: %v", err)
	}
	if event.Type != workflow.EventTypeGoalAccepted || event.Sequence != 1 {
		t.Fatalf("unexpected goal event %+v", event)
	}
	payload, err := workflow.DecodeGoalAcceptedPayload(event.PayloadVersion, event.Payload)
	if err != nil {
		t.Fatalf("decode goal event: %v", err)
	}
	if payload.Goal != acceptance.Goal || payload.SessionID != session.ID {
		t.Fatalf("unexpected goal payload %+v", payload)
	}

	storedFeature, err := features.GetByID(t.Context(), run.FeatureID)
	if err != nil {
		t.Fatalf("get accepted feature: %v", err)
	}
	if storedFeature.State != feature.StateDraft || storedFeature.AcceptedGoal != acceptance.Goal ||
		storedFeature.GoalAcceptedAt == nil || !storedFeature.GoalAcceptedAt.Equal(acceptance.OccurredAt) ||
		storedFeature.UpdatedAt != acceptance.OccurredAt {
		t.Fatalf("unexpected accepted feature %+v", storedFeature)
	}

	retry := acceptance
	retry.EventID = "evt_retry_ignored"
	retry.OccurredAt = acceptance.OccurredAt.Add(time.Hour)
	retried, err := store.AcceptGoal(t.Context(), retry)
	if err != nil {
		t.Fatalf("retry goal acceptance: %v", err)
	}
	if retried != event {
		t.Fatalf("expected original event %+v, got %+v", event, retried)
	}
	events, err := store.ListEvents(t.Context(), run.FeatureID)
	if err != nil {
		t.Fatalf("list goal events: %v", err)
	}
	if len(events) != 1 || events[0] != event {
		t.Fatalf("expected one durable goal event, got %+v", events)
	}

	conflict := retry
	conflict.Goal = "A different goal."
	if _, err := store.AcceptGoal(t.Context(), conflict); !errors.Is(err, workflow.ErrIdempotencyConflict) {
		t.Fatalf("expected error %v, got %v", workflow.ErrIdempotencyConflict, err)
	}
	second := acceptance
	second.EventID = "evt_second"
	second.IdempotencyKey = "accept-goal-2"
	if _, err := store.AcceptGoal(t.Context(), second); !errors.Is(err, workflow.ErrGoalAlreadyAccepted) {
		t.Fatalf("expected error %v, got %v", workflow.ErrGoalAlreadyAccepted, err)
	}
}

func TestWorkflowStoreRejectsGoalUnlessRunAndSessionAreWaiting(t *testing.T) {
	db, executions := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, executions)
	store := NewWorkflowStore(db)
	acceptance := workflow.GoalAcceptance{
		EventID: "evt_goal", FeatureID: run.FeatureID, SessionID: session.ID,
		Goal:       "Export reports as CSV.",
		Actor:      workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"},
		OccurredAt: run.StartedAt.Add(time.Minute), IdempotencyKey: "accept-goal-1",
	}

	if _, err := store.AcceptGoal(t.Context(), acceptance); !errors.Is(err, workflow.ErrGoalAcceptanceNotAllowed) {
		t.Fatalf("expected error %v, got %v", workflow.ErrGoalAcceptanceNotAllowed, err)
	}
	storedFeature, err := NewFeatureStore(db).GetByID(t.Context(), run.FeatureID)
	if err != nil {
		t.Fatalf("get unchanged feature: %v", err)
	}
	if storedFeature.AcceptedGoal != "" || storedFeature.GoalAcceptedAt != nil {
		t.Fatalf("goal was partially stored: %+v", storedFeature)
	}
	events, err := store.ListEvents(t.Context(), run.FeatureID)
	if err != nil {
		t.Fatalf("list unchanged events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("goal event was partially stored: %+v", events)
	}
}

func moveExecutionToUserWait(
	t *testing.T,
	store *ExecutionStore,
	run execution.Run,
	session execution.Session,
	occurredAt time.Time,
) {
	t.Helper()
	if _, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusWaitingForUser, OccurredAt: occurredAt,
	}); err != nil {
		t.Fatalf("move session to user wait: %v", err)
	}
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser, OccurredAt: occurredAt,
	}); err != nil {
		t.Fatalf("move run to user wait: %v", err)
	}
}
