package orchestration

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

const expiredLogin = "Failed to authenticate: OAuth session expired"

// startSignedOutRun starts a run whose lead agent is already known to need a
// sign-in, so its failed turn waits for the sign-in at once; the next turn
// (after a sign-in or a try) completes the planning.
func startSignedOutRun(t *testing.T, runID string) *interruptedRun {
	t.Helper()
	return startInterruptedPlanningRun(t, runID, func(run *interruptedRun) {
		if err := run.executions.MarkAgentNeedsSignIn(t.Context(), "codex", expiredLogin); err != nil {
			t.Fatal(err)
		}
		run.fail(run.attempts[0], workerhttp.ErrorProviderLoginRequired, expiredLogin)
	})
}

func signInOf(t *testing.T, executions *execution.Service, agentID string) (execution.AgentSignIn, bool) {
	t.Helper()
	signIns, err := executions.AgentSignIns(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, signIn := range signIns {
		if signIn.AgentID == agentID {
			return signIn, true
		}
	}
	return execution.AgentSignIn{}, false
}

func TestAnAgentKnownToNeedASignInWaitsAtOnce(t *testing.T) {
	run := startSignedOutRun(t, "run_known_signed_out")
	waitForRunReason(t, run.executions, run.runID, "must log in again")
	if wait, _ := run.executions.RetryWaitForRun(t.Context(), run.runID); wait.RunID != "" {
		t.Fatalf("a known signed-out agent retried: %+v", wait)
	}
	if wait, err := run.executions.LoginWaitForRun(t.Context(), run.runID); err != nil || wait.AgentID != "codex" {
		t.Fatalf("login wait = %+v err=%v", wait, err)
	}
}

func TestATurnThatWorksClearsTheSignInAndResumesTheRest(t *testing.T) {
	run := startSignedOutRun(t, "run_signed_in_elsewhere")
	waitForRunReason(t, run.executions, run.runID, "must log in again")
	cleared := make(chan string, 1)
	run.starter.FollowSignIns(func(agentID string) { cleared <- agentID }, nil)
	// A turn on the same agent worked, here, in another work order, or in
	// the assistant.
	run.starter.AgentAnswered(t.Context(), "codex")
	select {
	case agentID := <-cleared:
		if agentID != "codex" {
			t.Fatalf("followers heard %q", agentID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the assistant's conversations did not hear the agent works again")
	}
	if _, needs := signInOf(t, run.executions, "codex"); needs {
		t.Fatal("a working turn did not clear the sign-in")
	}
	waitForRetriedTurn(t, run.executions, run.runID)
}

func TestTheCoordinatorTriesSignedOutWorkWhenDue(t *testing.T) {
	run := startSignedOutRun(t, "run_tried_when_due")
	waitForRunReason(t, run.executions, run.runID, "must log in again")
	tries := 0
	run.starter.FollowSignIns(nil, func(string) { tries++ })
	if tried, err := run.starter.TrySignedOutWork(t.Context(), time.Now().UTC()); err != nil || tried != 0 || tries != 0 {
		t.Fatalf("tried before it was due: %d (followers %d) err=%v", tried, tries, err)
	}
	if tried, err := run.starter.TrySignedOutWork(t.Context(), time.Now().UTC().Add(6*time.Minute)); err != nil ||
		tried != 1 || tries != 1 {
		t.Fatalf("did not try when due: %d (followers %d) err=%v", tried, tries, err)
	}
	// The login worked again: the turn completes and the sign-in clears.
	waitForRetriedTurn(t, run.executions, run.runID)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, needs := signInOf(t, run.executions, "codex"); !needs {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the turn that worked did not clear the sign-in")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAPausedRunKeepsWaitingForTheSignIn(t *testing.T) {
	run := startSignedOutRun(t, "run_paused_signed_out")
	waitForRunReason(t, run.executions, run.runID, "must log in again")
	if _, _, err := run.starter.Pause(t.Context(), run.runID, "user-pause"); err != nil {
		t.Fatal(err)
	}
	if resumed, err := run.starter.ResumeAfterLogin(t.Context(), "codex"); err != nil || resumed != 0 {
		t.Fatalf("a paused run resumed: %d err=%v", resumed, err)
	}
	if wait, err := run.executions.LoginWaitForRun(t.Context(), run.runID); err != nil || wait.AgentID != "codex" {
		t.Fatalf("the paused run lost its login wait: %+v err=%v", wait, err)
	}
}
