package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestExecutionStoreBeginsAnotherWorkerTurnAtomically(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	checkpoint := createWorkerAttempt(t, store, session.ID, "att_first", now)
	if _, _, err := store.AppendWorkerEvent(t.Context(), execution.PendingWorkerEvent{
		ID: "sev_first", SessionID: session.ID, AttemptID: checkpoint.AttemptID,
		SourceSequence: 1, Type: worker.EventMessage, Text: "What should success look like?",
		OccurredAt: now, AcceptedAt: now,
	}); err != nil {
		t.Fatalf("record first turn event: %v", err)
	}
	waitAt := now.Add(time.Second)
	if _, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusWaitingForUser, OccurredAt: waitAt,
	}); err != nil {
		t.Fatalf("wait lead session: %v", err)
	}
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser, OccurredAt: waitAt,
	}); err != nil {
		t.Fatalf("wait lead run: %v", err)
	}

	turnAt := waitAt.Add(time.Second)
	admission := execution.WorkerTurnAdmission{
		Command: execution.Command{
			ID: "reply-one", SessionID: session.ID, Type: worker.CommandMessage,
			Message: "A CSV download is enough.", Status: execution.CommandStatusPending,
			RequestedAt: turnAt,
		},
		UserEvent: execution.PendingEvent{
			ID: "reply-one:user-message", SessionID: session.ID,
			Type: worker.EventUserMessage, Text: "A CSV download is enough.", OccurredAt: turnAt,
		},
		PreviousAttemptID: "att_first", PreviousLastEventSequence: 1,
		NextAttempt: execution.WorkerAttemptCheckpoint{
			SessionID: session.ID, AttemptID: "att_second", CreatedAt: turnAt, UpdatedAt: turnAt,
		},
		ExpectedFeatureState: feature.StateDraft,
		RunReason:            "The lead agent is responding.", OccurredAt: turnAt,
	}
	result, admitted, err := store.BeginWorkerTurn(t.Context(), admission)
	if err != nil {
		t.Fatalf("begin another worker turn: %v", err)
	}
	if !admitted || result.Command.Status != execution.CommandStatusPending ||
		result.UserEvent.Type != worker.EventUserMessage || result.UserEvent.Sequence != 2 {
		t.Fatalf("unexpected worker turn result %+v admitted=%t", result, admitted)
	}
	storedSession, err := store.GetSession(t.Context(), session.ID)
	if err != nil || storedSession.Status != execution.SessionStatusRunning {
		t.Fatalf("session was not activated: %+v err=%v", storedSession, err)
	}
	storedRun, err := store.GetRun(t.Context(), run.ID)
	if err != nil || storedRun.Status != execution.RunStatusRunning {
		t.Fatalf("run was not activated: %+v err=%v", storedRun, err)
	}
	storedCheckpoint, err := store.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil || storedCheckpoint.AttemptID != "att_second" ||
		storedCheckpoint.LastEventSequence != 0 {
		t.Fatalf("worker checkpoint was not replaced: %+v err=%v", storedCheckpoint, err)
	}

	retried, admitted, err := store.BeginWorkerTurn(t.Context(), admission)
	if err != nil {
		t.Fatalf("retry worker turn admission: %v", err)
	}
	if admitted || retried.Command.ID != admission.Command.ID {
		t.Fatalf("retry was not idempotent: %+v admitted=%t", retried, admitted)
	}
	events, err := store.ListEvents(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list conversation: %v", err)
	}
	if len(events) != 2 || events[1].Text != admission.Command.Message {
		t.Fatalf("user reply was not recorded exactly once: %+v", events)
	}
}

func TestExecutionStoreRejectsReplyAfterGoalAcceptance(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	createWorkerAttempt(t, store, session.ID, "att_first", now)
	moveExecutionToUserWait(t, store, run, session, now)
	if _, err := NewWorkflowStore(db).AcceptGoal(t.Context(), workflow.GoalAcceptance{
		EventID: "evt_goal", FeatureID: run.FeatureID, SessionID: session.ID,
		Goal: "Ship CSV export.", Actor: workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"},
		OccurredAt: now.Add(time.Second), IdempotencyKey: "accept-1",
	}); err != nil {
		t.Fatalf("accept goal: %v", err)
	}

	turnAt := now.Add(2 * time.Second)
	admission := execution.WorkerTurnAdmission{
		Command: execution.Command{
			ID: "reply-too-late", SessionID: session.ID, Type: worker.CommandMessage,
			Message: "Change the scope.", Status: execution.CommandStatusPending,
			RequestedAt: turnAt,
		},
		UserEvent: execution.PendingEvent{
			ID: "reply-too-late:user-message", SessionID: session.ID,
			Type: worker.EventUserMessage, Text: "Change the scope.", OccurredAt: turnAt,
		},
		PreviousAttemptID: "att_first", PreviousLastEventSequence: 0,
		NextAttempt: execution.WorkerAttemptCheckpoint{
			SessionID: session.ID, AttemptID: "att_second", CreatedAt: turnAt, UpdatedAt: turnAt,
		},
		ExpectedFeatureState: feature.StateDraft,
		RunReason:            "The lead agent is responding.", OccurredAt: turnAt,
	}
	if _, _, err := store.BeginWorkerTurn(t.Context(), admission); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("expected error %v, got %v", execution.ErrStateConflict, err)
	}
	if _, err := store.GetCommand(t.Context(), admission.Command.ID); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("rejected reply left a command behind: %v", err)
	}
}

func TestExecutionStoreBeginsImplementationContinuationAtomically(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	createWorkerAttempt(t, store, session.ID, "att_implementation_1", now)
	moveExecutionToUserWait(t, store, run, session, now)
	workflowService := workflow.NewService(NewWorkflowStore(db))
	actor := workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}
	if _, err := workflowService.AcceptGoal(
		t.Context(), run.FeatureID, session.ID, "Ship CSV export.", actor, "accept-implementation-goal",
	); err != nil {
		t.Fatalf("accept goal: %v", err)
	}
	coordinator := workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: "coordinator"}
	if _, err := workflowService.TransitionFeature(
		t.Context(), run.FeatureID, feature.StatePlanning, coordinator, "enter-planning",
	); err != nil {
		t.Fatalf("enter planning: %v", err)
	}
	if _, err := workflowService.TransitionFeature(
		t.Context(), run.FeatureID, feature.StateImplementing, coordinator, "enter-implementation",
	); err != nil {
		t.Fatalf("enter implementation: %v", err)
	}

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
		ExpectedFeatureState: feature.StateDraft,
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
