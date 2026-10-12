package orchestration

import (
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestAUsageLimitPausesTheRunAndItContinuesWhenDue(t *testing.T) {
	runID := "run_usage_limit"
	starter, executions, stub := startFailingPlanningRun(t, runID, workerhttp.ErrorUsageLimit)
	paused, err := executions.GetRun(t.Context(), runID)
	if err != nil || paused.WaitKind != execution.RunWaitKindBlocker || !strings.Contains(paused.Reason, "reached its usage limit") {
		t.Fatalf("a usage limit did not pause the run: %+v err=%v", paused, err)
	}
	wait, err := executions.LimitWaitForRun(t.Context(), runID)
	if err != nil || wait.AgentID != "codex" {
		t.Fatalf("limit wait = %+v err=%v", wait, err)
	}

	if resumed, err := starter.ResumeDueLimits(t.Context(), time.Now().UTC()); err != nil || resumed != 0 {
		t.Fatalf("a wait resumed before it was due: %d err=%v", resumed, err)
	}
	if resumed, err := starter.ResumeDueLimits(t.Context(), time.Now().UTC().Add(limitRetryInterval+time.Minute)); err != nil || resumed != 1 {
		t.Fatalf("resume when due: %d err=%v", resumed, err)
	}
	waitForRetriedTurn(t, executions, runID)
	if wait, _ := executions.LimitWaitForRun(t.Context(), runID); wait.AgentID != "" {
		t.Fatalf("the limit wait outlived the resumed run: %+v", wait)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if retry := stub.putRequests[len(stub.putRequests)-1]; retry.Mode != workerhttp.AttemptModeResume ||
		retry.ProviderSessionID != "codex-thread-test" {
		t.Fatalf("the turn did not continue in its conversation: %+v", retry)
	}
}

func TestALimitWaitWithAResetTimeIsDueAfterIt(t *testing.T) {
	resetsAt := time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)
	wait := execution.LimitWait{ResetsAt: &resetsAt, CreatedAt: resetsAt.Add(-time.Hour)}
	if limitWaitDue(wait, resetsAt) {
		t.Fatal("due before the reset's grace minute")
	}
	if !limitWaitDue(wait, resetsAt.Add(time.Minute)) {
		t.Fatal("not due after the reset")
	}
}
