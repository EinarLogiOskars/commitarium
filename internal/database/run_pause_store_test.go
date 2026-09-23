package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func TestExecutionStorePausesAndResumesExactWaitingCheckpointIdempotently(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, _ := createExecutionRecords(t, db, store)
	waitingAt := run.UpdatedAt.Add(time.Second)
	waiting, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status:     execution.RunStatusWaitingForUser,
		Reason:     "The planning proposal is ready.",
		WaitKind:   execution.RunWaitKindPhaseCheckpoint,
		OccurredAt: waitingAt,
	})
	if err != nil {
		t.Fatalf("create phase checkpoint: %v", err)
	}

	paused, applied, err := store.ApplyRunPause(t.Context(), execution.RunPauseMutation{
		ID: "pause-one", RunID: run.ID, Action: execution.RunPauseActionPause,
		OccurredAt: waitingAt.Add(time.Second),
	})
	if err != nil || !applied {
		t.Fatalf("pause checkpoint: run=%+v applied=%t err=%v", paused, applied, err)
	}
	if !paused.Paused || paused.WaitKind != execution.RunWaitKindPaused ||
		paused.PausedFromWaitKind != waiting.WaitKind {
		t.Fatalf("pause did not retain checkpoint: %+v", paused)
	}
	retried, applied, err := store.ApplyRunPause(t.Context(), execution.RunPauseMutation{
		ID: "pause-one", RunID: run.ID, Action: execution.RunPauseActionPause,
		OccurredAt: waitingAt.Add(10 * time.Second),
	})
	if err != nil || applied || retried != paused {
		t.Fatalf("pause retry changed state: run=%+v applied=%t err=%v", retried, applied, err)
	}

	resumed, applied, err := store.ApplyRunPause(t.Context(), execution.RunPauseMutation{
		ID: "resume-one", RunID: run.ID, Action: execution.RunPauseActionResume,
		OccurredAt: waitingAt.Add(2 * time.Second),
	})
	if err != nil || !applied {
		t.Fatalf("resume checkpoint: run=%+v applied=%t err=%v", resumed, applied, err)
	}
	if resumed.Paused || resumed.WaitKind != execution.RunWaitKindPhaseCheckpoint ||
		resumed.PausedFromWaitKind != "" {
		t.Fatalf("resume did not restore exact checkpoint: %+v", resumed)
	}
	reclassified, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusWaitingForUser,
		Status:     execution.RunStatusWaitingForUser,
		Reason:     "Both agents approved; user merge approval is required.",
		WaitKind:   execution.RunWaitKindMergeGate,
		OccurredAt: waitingAt.Add(3 * time.Second),
	})
	if err != nil || reclassified.WaitKind != execution.RunWaitKindMergeGate {
		t.Fatalf("reclassify automatic checkpoint as merge gate: run=%+v err=%v", reclassified, err)
	}
	if _, _, err := store.ApplyRunPause(t.Context(), execution.RunPauseMutation{
		ID: "pause-one", RunID: run.ID, Action: execution.RunPauseActionResume,
		OccurredAt: waitingAt.Add(4 * time.Second),
	}); !errors.Is(err, execution.ErrRunActionConflict) {
		t.Fatalf("expected cross-action idempotency conflict, got %v", err)
	}
}

func TestExecutionStoreArmsPauseDuringRunningTurnAndKeepsNextBoundary(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, _ := createExecutionRecords(t, db, store)
	paused, _, err := store.ApplyRunPause(t.Context(), execution.RunPauseMutation{
		ID: "pause-running", RunID: run.ID, Action: execution.RunPauseActionPause,
		OccurredAt: run.UpdatedAt.Add(time.Second),
	})
	if err != nil || !paused.Paused || paused.Status != execution.RunStatusRunning ||
		paused.WaitKind != execution.RunWaitKindPaused {
		t.Fatalf("arm running pause: run=%+v err=%v", paused, err)
	}

	waiting, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status:     execution.RunStatusWaitingForUser,
		Reason:     "Implementation is ready for review.",
		WaitKind:   execution.RunWaitKindPhaseCheckpoint,
		OccurredAt: run.UpdatedAt.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("reach paused boundary: %v", err)
	}
	if !waiting.Paused || waiting.WaitKind != execution.RunWaitKindPaused ||
		waiting.PausedFromWaitKind != execution.RunWaitKindPhaseCheckpoint {
		t.Fatalf("next boundary was not retained: %+v", waiting)
	}
}
