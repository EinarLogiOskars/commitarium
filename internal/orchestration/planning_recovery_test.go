package orchestration

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A lead recovered during its first planning turn (ADR-032) answers under a
// recovery attempt ID, and the plan it proposes there still goes to review.
func TestARecoveredLeadsPlanGoesToReview(t *testing.T) {
	for _, action := range []RecoveryAction{RecoveryActionContinue, RecoveryActionReplace} {
		t.Run(string(action), func(t *testing.T) {
			runID := "run_recovered_plan_" + string(action)
			starter, executions, stub := startFailingPlanningRun(t, runID, workerhttp.ErrorInternal)
			addCompletedReviewerPlanningAttempt(stub, runID, "", "")
			if _, admitted, err := starter.RecoverBlockerWithDirective(
				t.Context(), runID, "recover-planning", RecoveryDirective{Action: action},
			); err != nil || !admitted {
				t.Fatalf("recover planning: admitted=%t err=%v", admitted, err)
			}
			waitForRetriedTurn(t, executions, runID)
			leadID := remoteLeadSessionID(runID)
			checkpoint, err := executions.GetWorkerAttempt(t.Context(), leadID)
			if err != nil || checkpoint.AttemptID != planningRecoveryAttemptID(leadID, 1, 1, 1) {
				t.Fatalf("the lead did not answer under its recovery attempt: %+v err=%v", checkpoint, err)
			}

			reviewRun, admitted, err := starter.StartPlanningReview(t.Context(), runID, "start-reviewer")
			if err != nil || !admitted || reviewRun.Status != execution.RunStatusRunning {
				t.Fatalf("start reviewer after recovery: run=%+v admitted=%t err=%v", reviewRun, admitted, err)
			}
			waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
			messages, err := executions.PlanningMessagesForRun(t.Context(), runID)
			if err != nil || len(messages) != 2 ||
				messages[0].Role != worker.RoleLead || messages[0].Event.Text != "Proposed implementation plan" ||
				messages[0].Event.WorkerAttemptID != checkpoint.AttemptID ||
				messages[1].Role != worker.RoleReviewer {
				t.Fatalf("unexpected planning history after recovery: %+v err=%v", messages, err)
			}
			stub.mu.Lock()
			requests := len(stub.putRequests)
			review := stub.putRequests[requests-1]
			stub.mu.Unlock()
			if review.Assignment.Role != workerhttp.RoleReviewer ||
				!strings.Contains(review.Instructions, "Proposed implementation plan") {
				t.Fatalf("the reviewer was not given the recovered proposal: %+v", review)
			}

			// Asking again does not start a second review.
			if _, admitted, err := starter.StartPlanningReview(t.Context(), runID, "start-reviewer"); err != nil || admitted {
				t.Fatalf("retry reviewer: admitted=%t err=%v", admitted, err)
			}
			stub.mu.Lock()
			defer stub.mu.Unlock()
			if len(stub.putRequests) != requests {
				t.Fatalf("reviewer retry launched another worker turn: %d requests", len(stub.putRequests))
			}
		})
	}
}

// Without a reviewer, a recovered lead that replied instead of submitting is
// asked again for its plan, and the plan it submits is published.
func TestARecoveredSoloLeadIsAskedForItsPlan(t *testing.T) {
	runID := "run_recovered_solo_plan"
	starter, executions, stub := startFailingPlanningRunWithSteps(
		t, runID, workerhttp.ErrorInternal, project.WorkSteps{SkipReviewer: true},
	)
	leadID := remoteLeadSessionID(runID)
	addCompletedPlanningCorrectionAttempt(
		stub, leadID, "", "", workerhttp.RoleLead, "codex-thread-test", 2,
		workerhttp.EventPlanSubmitted, "Complete final implementation plan",
	)
	if _, admitted, err := starter.RecoverBlockerWithDirective(
		t.Context(), runID, "recover-planning", RecoveryDirective{Action: RecoveryActionContinue},
	); err != nil || !admitted {
		t.Fatalf("recover planning: admitted=%t err=%v", admitted, err)
	}
	published := waitForRunReason(t, executions, runID, "plan was published")
	if published.WaitKind != execution.RunWaitKindPhaseCheckpoint {
		t.Fatalf("the recovered solo plan was not published: %+v", published)
	}
	messages, err := executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != 2 ||
		messages[0].Event.WorkerAttemptID != planningRecoveryAttemptID(leadID, 1, 1, 1) ||
		messages[1].Event.Type != worker.EventPlanSubmitted ||
		messages[1].Event.WorkerAttemptID != planningTurnAttemptID(leadID, 2) {
		t.Fatalf("unexpected solo planning history after recovery: %+v err=%v", messages, err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	asked := stub.putRequests[len(stub.putRequests)-1]
	if asked.Assignment.Role != workerhttp.RoleLead || !strings.Contains(asked.Instructions, "submit_plan") {
		t.Fatalf("the lead was not asked again to submit its plan: %+v", asked)
	}
}

func TestIsPlanningTurnAcceptsRecoverySuccessors(t *testing.T) {
	const session = "run_x:lead"
	for _, tc := range []struct {
		attemptID     string
		version, turn int
		want          bool
	}{
		{"run_x:lead:planning:1", 1, 1, true},
		{"run_x:lead:planning-recovery:1:1", 1, 1, true},
		{"run_x:lead:planning-recovery:1:3", 1, 1, true},
		{"run_x:lead:planning:2", 1, 1, false},
		{"run_x:lead:planning-recovery:2:1", 1, 1, false},
		{"run_x:lead:planning:v2:1", 2, 1, true},
		{"run_x:lead:planning-recovery:v2:1:1", 2, 1, true},
		{"run_x:lead:planning-recovery:v2:1:1", 1, 1, false},
		{"run_x:lead:planning-recovery:1:1", 2, 1, false},
		{"run_x:reviewer:planning:1", 1, 1, false},
		{"run_x:lead:implementation:1", 1, 1, false},
	} {
		if got := isPlanningTurn(session, tc.attemptID, tc.version, tc.turn); got != tc.want {
			t.Errorf("isPlanningTurn(%q, v%d turn %d) = %t, want %t", tc.attemptID, tc.version, tc.turn, got, tc.want)
		}
	}
}
