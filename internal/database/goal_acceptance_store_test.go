package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestWorkflowStoreAcceptsGoalAtStartOnlyForReadyFeatures(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	acceptance := workflow.GoalAcceptance{
		EventID: "evt_goal", FeatureID: created.ID, SessionID: "run_start:lead",
		Goal:       "Export reports as CSV.\n\nAreas to touch:\n- report export",
		Actor:      workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"},
		OccurredAt: created.CreatedAt.Add(time.Hour), IdempotencyKey: "run_start:start-goal",
	}
	if _, err := store.AcceptGoalAtStart(t.Context(), acceptance); !errors.Is(err, workflow.ErrGoalAcceptanceNotAllowed) {
		t.Fatalf("draft goal acceptance error = %v", err)
	}
	if _, err := workflow.NewService(store).TransitionFeature(
		t.Context(), created.ID, feature.StateReady,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, "make-ready",
	); err != nil {
		t.Fatalf("make ready: %v", err)
	}

	event, err := store.AcceptGoalAtStart(t.Context(), acceptance)
	if err != nil || event.Type != workflow.EventTypeGoalAccepted {
		t.Fatalf("accept goal at start: event=%+v err=%v", event, err)
	}
	stored, err := features.GetByID(t.Context(), created.ID)
	if err != nil || stored.AcceptedGoal != acceptance.Goal || stored.GoalAcceptedAt == nil ||
		stored.State != feature.StateReady {
		t.Fatalf("stored feature=%+v err=%v", stored, err)
	}
	retry := acceptance
	retry.EventID = "evt_goal_retry"
	if retried, err := store.AcceptGoalAtStart(t.Context(), retry); err != nil || retried.ID != event.ID {
		t.Fatalf("retried acceptance=%+v err=%v", retried, err)
	}
	conflict := retry
	conflict.Goal = "Something else."
	if _, err := store.AcceptGoalAtStart(t.Context(), conflict); !errors.Is(err, workflow.ErrIdempotencyConflict) {
		t.Fatalf("changed goal under the same key error = %v", err)
	}
	second := acceptance
	second.EventID, second.IdempotencyKey = "evt_goal_second", "run_other:start-goal"
	if _, err := store.AcceptGoalAtStart(t.Context(), second); !errors.Is(err, workflow.ErrGoalAcceptanceNotAllowed) {
		t.Fatalf("second acceptance error = %v", err)
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
