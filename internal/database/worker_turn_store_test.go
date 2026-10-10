package database

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestExecutionStoreBeginsImplementationContinuationAtomically(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	createWorkerAttempt(t, store, session.ID, "att_implementation_1", now)
	moveExecutionToUserWait(t, store, run, session, now)
	moveFeatureToImplementation(t, db, run.FeatureID, session.ID)

	turnAt := now.Add(time.Second)
	admission := execution.WorkerTurnAdmission{
		Command: execution.Command{
			ID: "continue-implementation", SessionID: session.ID, Type: worker.CommandMessage,
			Message: "Keep my manual error handling and finish the tests.",
			Status:  execution.CommandStatusPending, RequestedAt: turnAt,
		},
		UserEvent: execution.PendingEvent{
			ID: "continue-implementation:user-message", SessionID: session.ID,
			Type: worker.EventUserMessage,
			Text: "Keep my manual error handling and finish the tests.", OccurredAt: turnAt,
		},
		PreviousAttemptID: "att_implementation_1", PreviousLastEventSequence: 0,
		NextAttempt: execution.WorkerAttemptCheckpoint{
			SessionID: session.ID, AttemptID: "att_implementation_2",
			CreatedAt: turnAt, UpdatedAt: turnAt,
		},
		ExpectedFeatureState: feature.StateImplementing,
		RunReason:            "The lead is continuing implementation.", OccurredAt: turnAt,
	}
	result, admitted, err := store.BeginWorkerTurn(t.Context(), admission)
	if err != nil || !admitted {
		t.Fatalf("begin implementation continuation: result=%+v admitted=%t err=%v", result, admitted, err)
	}
	if result.UserEvent.Text != admission.Command.Message {
		t.Fatalf("continuation guidance was not preserved: %+v", result.UserEvent)
	}
	checkpoint, err := store.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil || checkpoint.AttemptID != admission.NextAttempt.AttemptID {
		t.Fatalf("continuation checkpoint was not installed: %+v err=%v", checkpoint, err)
	}
}

func TestExecutionStoreDoesNotPartiallyBeginWorkerTurn(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	moveFeatureToImplementation(t, db, run.FeatureID, session.ID)
	now := session.StartedAt.Add(time.Second)
	createWorkerAttempt(t, store, session.ID, "att_first", now)
	if _, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusWaitingForUser, OccurredAt: now,
	}); err != nil {
		t.Fatalf("wait session: %v", err)
	}
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser, OccurredAt: now,
	}); err != nil {
		t.Fatalf("wait run: %v", err)
	}
	admission := execution.WorkerTurnAdmission{
		Command: execution.Command{
			ID: "reply-conflict", SessionID: session.ID, Type: worker.CommandMessage,
			Message: "Continue", Status: execution.CommandStatusPending, RequestedAt: now,
		},
		UserEvent: execution.PendingEvent{
			ID: "reply-conflict:user-message", SessionID: session.ID,
			Type: worker.EventUserMessage, Text: "Continue", OccurredAt: now,
		},
		PreviousAttemptID: "att_wrong", PreviousLastEventSequence: 0,
		NextAttempt: execution.WorkerAttemptCheckpoint{
			SessionID: session.ID, AttemptID: "att_second", CreatedAt: now, UpdatedAt: now,
		},
		ExpectedFeatureState: feature.StateImplementing,
		RunReason:            "Responding", OccurredAt: now,
	}
	if _, _, err := store.BeginWorkerTurn(t.Context(), admission); !errors.Is(err, execution.ErrWorkerAttemptConflict) {
		t.Fatalf("expected fenced attempt error, got %v", err)
	}
	if _, err := store.GetCommand(t.Context(), admission.Command.ID); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("failed admission left a command behind: %v", err)
	}
	events, err := store.ListEvents(t.Context(), session.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("failed admission left an event behind: events=%+v err=%v", events, err)
	}
	checkpoint, err := store.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil || checkpoint.AttemptID != "att_first" {
		t.Fatalf("failed admission replaced checkpoint: %+v err=%v", checkpoint, err)
	}
}

// moveFeatureToImplementation takes a draft work order through Ready and a
// started run to implementation, where the lead accepts the user's replies.
func moveFeatureToImplementation(t *testing.T, db *sql.DB, featureID, sessionID string) {
	t.Helper()
	store := NewWorkflowStore(db)
	service := workflow.NewService(store)
	user := workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}
	coordinator := workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: "coordinator"}
	if _, err := service.TransitionFeature(t.Context(), featureID, feature.StateReady, user, "make-ready"); err != nil {
		t.Fatalf("make ready: %v", err)
	}
	if _, err := service.AcceptGoalAtStart(t.Context(), featureID, sessionID, "Ship CSV export.", user, "start-goal"); err != nil {
		t.Fatalf("accept goal at start: %v", err)
	}
	for _, state := range []feature.State{feature.StatePlanning, feature.StateImplementing} {
		if _, err := service.TransitionFeature(t.Context(), featureID, state, coordinator, "enter-"+string(state)); err != nil {
			t.Fatalf("enter %s: %v", state, err)
		}
	}
}
