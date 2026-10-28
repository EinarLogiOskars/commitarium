package orchestration

import (
	"strings"
	"sync"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

const packageRejection = "The user declined the requested system packages. The user's reason: Workers have no Docker; run the tests without containers."

// packageRequestRun is a lead-only work order whose lead asks for packages
// in its first implementation turn.
type packageRequestRun struct {
	*baseUpdateFixture
	environments *projectenvironment.Service
	errorsMu     sync.Mutex
	errors       []string
}

func (run *packageRequestRun) attempt(turn int) workerhttp.AttemptReference {
	leadID := remoteLeadSessionID(run.runID)
	return workerhttp.AttemptReference{SessionID: leadID, AttemptID: implementationAttemptID(leadID, turn)}
}

// script makes a copy of the lead's publishing implementation turn answer
// as the given attempt, with result in place of the publication when set.
func (run *packageRequestRun) script(to workerhttp.AttemptReference, result *workerhttp.TerminalResult) {
	copyScriptedAttempt(run.worker, run.attempt(99), to)
	if result != nil {
		run.worker.mu.Lock()
		terminal := run.worker.terminal[to]
		terminal.Result = result
		run.worker.terminal[to] = terminal
		run.worker.mu.Unlock()
	}
}

func packageRequestResult() *workerhttp.TerminalResult {
	return &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired,
		Summary: "I need Docker to run the app's containers.",
		EnvironmentRequest: &workerhttp.EnvironmentRequest{
			SystemPackages: []string{"docker.io", "docker-compose-v2"}, Reason: "Run the app's containers.",
		},
	}
}

// startPackageRequest runs a lead-only work order until its lead asks for
// packages. Every later implementation turn publishes unless prepare scripts
// it otherwise.
func startPackageRequest(t *testing.T, runID string, prepare func(*packageRequestRun)) *packageRequestRun {
	t.Helper()
	run := &packageRequestRun{}
	run.baseUpdateFixture = startBaseUpdateRun(t, runID, false, project.MergePolicyRequireUserApproval, 0,
		func(fixture *baseUpdateFixture) {
			run.baseUpdateFixture = fixture
			run.environments = projectenvironment.NewService(database.NewProjectEnvironmentStore(fixture.db))
			fixture.starter.environmentRequests = run.environments
			fixture.starter.reportError = func(err error) {
				run.errorsMu.Lock()
				defer run.errorsMu.Unlock()
				run.errors = append(run.errors, err.Error())
			}
			copyScriptedAttempt(fixture.worker, run.attempt(1), run.attempt(99))
			for turn := 2; turn <= 4; turn++ {
				run.script(run.attempt(turn), nil)
			}
			run.script(run.attempt(1), packageRequestResult())
			if prepare != nil {
				prepare(run)
			}
		})
	run.waitBlocked(t, "")
	return run
}

// reject declines the lead's package request as the user's Reject does.
func (run *packageRequestRun) reject(t *testing.T, turn int) error {
	t.Helper()
	request, _, err := run.environments.Reject(t.Context(), run.attempt(turn).AttemptID+":environment", "")
	if err != nil {
		t.Fatalf("reject the package request: %v", err)
	}
	return run.starter.ResumeAfterEnvironmentRejection(t.Context(), request, packageRejection)
}

// intervene has the user message the blocked lead, which answers it.
func (run *packageRequestRun) intervene(t *testing.T) {
	t.Helper()
	leadID := remoteLeadSessionID(run.runID)
	answer := workerhttp.AttemptReference{SessionID: leadID, AttemptID: interventionAttemptID(leadID, "int_why_docker")}
	run.script(answer, &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "I asked for Docker to run the containers.", InterventionEffect: workerhttp.InterventionEffectGuidanceApplied,
	})
	if _, _, err := run.starter.Pause(t.Context(), run.runID, "pause-to-ask"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, _, err := run.starter.QueueIntervention(
		t.Context(), run.runID, "int_why_docker", worker.RoleLead, "Why do you need Docker?",
	); err != nil {
		t.Fatalf("intervene: %v", err)
	}
	waitForInterventionStatus(t, run.executions, run.runID, execution.InterventionStatusAnswered)
	if _, _, err := run.starter.Resume(t.Context(), run.runID, "resume-after-asking"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	blocked := run.waitBlocked(t, "")
	if !strings.Contains(blocked.Reason, "docker.io") || run.leadCheckpoint(t).AttemptID != answer.AttemptID {
		t.Fatalf("the answered intervention did not leave the package request blocked: run=%+v checkpoint=%+v",
			blocked, run.leadCheckpoint(t))
	}
}

// expectContinued checks that the lead's next turn ran as turn in its own
// conversation, carrying want, and that the work order went on to the merge
// gate.
func (run *packageRequestRun) expectContinued(t *testing.T, turn int, want string) workerhttp.PutAttemptRequest {
	t.Helper()
	run.waitAtMergeGate(t)
	if checkpoint := run.leadCheckpoint(t); checkpoint.AttemptID != run.attempt(turn).AttemptID {
		t.Fatalf("the lead continued as %q, want %q", checkpoint.AttemptID, run.attempt(turn).AttemptID)
	}
	requests := run.requests()
	last := requests[len(requests)-1]
	if last.Mode != workerhttp.AttemptModeResume || last.ProviderSessionID != "codex-thread-test" ||
		last.OutputContract != workerhttp.OutputContractImplementationLead || !strings.Contains(last.Instructions, want) {
		t.Fatalf("the lead was not continued in its conversation with %q: %+v", want, last)
	}
	return last
}

func TestRejectingPackagesRepliesToTheLeadThatAsked(t *testing.T) {
	run := startPackageRequest(t, "run_reject_packages", nil)
	if err := run.reject(t, 1); err != nil {
		t.Fatalf("resume after rejection: %v", err)
	}
	run.expectContinued(t, 2, "Workers have no Docker")
	if run.workspace.continuationVerifyCalls != 1 {
		t.Fatalf("the rejection did not reach the lead as a reply: %+v", run.workspace)
	}
}

// The live failure: the user asked the blocked lead about its request before
// declining it. The answered intervention became the lead's latest attempt,
// so the rejection's reply was refused ("command is not allowed for session
// state") and every Continue and Re-check said the idle implementation attempt
// was incomplete.
func TestRejectingPackagesAfterAnInterventionContinuesTheLead(t *testing.T) {
	run := startPackageRequest(t, "run_reject_after_intervention", nil)
	run.intervene(t)
	if err := run.reject(t, 1); err != nil {
		t.Fatalf("resume after rejection: %v", err)
	}
	run.expectContinued(t, 2, "Workers have no Docker")
}

func TestContinueAfterAnInterventionAnswersThePackageRequest(t *testing.T) {
	for _, guidance := range []string{"", "Use the test database instead of containers."} {
		run := startPackageRequest(t, "run_continue_after_intervention_"+map[bool]string{true: "guided", false: "plain"}[guidance != ""], nil)
		run.intervene(t)
		if _, admitted, err := run.starter.RecoverBlockerWithDirective(
			t.Context(), run.runID, "continue-after-intervention",
			RecoveryDirective{Action: RecoveryActionContinue, Message: guidance},
		); err != nil || !admitted {
			t.Fatalf("continue: admitted=%t err=%v errors=%v", admitted, err, run.errors)
		}
		request := run.expectContinued(t, 2, "The user reviewed the blocker you reported")
		if !strings.Contains(request.Instructions, guidance) {
			t.Fatalf("the guidance did not reach the lead: %s", request.Instructions)
		}
	}
}

func TestRecheckAfterAnInterventionKeepsThePackageRequest(t *testing.T) {
	run := startPackageRequest(t, "run_recheck_after_intervention", nil)
	run.intervene(t)
	turns := len(run.requests())
	if _, admitted, err := run.starter.RecoverBlockerWithDirective(
		t.Context(), run.runID, "recheck-after-intervention", RecoveryDirective{Action: RecoveryActionRecheck},
	); err != nil || !admitted {
		t.Fatalf("re-check: admitted=%t err=%v errors=%v", admitted, err, run.errors)
	}
	blocked := run.waitBlocked(t, "")
	if !strings.Contains(blocked.Reason, "docker.io") || len(run.requests()) != turns || len(run.errors) != 0 {
		t.Fatalf("re-check did not just keep the package request: run=%+v errors=%v", blocked, run.errors)
	}
}

// A request the user declines after the lead moved on and a later turn of it
// failed: the lead cannot take a reply, so the rejection goes with Continue.
func TestRejectingPackagesAfterALaterTurnFailedContinuesWithTheReason(t *testing.T) {
	run := startPackageRequest(t, "run_reject_after_failure", func(run *packageRequestRun) {
		run.script(run.attempt(2), &workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeFailed, Summary: "The tests still fail after the change.",
			Error: &workerhttp.ProtocolError{Code: workerhttp.ErrorInternal, Message: "The tests still fail after the change."},
		})
	})
	if _, admitted, err := run.starter.RecoverBlockerWithDirective(
		t.Context(), run.runID, "continue-without-deciding", RecoveryDirective{Action: RecoveryActionContinue},
	); err != nil || !admitted {
		t.Fatalf("continue: admitted=%t err=%v", admitted, err)
	}
	blocked := run.waitBlocked(t, "")
	if !strings.Contains(blocked.Reason, "The tests still fail") {
		t.Fatalf("the later turn did not fail: %+v", blocked)
	}
	if err := run.reject(t, 1); err != nil {
		t.Fatalf("resume after rejection: %v errors=%v", err, run.errors)
	}
	run.expectContinued(t, 3, "Workers have no Docker")
}

func TestRejectingPackagesTheRunNoLongerWaitsOnNeedsNothing(t *testing.T) {
	run := startPackageRequest(t, "run_reject_after_merge_gate", nil)
	if _, admitted, err := run.starter.RecoverBlockerWithDirective(
		t.Context(), run.runID, "continue-without-deciding", RecoveryDirective{Action: RecoveryActionContinue},
	); err != nil || !admitted {
		t.Fatalf("continue: admitted=%t err=%v", admitted, err)
	}
	run.waitAtMergeGate(t)
	turns := len(run.requests())
	if err := run.reject(t, 1); err != nil {
		t.Fatalf("a rejection at the merge gate failed: %v", err)
	}
	if gated := run.waitAtMergeGate(t); len(run.requests()) != turns || gated.Reason != implementationLeadOnlyReadyReason {
		t.Fatalf("a rejection at the merge gate changed the run: %+v", gated)
	}
}

// A lead may also ask for packages while it corrects its work after a
// review. The user's rejection continues the same round, so the review that
// follows still answers that correction.
func TestRejectingPackagesDuringACorrectionTriesTheRoundAgain(t *testing.T) {
	runID := "run_reject_during_correction"
	leadID := remoteLeadSessionID(runID)
	correction := workerhttp.AttemptReference{SessionID: leadID, AttemptID: implementationCorrectionAttemptID(leadID, 1)}
	retry := workerhttp.AttemptReference{SessionID: leadID, AttemptID: roundRetryAttemptID(leadID, "correction", 1, 1, 2)}
	var environments *projectenvironment.Service
	fixture := startBaseUpdateRun(t, runID, true, project.MergePolicyRequireUserApproval, 0,
		func(fixture *baseUpdateFixture) {
			environments = projectenvironment.NewService(database.NewProjectEnvironmentStore(fixture.db))
			fixture.starter.environmentRequests = environments
			copyScriptedAttempt(fixture.worker, correction, retry)
			terminal := fixture.worker.terminal[correction]
			terminal.Result = packageRequestResult()
			fixture.worker.terminal[correction] = terminal
		})
	if blocked := fixture.waitBlocked(t, ""); !strings.Contains(blocked.Reason, "docker.io") {
		t.Fatalf("the correction's package request did not block the run: %+v", blocked)
	}
	request, _, err := environments.Reject(t.Context(), correction.AttemptID+":environment", "")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if err := fixture.starter.ResumeAfterEnvironmentRejection(t.Context(), request, packageRejection); err != nil {
		t.Fatalf("resume after rejection: %v", err)
	}
	fixture.waitAtMergeGate(t)
	retried := false
	for _, put := range fixture.requests() {
		retried = retried || put.ProviderSessionID == "codex-thread-test" && put.Mode == workerhttp.AttemptModeResume &&
			put.OutputContract == workerhttp.OutputContractImplementationLead &&
			strings.Contains(put.Instructions, "Workers have no Docker")
	}
	events, err := fixture.executions.EventsForSession(t.Context(), leadID)
	if err != nil {
		t.Fatalf("load lead activity: %v", err)
	}
	ran := false
	for _, event := range events {
		ran = ran || event.WorkerAttemptID == retry.AttemptID
	}
	if !retried || !ran {
		t.Fatalf("the lead was not continued with the rejection as the next try of its correction round: sent=%t ran=%t", retried, ran)
	}
	reviewerID := remoteReviewerSessionID(runID)
	if checkpoint, err := fixture.executions.GetWorkerAttempt(t.Context(), reviewerID); err != nil ||
		checkpoint.AttemptID != implementationReviewAttemptID(reviewerID, 2) {
		t.Fatalf("the review after the retried correction is not round 2: %+v err=%v", checkpoint, err)
	}
}
