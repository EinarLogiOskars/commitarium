package database

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// A run keeps when it blocked while later writes (a model change) move its
// updated_at on, and a new blocker starts it again.
func TestExecutionStoreKeepsWhenARunBlocked(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, _ := createExecutionRecords(t, db, store)
	since := func() time.Time {
		t.Helper()
		at, err := store.RunStateSince(t.Context(), run.ID)
		if err != nil {
			t.Fatalf("state since: %v", err)
		}
		return at
	}
	if got := since(); !got.Equal(run.UpdatedAt) {
		t.Fatalf("new run since %v, want %v", got, run.UpdatedAt)
	}

	blocked := time.Date(2026, time.October, 13, 12, 0, 0, 0, time.UTC)
	transition := func(expected, status execution.RunStatus, reason string, at time.Time) {
		t.Helper()
		if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
			RunID: run.ID, Expected: expected, Status: status, Reason: reason,
			WaitKind: execution.RunWaitKindBlocker, OccurredAt: at,
		}); err != nil {
			t.Fatalf("transition to %s: %v", status, err)
		}
	}
	transition(execution.RunStatusRunning, execution.RunStatusWaitingForUser, "The lead ended early.", blocked)
	if _, err := store.ChangeRunModels(t.Context(), run.ID,
		project.AgentModels{Lead: "other-model", Reviewer: "other-model"}, nil, nil, blocked.Add(5*time.Minute),
	); err != nil {
		t.Fatalf("change models: %v", err)
	}
	if got := since(); !got.Equal(blocked) {
		t.Fatalf("blocked since %v after a model change, want %v", got, blocked)
	}
	// The same blocker recorded again keeps its time; a new one starts anew.
	transition(execution.RunStatusWaitingForUser, execution.RunStatusWaitingForUser, "The lead ended early.", blocked.Add(6*time.Minute))
	if got := since(); !got.Equal(blocked) {
		t.Fatalf("blocked since %v after the same blocker, want %v", got, blocked)
	}
	reblocked := blocked.Add(20 * time.Minute)
	transition(execution.RunStatusWaitingForUser, execution.RunStatusWaitingForUser, "Tests failed.", reblocked)
	if got := since(); !got.Equal(reblocked) {
		t.Fatalf("blocked since %v after a new blocker, want %v", got, reblocked)
	}
}
