package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// restartingRemoteLeadWorker is a worker that can be down (every call is
// refused, as when it restarts) and that fences indeterminate attempts.
type restartingRemoteLeadWorker struct {
	*conversationalRemoteLeadWorker
	downMu sync.Mutex
	down   bool
}

func (stub *restartingRemoteLeadWorker) setDown(down bool) {
	stub.downMu.Lock()
	defer stub.downMu.Unlock()
	stub.down = down
}

func (stub *restartingRemoteLeadWorker) refused() error {
	stub.downMu.Lock()
	defer stub.downMu.Unlock()
	if stub.down {
		return fmt.Errorf("%w: dial tcp 127.0.0.1:8081: connect: connection refused", workerhttp.ErrRequestFailed)
	}
	return nil
}

func (stub *restartingRemoteLeadWorker) Reachable(context.Context, string) error {
	return stub.refused()
}

func (stub *restartingRemoteLeadWorker) PutAttempt(
	ctx context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	if err := stub.refused(); err != nil {
		return workerhttp.Attempt{}, false, err
	}
	return stub.conversationalRemoteLeadWorker.PutAttempt(ctx, identity, request)
}

func (stub *restartingRemoteLeadWorker) GetAttempt(
	ctx context.Context, reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	if err := stub.refused(); err != nil {
		return workerhttp.Attempt{}, err
	}
	stub.mu.Lock()
	unsent := stub.unsent[reference]
	stub.mu.Unlock()
	if unsent {
		return workerhttp.Attempt{}, &workerhttp.RemoteError{
			StatusCode: 404, ProtocolError: workerhttp.ProtocolError{Code: workerhttp.ErrorNotFound, Message: "attempt not found"},
		}
	}
	return stub.conversationalRemoteLeadWorker.GetAttempt(ctx, reference)
}

func (stub *restartingRemoteLeadWorker) Supersede(
	_ context.Context, identity workerhttp.MutationIdentity, _ workerhttp.SupersedeRequest,
) (workerhttp.Attempt, error) {
	if err := stub.refused(); err != nil {
		return workerhttp.Attempt{}, err
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	closed := stub.terminal[identity.AttemptReference]
	closed.State = workerhttp.AttemptStateTerminal
	closed.Result = &workerhttp.TerminalResult{Outcome: workerhttp.OutcomeStopped, Summary: "Superseded."}
	return closed, nil
}

type interruptedRun struct {
	runID      string
	executions *execution.Service
	stub       *conversationalRemoteLeadWorker
	worker     *restartingRemoteLeadWorker
	config     RemoteLeadConfig
	starter    *RemoteLeadStarter
	// attempts are the planning turn and its recovery successors, in order.
	attempts []workerhttp.AttemptReference
}

// interrupt makes an attempt end as a worker restart leaves it.
func (run *interruptedRun) interrupt(reference workerhttp.AttemptReference) {
	run.stub.mu.Lock()
	defer run.stub.mu.Unlock()
	attempt := run.stub.terminal[reference]
	attempt.State = workerhttp.AttemptStateIndeterminate
	attempt.Result, attempt.EndedAt = nil, nil
	run.stub.terminal[reference] = attempt
}

// fail makes an attempt end with a provider failure.
func (run *interruptedRun) fail(reference workerhttp.AttemptReference, code workerhttp.ErrorCode, message string) {
	run.stub.mu.Lock()
	defer run.stub.mu.Unlock()
	attempt := run.stub.terminal[reference]
	attempt.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeFailed, Summary: message,
		Error: &workerhttp.ProtocolError{Code: code, Message: message},
	}
	run.stub.terminal[reference] = attempt
}

// startInterruptedPlanningRun starts a run whose planning turn (and its
// recovery successors) script sets up to fail; a successor left alone
// completes.
func startInterruptedPlanningRun(
	t *testing.T, runID string, script func(*interruptedRun),
) *interruptedRun {
	t.Helper()
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	leadID := remoteLeadSessionID(runID)
	run := &interruptedRun{
		runID: runID, executions: executions, stub: stub,
		worker:   &restartingRemoteLeadWorker{conversationalRemoteLeadWorker: stub},
		attempts: []workerhttp.AttemptReference{{SessionID: leadID, AttemptID: planningAttemptID(leadID)}},
	}
	for range len(retryBackoff) {
		successorID, err := recoverySuccessorAttemptID(leadID, run.attempts[len(run.attempts)-1].AttemptID)
		if err != nil {
			t.Fatalf("successor identity: %v", err)
		}
		successor := workerhttp.AttemptReference{SessionID: leadID, AttemptID: successorID}
		copyScriptedAttempt(stub, run.attempts[0], successor)
		run.attempts = append(run.attempts, successor)
	}
	script(run)

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.September, 9, 22, 0, 0, 0, time.UTC)
	run.config = RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Worker: run.worker,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
		Workspaces: &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
			ID: "wsp_managed_feature", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
			RepositoryOwner: "commitarium", RepositoryName: "planning-test",
			BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
			BaseCommitID: "0123456789abcdef0123456789abcdef01234567",
			Status:       workspace.StatusBranchReady, CheckoutRelativePath: "wsp_managed_feature",
			CheckoutCreatedAt: &checkoutAt,
		}},
	}
	starter, err := NewRemoteLeadStarter(run.config)
	if err != nil {
		t.Fatalf("create remote starter: %v", err)
	}
	run.starter = starter
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders, storedProject.MergePolicy,
	); err != nil {
		t.Fatalf("start lead: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	return run
}

func waitForRunReason(t *testing.T, executions *execution.Service, runID, want string) execution.Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		run, err := executions.GetRun(t.Context(), runID)
		if err == nil && run.Status == execution.RunStatusWaitingForUser && strings.Contains(run.Reason, want) {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run reason does not say %q: %+v err=%v", want, run, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// later is past every retry's backoff.
func later() time.Time { return time.Now().UTC().Add(time.Hour) }

func TestAnInterruptedTurnRetriesInTheSameConversation(t *testing.T) {
	run := startInterruptedPlanningRun(t, "run_interrupted_turn", func(run *interruptedRun) {
		run.interrupt(run.attempts[0])
	})
	paused := waitForRunReason(t, run.executions, run.runID, "interrupted (the worker restarted)")
	if paused.WaitKind != execution.RunWaitKindBlocker || !strings.Contains(paused.Reason, "(1 of 3)") {
		t.Fatalf("an interrupted turn did not wait for its retry: %+v", paused)
	}
	wait, err := run.executions.RetryWaitForRun(t.Context(), run.runID)
	if err != nil || wait.Retries != 0 || wait.DueAt == nil || wait.AttemptID != run.attempts[0].AttemptID {
		t.Fatalf("retry wait = %+v err=%v", wait, err)
	}
	if resumed, err := run.starter.ResumeDueRetries(t.Context(), time.Now().UTC()); err != nil || resumed != 0 {
		t.Fatalf("a retry started before it was due: %d err=%v", resumed, err)
	}
	if resumed, err := run.starter.ResumeDueRetries(t.Context(), later()); err != nil || resumed != 1 {
		t.Fatalf("resume a due retry: %d err=%v", resumed, err)
	}
	waitForRetriedTurn(t, run.executions, run.runID)
	if wait, _ := run.executions.RetryWaitForRun(t.Context(), run.runID); wait.RunID != "" {
		t.Fatalf("the retry wait outlived the completed turn: %+v", wait)
	}
	run.stub.mu.Lock()
	retry := run.stub.putRequests[len(run.stub.putRequests)-1]
	run.stub.mu.Unlock()
	if retry.Mode != workerhttp.AttemptModeResume || retry.ProviderSessionID != "codex-thread-test" ||
		!strings.Contains(retry.Instructions, "interrupted (the worker restarted)") ||
		!strings.Contains(retry.Instructions, "Never redo or republish") {
		t.Fatalf("the retry did not resume the conversation with the interruption: %+v", retry)
	}
	events, err := run.executions.EventsForSession(t.Context(), remoteLeadSessionID(run.runID))
	if err != nil {
		t.Fatal(err)
	}
	narrated := false
	for _, event := range events {
		narrated = narrated || strings.Contains(event.Text, "Retrying automatically in 30 seconds (1 of 3)")
	}
	if !narrated {
		t.Fatal("the transcript does not say the turn is retried")
	}
}

func TestATurnTheWorkerRefusedIsSentAgainOnceItIsBack(t *testing.T) {
	run := startInterruptedPlanningRun(t, "run_worker_refused", func(run *interruptedRun) {
		run.stub.unsent = map[workerhttp.AttemptReference]bool{run.attempts[0]: true}
		run.worker.setDown(true)
	})
	waitForRunReason(t, run.executions, run.runID, "interrupted (the connection to the worker was lost)")
	// While the worker is down the retry waits, without spending itself.
	if resumed, err := run.starter.ResumeDueRetries(t.Context(), later()); err != nil || resumed != 0 {
		t.Fatalf("a retry started while the worker was down: %d err=%v", resumed, err)
	}
	if wait, _ := run.executions.RetryWaitForRun(t.Context(), run.runID); wait.Retries != 0 || wait.DueAt == nil {
		t.Fatalf("waiting for the worker spent a retry: %+v", wait)
	}
	run.worker.setDown(false)
	if resumed, err := run.starter.ResumeDueRetries(t.Context(), later()); err != nil || resumed != 1 {
		t.Fatalf("resume once the worker is back: %d err=%v", resumed, err)
	}
	waitForRetriedTurn(t, run.executions, run.runID)
	run.stub.mu.Lock()
	defer run.stub.mu.Unlock()
	if len(run.stub.putRequests) != 1 || run.stub.putRequests[0].Mode != workerhttp.AttemptModeStart ||
		strings.Contains(run.stub.putRequests[0].Instructions, "Recovery continuation") {
		t.Fatalf("the refused turn was not sent again as it was: %+v", run.stub.putRequests)
	}
}

func TestRepeatedInterruptionsBlockAfterTheRetries(t *testing.T) {
	run := startInterruptedPlanningRun(t, "run_interrupted_again", func(run *interruptedRun) {
		for _, attempt := range run.attempts {
			run.interrupt(attempt)
		}
	})
	waitForRunReason(t, run.executions, run.runID, "(1 of 3)")
	for retry := 1; retry <= len(retryBackoff); retry++ {
		if resumed, err := run.starter.ResumeDueRetries(t.Context(), later()); err != nil || resumed != 1 {
			t.Fatalf("retry %d: %d err=%v", retry, resumed, err)
		}
		if retry < len(retryBackoff) {
			waitForRunReason(t, run.executions, run.runID, fmt.Sprintf("(%d of 3)", retry+1))
		}
	}
	blocked := waitForRunReason(t, run.executions, run.runID, "after 3 automatic retries")
	if blocked.WaitKind != execution.RunWaitKindBlocker || !strings.Contains(blocked.Reason, "What failed:") {
		t.Fatalf("spent retries did not block as before: %+v", blocked)
	}
	if wait, _ := run.executions.RetryWaitForRun(t.Context(), run.runID); wait.RunID != "" {
		t.Fatalf("a blocked turn kept its retry wait: %+v", wait)
	}
	if resumed, err := run.starter.ResumeDueRetries(t.Context(), later()); err != nil || resumed != 0 {
		t.Fatalf("a blocked turn retried again: %d err=%v", resumed, err)
	}
}

func TestOnlyInfrastructureFailuresRetry(t *testing.T) {
	for _, tc := range []struct {
		code    workerhttp.ErrorCode
		message string
		retries bool
	}{
		{workerhttp.ErrorInternal, `API Error: 529 {"type":"overloaded_error"}`, true},
		{workerhttp.ErrorInternal, "The tests still fail after the change.", false},
		{workerhttp.ErrorUsageLimit, "You've hit your usage limit.", false},
		// A login failure may pass on the next turn, so it retries before asking for a sign-in.
		{workerhttp.ErrorProviderLoginRequired, "Failed to authenticate: OAuth session expired", true},
	} {
		run := startInterruptedPlanningRun(t, "run_failure_"+string(tc.code), func(run *interruptedRun) {
			run.fail(run.attempts[0], tc.code, tc.message)
		})
		paused, err := run.executions.GetRun(t.Context(), run.runID)
		wait, waitErr := run.executions.RetryWaitForRun(t.Context(), run.runID)
		if err != nil || waitErr != nil || paused.WaitKind != execution.RunWaitKindBlocker ||
			strings.Contains(paused.Reason, "Retrying automatically") != tc.retries || (wait.RunID != "") != tc.retries {
			t.Fatalf("%s %q: retries=%t, run=%+v wait=%+v err=%v/%v", tc.code, tc.message, tc.retries, paused, wait, err, waitErr)
		}
	}
}

func TestACoordinatorRestartContinuesTheRetrySchedule(t *testing.T) {
	run := startInterruptedPlanningRun(t, "run_retry_restart", func(run *interruptedRun) {
		run.interrupt(run.attempts[0])
	})
	waitForRunReason(t, run.executions, run.runID, "(1 of 3)")
	restarted, err := NewRemoteLeadStarter(run.config)
	if err != nil {
		t.Fatal(err)
	}
	if resumed, err := restarted.ResumeDueRetries(t.Context(), later()); err != nil || resumed != 1 {
		t.Fatalf("the restarted coordinator did not continue the schedule: %d err=%v", resumed, err)
	}
	// Neither coordinator fires the same retry twice.
	for _, starter := range []*RemoteLeadStarter{run.starter, restarted} {
		if resumed, err := starter.ResumeDueRetries(t.Context(), later()); err != nil || resumed != 0 {
			t.Fatalf("a retry fired twice: %d err=%v", resumed, err)
		}
	}
	waitForRetriedTurn(t, run.executions, run.runID)
	run.stub.mu.Lock()
	defer run.stub.mu.Unlock()
	if len(run.stub.putRequests) != 2 {
		t.Fatalf("expected the planning turn and one retry, got %d turns", len(run.stub.putRequests))
	}
}

func TestTheUsersContinueStartsRetriesAfresh(t *testing.T) {
	run := startInterruptedPlanningRun(t, "run_retry_user_continue", func(run *interruptedRun) {
		run.interrupt(run.attempts[0])
	})
	waitForRunReason(t, run.executions, run.runID, "(1 of 3)")
	if _, admitted, err := run.starter.RecoverBlockerWithDirective(
		t.Context(), run.runID, "user-continue", RecoveryDirective{Action: RecoveryActionContinue},
	); err != nil || !admitted {
		t.Fatalf("continue: admitted=%t err=%v", admitted, err)
	}
	waitForRetriedTurn(t, run.executions, run.runID)
	if wait, _ := run.executions.RetryWaitForRun(t.Context(), run.runID); wait.RunID != "" {
		t.Fatalf("the user's continue kept the retry wait: %+v", wait)
	}
}

// A planning turn that ended without its structured result is the agent's to
// put right: it is not retried, but handed back to the lead in its own
// conversation, and planning goes on (self_correction.go).
func TestAnIncompletePlanningResultIsHandedBackNotRetried(t *testing.T) {
	run := startInterruptedPlanningRun(t, "run_failure_incomplete_planning", func(run *interruptedRun) {
		run.fail(run.attempts[0], workerhttp.ErrorIncompleteResult, "the turn ended without its structured result")
	})
	waitForRunReason(t, run.executions, run.runID, "The lead planning proposal is ready for reviewer consultation.")
	if wait, _ := run.executions.RetryWaitForRun(t.Context(), run.runID); wait.RunID != "" {
		t.Fatalf("an incomplete result was retried: %+v", wait)
	}
	recoveries, err := run.executions.RunRecoveries(t.Context(), run.runID)
	if err != nil || len(recoveries) != 1 || recoveries[0].How != execution.RecoverySelfCorrection ||
		!strings.Contains(recoveries[0].Guidance, "the turn ended without its required structured result") {
		t.Fatalf("the incomplete result was not handed back: %+v err=%v", recoveries, err)
	}
	run.stub.mu.Lock()
	defer run.stub.mu.Unlock()
	last := run.stub.putRequests[len(run.stub.putRequests)-1]
	if last.Mode != workerhttp.AttemptModeResume || !strings.Contains(last.Instructions, "Commitarium's correction:") {
		t.Fatalf("the correction did not continue the lead's conversation: %+v", last)
	}
}
