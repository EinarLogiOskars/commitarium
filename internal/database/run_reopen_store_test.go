package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func TestExecutionStoreReopensAFailedRunAtItsLastTurn(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	at := run.StartedAt.Add(time.Minute)
	if _, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: session.Status, Status: execution.SessionStatusFailed, OccurredAt: at,
	}); err != nil {
		t.Fatalf("fail session: %v", err)
	}
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: run.Status, Status: execution.RunStatusFailed,
		Reason: "The reviewer could not complete this turn.", OccurredAt: at,
	}); err != nil {
		t.Fatalf("fail run: %v", err)
	}

	reopened, err := store.ReopenFailedRun(t.Context(), run.ID, "Reopened.", at.Add(time.Minute))
	if err != nil || reopened.Status != execution.RunStatusWaitingForUser ||
		reopened.WaitKind != execution.RunWaitKindBlocker || reopened.EndedAt != nil {
		t.Fatalf("reopened run = %+v err=%v", reopened, err)
	}
	active, err := store.ListActiveSessions(t.Context(), run.ID)
	if err != nil || len(active) != 1 || active[0].ID != session.ID ||
		active[0].Status != execution.SessionStatusPauseRequested {
		t.Fatalf("failed session was not reactivated: %+v err=%v", active, err)
	}
	again, err := store.ReopenFailedRun(t.Context(), run.ID, "Reopened again.", at.Add(2*time.Minute))
	if err != nil || again.Reason != "Reopened." {
		t.Fatalf("second reopen changed the run: %+v err=%v", again, err)
	}
	if _, err := store.ReopenFailedRun(t.Context(), "run_missing", "x", at); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("missing run error = %v", err)
	}
}
