package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func TestExecutionStoreExtendsRoundCapOncePerAction(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, _ := createExecutionRecords(t, db, store)
	if _, err := db.ExecContext(
		t.Context(), `UPDATE runs SET planning_round_limit = 3 WHERE id = ?`, run.ID,
	); err != nil {
		t.Fatalf("set planning limit: %v", err)
	}
	waiting, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status:     execution.RunStatusWaitingForUser,
		Reason:     "The planning discussion reached its round limit.",
		WaitKind:   execution.RunWaitKindRoundCap,
		OccurredAt: run.UpdatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("create round cap: %v", err)
	}

	extended, applied, err := store.ExtendPlanningRoundLimit(
		t.Context(), execution.PlanningRoundExtension{
			ID: "extend-round-1", RunID: run.ID,
			ExpectedLimit: waiting.PlanningRoundLimit, OccurredAt: waiting.UpdatedAt.Add(time.Second),
		},
	)
	if err != nil || !applied || extended.PlanningRoundLimit != 4 {
		t.Fatalf("extend round cap: run=%+v applied=%t err=%v", extended, applied, err)
	}
	retried, applied, err := store.ExtendPlanningRoundLimit(
		t.Context(), execution.PlanningRoundExtension{
			ID: "extend-round-1", RunID: run.ID,
			ExpectedLimit: waiting.PlanningRoundLimit, OccurredAt: waiting.UpdatedAt.Add(2 * time.Second),
		},
	)
	if err != nil || applied || retried.PlanningRoundLimit != 4 {
		t.Fatalf("retry round extension: run=%+v applied=%t err=%v", retried, applied, err)
	}
	if _, _, err := store.ExtendPlanningRoundLimit(
		t.Context(), execution.PlanningRoundExtension{
			ID: "extend-round-1", RunID: "run_other", ExpectedLimit: 3,
			OccurredAt: waiting.UpdatedAt.Add(2 * time.Second),
		},
	); !errors.Is(err, execution.ErrRunActionConflict) {
		t.Fatalf("expected action conflict, got %v", err)
	}
}
