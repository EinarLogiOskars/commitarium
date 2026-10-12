package orchestration

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestATurnStoppedToSwitchModelsResumesItsConversationOnTheNewModel(t *testing.T) {
	runID := "run_model_switch"
	starter, executions, stub := startFailingPlanningRun(t, runID, workerhttp.ErrorInternal)
	leadID := remoteLeadSessionID(runID)
	stoppedRef := workerhttp.AttemptReference{SessionID: leadID, AttemptID: planningAttemptID(leadID)}
	if _, _, err := executions.ChangeRunModels(t.Context(), runID,
		project.AgentModels{Lead: "cheap-model", Reviewer: "review-model"}, []string{stoppedRef.AttemptID},
	); err != nil {
		t.Fatalf("change models: %v", err)
	}

	starter.reportError = func(err error) { t.Log(err) }
	// The agent is mid-turn when the user switches, so the run is running.
	if _, err := executions.TransitionRun(
		t.Context(), runID, execution.RunStatusWaitingForUser, execution.RunStatusRunning, "The lead is planning.", "",
	); err != nil {
		t.Fatalf("resume the run: %v", err)
	}
	stub.mu.Lock()
	stopped := stub.terminal[stoppedRef]
	stopped.Result = &workerhttp.TerminalResult{Outcome: workerhttp.OutcomeStopped, Summary: "Stopped."}
	before := len(stub.putRequests)
	stub.mu.Unlock()
	starter.finish(t.Context(), remoteLeadRequest{
		runID: runID, agentName: "lead agent", planningStage: planningStageLeadProposal,
		waitingReason: "The lead proposed a plan.", waitKind: execution.RunWaitKindPhaseCheckpoint,
		identity: workerhttp.MutationIdentity{AttemptReference: stoppedRef},
	}, stopped)

	waitForRetriedTurn(t, executions, runID)
	run, err := executions.GetRun(t.Context(), runID)
	if err != nil || run.Status.IsTerminal() {
		t.Fatalf("the switch ended the run: %+v err=%v", run, err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.putRequests) != before+1 {
		t.Fatalf("expected one resumed turn, got %d", len(stub.putRequests)-before)
	}
	resumed := stub.putRequests[len(stub.putRequests)-1]
	if resumed.Mode != workerhttp.AttemptModeResume || resumed.ProviderSessionID != "codex-thread-test" ||
		resumed.Assignment.Model != "cheap-model" || !strings.Contains(resumed.Instructions, "switch you to the model cheap-model") {
		t.Fatalf("the stopped turn did not resume on the new model: %+v", resumed)
	}
	if switched, _ := executions.TakeModelSwitch(t.Context(), runID, stoppedRef.AttemptID); switched {
		t.Fatal("the switch mark outlived the resumed turn")
	}
}

func TestAStoppedTurnWithoutASwitchStillEndsTheRun(t *testing.T) {
	runID := "run_plain_stop"
	starter, executions, stub := startFailingPlanningRun(t, runID, workerhttp.ErrorInternal)
	leadID := remoteLeadSessionID(runID)
	ref := workerhttp.AttemptReference{SessionID: leadID, AttemptID: planningAttemptID(leadID)}
	stub.mu.Lock()
	stopped := stub.terminal[ref]
	stopped.Result = &workerhttp.TerminalResult{Outcome: workerhttp.OutcomeStopped, Summary: "Stopped."}
	stub.mu.Unlock()
	starter.finish(t.Context(), remoteLeadRequest{
		runID: runID, agentName: "lead agent", planningStage: planningStageLeadProposal,
		identity: workerhttp.MutationIdentity{AttemptReference: ref},
	}, stopped)
	if run, _ := executions.GetRun(t.Context(), runID); run.Status != execution.RunStatusStopped {
		t.Fatalf("a plain stop no longer ends the run: %+v", run)
	}
}

func TestContinueAnswersAnAgentThatAskedForHelp(t *testing.T) {
	runID := "run_answer_blocker"
	starter, executions, stub := startFailingPlanningRun(t, runID, workerhttp.ErrorInternal)
	leadID := remoteLeadSessionID(runID)
	ref := workerhttp.AttemptReference{SessionID: leadID, AttemptID: planningAttemptID(leadID)}
	stub.mu.Lock()
	asked := stub.terminal[ref]
	asked.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired,
		Summary: "The coordinator refused the step's start; it must resolve the conflict.",
	}
	stub.terminal[ref] = asked
	stub.mu.Unlock()

	if _, admitted, err := starter.RecoverBlockerWithDirective(
		t.Context(), runID, "continue-after-question", RecoveryDirective{Action: RecoveryActionContinue, Message: "Retry it now."},
	); err != nil || !admitted {
		t.Fatalf("continue: admitted=%t err=%v", admitted, err)
	}
	waitForRetriedTurn(t, executions, runID)
	stub.mu.Lock()
	defer stub.mu.Unlock()
	resumed := stub.putRequests[len(stub.putRequests)-1]
	if resumed.Mode != workerhttp.AttemptModeResume || resumed.ProviderSessionID != "codex-thread-test" ||
		!strings.Contains(resumed.Instructions, "reviewed the blocker you reported") ||
		!strings.Contains(resumed.Instructions, "Retry it now.") {
		t.Fatalf("Continue did not answer the agent in its conversation: %+v", resumed)
	}
}
