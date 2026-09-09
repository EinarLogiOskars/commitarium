package database

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestExecutionStoreBeginsNewReviewerSessionAtomically(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, lead := createExecutionRecords(t, db, store)
	now := lead.StartedAt.Add(time.Second)
	moveExecutionToUserWait(t, store, run, lead, now)
	preparePlanningFeature(t, db, run.FeatureID, now)

	reviewer := execution.Session{
		ID: "ses_reviewer", RunID: run.ID, AgentID: "codex-reviewer",
		Role: worker.RoleReviewer, Status: execution.SessionStatusStarting,
		StartedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}
	admission := execution.NewSessionTurnAdmission{
		Session: reviewer,
		Attempt: execution.WorkerAttemptCheckpoint{
			SessionID: reviewer.ID, AttemptID: "att_reviewer_planning",
			CreatedAt: reviewer.StartedAt, UpdatedAt: reviewer.StartedAt,
		},
		RunReason: "The reviewer is inspecting the plan.", OccurredAt: reviewer.StartedAt,
	}
	admitted, err := store.BeginNewSessionTurn(t.Context(), admission)
	if err != nil || !admitted {
		t.Fatalf("begin reviewer session: admitted=%t err=%v", admitted, err)
	}
	storedReviewer, err := store.GetSession(t.Context(), reviewer.ID)
	if err != nil || storedReviewer != reviewer {
		t.Fatalf("unexpected reviewer %+v err=%v", storedReviewer, err)
	}
	checkpoint, err := store.GetWorkerAttempt(t.Context(), reviewer.ID)
	if err != nil || checkpoint.AttemptID != admission.Attempt.AttemptID {
		t.Fatalf("unexpected reviewer checkpoint %+v err=%v", checkpoint, err)
	}
	storedRun, err := store.GetRun(t.Context(), run.ID)
	if err != nil || storedRun.Status != execution.RunStatusRunning ||
		storedRun.Reason != admission.RunReason {
		t.Fatalf("reviewer did not activate run: %+v err=%v", storedRun, err)
	}

	admitted, err = store.BeginNewSessionTurn(t.Context(), admission)
	if err != nil || admitted {
		t.Fatalf("exact reviewer retry was not idempotent: admitted=%t err=%v", admitted, err)
	}
}

func TestExecutionStoreDoesNotPartiallyCreateReviewerOutsidePlanning(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, lead := createExecutionRecords(t, db, store)
	now := lead.StartedAt.Add(time.Second)
	moveExecutionToUserWait(t, store, run, lead, now)
	reviewer := execution.Session{
		ID: "ses_reviewer", RunID: run.ID, AgentID: "codex-reviewer",
		Role: worker.RoleReviewer, Status: execution.SessionStatusStarting,
		StartedAt: now, UpdatedAt: now,
	}
	_, err := store.BeginNewSessionTurn(t.Context(), execution.NewSessionTurnAdmission{
		Session: reviewer,
		Attempt: execution.WorkerAttemptCheckpoint{
			SessionID: reviewer.ID, AttemptID: "att_reviewer_planning",
			CreatedAt: now, UpdatedAt: now,
		},
		RunReason: "The reviewer is inspecting the plan.", OccurredAt: now,
	})
	if !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("expected planning-state conflict, got %v", err)
	}
	if _, err := store.GetSession(t.Context(), reviewer.ID); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("rejected admission created reviewer: %v", err)
	}
	storedRun, err := store.GetRun(t.Context(), run.ID)
	if err != nil || storedRun.Status != execution.RunStatusWaitingForUser {
		t.Fatalf("rejected admission changed run: %+v err=%v", storedRun, err)
	}
}

func preparePlanningFeature(t *testing.T, db *sql.DB, featureID string, now time.Time) {
	t.Helper()
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE features SET state = ?, accepted_goal = ?, goal_accepted_at = ?, updated_at = ?
		 WHERE id = ?`,
		feature.StatePlanning, "Accepted goal", formatExecutionTime(now),
		formatExecutionTime(now), featureID,
	); err != nil {
		t.Fatalf("prepare planning feature: %v", err)
	}
}
