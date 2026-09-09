package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

func TestExecutionStoreBeginsAutonomousPlanningTurnAtomically(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	checkpoint := createWorkerAttempt(t, store, session.ID, "att_clarification", now)
	moveExecutionToUserWait(t, store, run, session, now)
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE features
		 SET state = ?, accepted_goal = ?, goal_accepted_at = ?, updated_at = ?
		 WHERE id = ?`,
		feature.StatePlanning, "Export visible columns as CSV.",
		formatExecutionTime(now), formatExecutionTime(now), run.FeatureID,
	); err != nil {
		t.Fatalf("prepare accepted planning feature: %v", err)
	}

	turnAt := now.Add(time.Second)
	admission := execution.AutonomousTurnAdmission{
		SessionID:                 session.ID,
		PreviousAttemptID:         checkpoint.AttemptID,
		PreviousLastEventSequence: checkpoint.LastEventSequence,
		NextAttempt: execution.WorkerAttemptCheckpoint{
			SessionID: session.ID, AttemptID: "att_planning",
			CreatedAt: turnAt, UpdatedAt: turnAt,
		},
		RunReason: "The lead agent is preparing a plan.", OccurredAt: turnAt,
	}
	admitted, err := store.BeginAutonomousTurn(t.Context(), admission)
	if err != nil || !admitted {
		t.Fatalf("begin autonomous turn: admitted=%t err=%v", admitted, err)
	}
	storedSession, err := store.GetSession(t.Context(), session.ID)
	if err != nil || storedSession.Status != execution.SessionStatusRunning {
		t.Fatalf("session was not activated: %+v err=%v", storedSession, err)
	}
	storedRun, err := store.GetRun(t.Context(), run.ID)
	if err != nil || storedRun.Status != execution.RunStatusRunning ||
		storedRun.Reason != admission.RunReason {
		t.Fatalf("run was not activated: %+v err=%v", storedRun, err)
	}
	storedCheckpoint, err := store.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil || storedCheckpoint.AttemptID != "att_planning" ||
		storedCheckpoint.LastEventSequence != 0 {
		t.Fatalf("attempt was not rotated: %+v err=%v", storedCheckpoint, err)
	}

	admitted, err = store.BeginAutonomousTurn(t.Context(), admission)
	if err != nil || admitted {
		t.Fatalf("exact retry was not idempotent: admitted=%t err=%v", admitted, err)
	}
}

func TestExecutionStoreRejectsAutonomousTurnOutsidePlanningWithoutChanges(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	checkpoint := createWorkerAttempt(t, store, session.ID, "att_clarification", now)
	moveExecutionToUserWait(t, store, run, session, now)

	admission := execution.AutonomousTurnAdmission{
		SessionID:                 session.ID,
		PreviousAttemptID:         checkpoint.AttemptID,
		PreviousLastEventSequence: checkpoint.LastEventSequence,
		NextAttempt: execution.WorkerAttemptCheckpoint{
			SessionID: session.ID, AttemptID: "att_planning",
			CreatedAt: now, UpdatedAt: now,
		},
		RunReason: "The lead agent is preparing a plan.", OccurredAt: now,
	}
	if _, err := store.BeginAutonomousTurn(t.Context(), admission); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("expected planning-state conflict, got %v", err)
	}
	storedCheckpoint, err := store.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil || storedCheckpoint.AttemptID != checkpoint.AttemptID {
		t.Fatalf("rejected turn changed checkpoint: %+v err=%v", storedCheckpoint, err)
	}
	storedRun, err := store.GetRun(t.Context(), run.ID)
	if err != nil || storedRun.Status != execution.RunStatusWaitingForUser {
		t.Fatalf("rejected turn changed run: %+v err=%v", storedRun, err)
	}
}
