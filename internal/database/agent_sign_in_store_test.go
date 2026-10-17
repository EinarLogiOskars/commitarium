package database

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// A run may wait on both its agents' logins; either agent's sign-in finds it.
func TestARunWaitsOnEachOfItsAgentsLogins(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, _ := createExecutionRecords(t, db, store)
	at := time.Date(2026, time.October, 17, 12, 0, 0, 0, time.UTC)
	if err := store.RecordLoginWait(t.Context(), run.ID, "codex", worker.RoleLead, at); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordLoginWait(t.Context(), run.ID, "claude", worker.RoleReviewer, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, agentID := range []string{"codex", "claude"} {
		if runs, err := store.RunsAwaitingLogin(t.Context(), agentID); err != nil || len(runs) != 1 || runs[0] != run.ID {
			t.Fatalf("%s's sign-in does not find the run: %v err=%v", agentID, runs, err)
		}
	}
	if wait, err := store.LoginWaitForRun(t.Context(), run.ID); err != nil || wait.AgentID != "codex" {
		t.Fatalf("the earliest wait = %+v err=%v", wait, err)
	}
	if err := store.ClearLoginWait(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	if wait, err := store.LoginWaitForRun(t.Context(), run.ID); err != nil || wait.AgentID != "" {
		t.Fatalf("a cleared run still waits: %+v err=%v", wait, err)
	}
}

func TestAnAgentNeedsASignInUntilCleared(t *testing.T) {
	_, store := newTestExecutionStore(t)
	since := time.Date(2026, time.October, 17, 12, 0, 0, 0, time.UTC)
	if err := store.MarkAgentNeedsSignIn(t.Context(), "claude", "OAuth session expired", since); err != nil {
		t.Fatal(err)
	}
	// A repeat keeps when it began and records the latest error and try.
	if err := store.MarkAgentNeedsSignIn(t.Context(), "claude", "still expired", since.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	signIns, err := store.AgentSignIns(t.Context())
	if err != nil || len(signIns) != 1 || !signIns[0].Since.Equal(since) || signIns[0].LastError != "still expired" ||
		!signIns[0].LastTriedAt.Equal(since.Add(time.Hour)) {
		t.Fatalf("sign-ins = %+v err=%v", signIns, err)
	}
	if err := store.TouchAgentSignIn(t.Context(), "claude", since.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if signIns, _ := store.AgentSignIns(t.Context()); !signIns[0].LastTriedAt.Equal(since.Add(2 * time.Hour)) {
		t.Fatalf("the try was not recorded: %+v", signIns)
	}
	if cleared, err := store.ClearAgentSignIn(t.Context(), "claude"); err != nil || !cleared {
		t.Fatalf("clear: %t err=%v", cleared, err)
	}
	if cleared, err := store.ClearAgentSignIn(t.Context(), "claude"); err != nil || cleared {
		t.Fatalf("a second clear reported a sign-in: %t err=%v", cleared, err)
	}
}
