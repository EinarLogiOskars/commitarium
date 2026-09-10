package orchestration

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestRunnerDrivesPlanningImplementationAndReviewLoop(t *testing.T) {
	workflowService, featureStore, executionService := orchestrationDatabase(t)
	codex := autoAdvance(newTestCodex())
	claude := autoAdvance(newTestClaude(
		worker.DispositionChangesRequested,
		worker.DispositionSucceeded,
	))
	activeSessions := NewActiveSessions()
	runner := NewRunner(workflowService, executionService, activeSessions)
	request := testRunRequest(codex, claude, 2)

	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("run feature: %v", err)
	}
	if result.Status != RunStatusReadyToMerge {
		t.Fatalf("expected status %q, got %+v", RunStatusReadyToMerge, result)
	}
	if result.PlanningRounds != 1 || result.ReviewRounds != 2 {
		t.Errorf("unexpected round counts %+v", result)
	}
	if len(result.Sessions) != 6 {
		t.Errorf("expected six sessions, got %d", len(result.Sessions))
	}
	if !strings.Contains(claude.requests[0].Instructions, "proposed an accepted plan") {
		t.Errorf("consultant did not receive lead proposal: %q", claude.requests[0].Instructions)
	}
	if !strings.Contains(codex.requests[1].Instructions, "accepted the refined plan") {
		t.Errorf("coder did not receive accepted plan: %q", codex.requests[1].Instructions)
	}
	if !strings.Contains(codex.requests[2].Instructions, "changes_requested") {
		t.Errorf("fix session did not receive review findings: %q", codex.requests[2].Instructions)
	}
	if !strings.Contains(claude.requests[2].Instructions, "addressed the review findings") {
		t.Errorf("second review did not receive fix summary: %q", claude.requests[2].Instructions)
	}

	storedFeature, err := featureStore.GetByID(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if storedFeature.State != feature.StateReadyToMerge {
		t.Errorf("expected state %q, got %q", feature.StateReadyToMerge, storedFeature.State)
	}
	storedRun, err := executionService.GetRun(t.Context(), request.ID)
	if err != nil {
		t.Fatalf("get execution run: %v", err)
	}
	if storedRun.Status != execution.RunStatusSucceeded || storedRun.EndedAt == nil {
		t.Errorf("unexpected stored run %+v", storedRun)
	}
	for _, summary := range result.Sessions {
		storedSession, err := executionService.GetSession(t.Context(), summary.SessionID)
		if err != nil {
			t.Fatalf("get session %q: %v", summary.SessionID, err)
		}
		if storedSession.Status != execution.SessionStatusCompleted ||
			storedSession.ProviderSessionID != summary.Result.ProviderSessionID ||
			storedSession.EndedAt == nil {
			t.Errorf("unexpected stored session %+v", storedSession)
		}
		events, err := executionService.EventsForSession(t.Context(), summary.SessionID)
		if err != nil {
			t.Fatalf("get events for session %q: %v", summary.SessionID, err)
		}
		if len(events) != 1 || events[0].Sequence != 1 {
			t.Errorf("unexpected events for session %q: %+v", summary.SessionID, events)
		}
		if _, active := activeSessions.Get(summary.SessionID); active {
			t.Errorf("expected completed session %q to be unregistered", summary.SessionID)
		}
	}
	retried, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if retried.Status != RunStatusReadyToMerge || len(retried.Sessions) != 0 {
		t.Errorf("unexpected retried result %+v", retried)
	}
	events, err := workflowService.EventsForFeature(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("list workflow events: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected four workflow events, got %d", len(events))
	}
	expectedStates := []feature.State{
		feature.StatePlanning,
		feature.StateImplementing,
		feature.StateReviewing,
		feature.StateReadyToMerge,
	}
	for index, event := range events {
		payload, err := workflow.DecodeFeatureStateChangedPayload(
			event.PayloadVersion,
			event.Payload,
		)
		if err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		if payload.State != expectedStates[index] {
			t.Errorf("expected event %d state %q, got %q", index, expectedStates[index], payload.State)
		}
		if event.Actor.Kind != workflow.ActorKindCoordinator || event.Actor.ID != coordinatorActorID {
			t.Errorf("unexpected transition actor %+v", event.Actor)
		}
	}
}

func TestRunnerTreatsZeroDialogueLimitsAsUnlimited(t *testing.T) {
	workflowService, _, executionService := orchestrationDatabase(t)
	request := testRunRequest(
		autoAdvance(newTestCodex()),
		autoAdvance(newTestClaude(worker.DispositionChangesRequested, worker.DispositionSucceeded)),
		0,
	)
	request.ID = "run_unlimited"
	request.MaxPlanningRounds = 0

	result, err := NewRunner(workflowService, executionService, NewActiveSessions()).Run(t.Context(), request)
	if err != nil {
		t.Fatalf("run unlimited workflow: %v", err)
	}
	if result.Status != RunStatusReadyToMerge || result.PlanningRounds != 1 || result.ReviewRounds != 2 {
		t.Fatalf("unexpected unlimited workflow result %+v", result)
	}
	stored, err := executionService.GetRun(t.Context(), request.ID)
	if err != nil {
		t.Fatalf("get unlimited run: %v", err)
	}
	if stored.PlanningRoundLimit != 0 || stored.ImplementationReviewRoundLimit != 0 {
		t.Fatalf("unlimited snapshot was not persisted: %+v", stored)
	}
}

func TestRunnerStartsSimulatedWorkflowAsynchronously(t *testing.T) {
	workflowService, featureStore, executionService := orchestrationDatabase(t)
	runner := NewRunner(workflowService, executionService, NewActiveSessions())
	request := RunRequest{
		ID: "run_async", FeatureID: "fea_test", Goal: "Exercise the public workflow",
		Assignment:        NewSimulatedAssignment(0),
		MaxPlanningRounds: 2,
		MaxReviewRounds:   3,
	}

	started, created, err := runner.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start asynchronous run: %v", err)
	}
	if !created || started.Status != execution.RunStatusRunning {
		t.Fatalf("unexpected admitted run %+v, created=%t", started, created)
	}

	deadline := time.Now().Add(2 * time.Second)
	var completed execution.Run
	for time.Now().Before(deadline) {
		completed, err = executionService.GetRun(t.Context(), request.ID)
		if err != nil {
			t.Fatalf("get asynchronous run: %v", err)
		}
		if completed.Status == execution.RunStatusSucceeded {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if completed.Status != execution.RunStatusSucceeded || completed.EndedAt == nil {
		t.Fatalf("asynchronous run did not complete: %+v", completed)
	}
	sessions, err := executionService.SessionsForRun(t.Context(), request.ID)
	if err != nil {
		t.Fatalf("list asynchronous sessions: %v", err)
	}
	if len(sessions) != 6 {
		t.Fatalf("expected six sessions, got %+v", sessions)
	}
	storedFeature, err := featureStore.GetByID(t.Context(), request.FeatureID)
	if err != nil {
		t.Fatalf("get completed feature: %v", err)
	}
	if storedFeature.State != feature.StateReadyToMerge {
		t.Errorf("expected ready-to-merge feature, got %q", storedFeature.State)
	}

	retried, created, err := runner.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("retry asynchronous run: %v", err)
	}
	if created || retried.Status != execution.RunStatusSucceeded {
		t.Errorf("unexpected retry %+v, created=%t", retried, created)
	}
}

func TestRunnerRecoversInterruptedSessionAndWaitsForApproval(t *testing.T) {
	workflowService, featureStore, executionService := orchestrationDatabase(t)
	seedInterruptedConsultant(t, workflowService, executionService, "run_recovery_approval")

	activeSessions := NewActiveSessions()
	runner := NewRunner(workflowService, executionService, activeSessions)
	request := recoveryRunRequest(
		"run_recovery_approval",
		project.RecoveryPolicyApprovalRequired,
	)
	if err := runner.Recover(t.Context(), request); err != nil {
		t.Fatalf("recover run: %v", err)
	}

	sessionID := request.ID + ":plan-consultant-1"
	waitForSessionStatus(t, executionService, sessionID, execution.SessionStatusPaused)
	waitForRunStatus(t, executionService, request.ID, execution.RunStatusWaitingForUser)
	session, err := executionService.GetSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("get recovering session: %v", err)
	}
	if session.RecoveryAttempt != 1 {
		t.Fatalf("expected one recovery attempt, got %+v", session)
	}
	events, err := executionService.EventsForSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("list recovery activity: %v", err)
	}
	if len(events) != 1 || events[0].Type != worker.EventRecoveryAssessment {
		t.Fatalf("expected one recovery assessment, got %+v", events)
	}

	controller := NewController(executionService, activeSessions)
	if _, err := controller.SendCommand(t.Context(), sessionID, worker.Command{
		ID: "approve-recovery", Type: worker.CommandContinue,
	}); err != nil {
		t.Fatalf("approve recovery: %v", err)
	}
	waitForRunStatus(t, executionService, request.ID, execution.RunStatusSucceeded)

	storedFeature, err := featureStore.GetByID(t.Context(), request.FeatureID)
	if err != nil {
		t.Fatalf("get recovered feature: %v", err)
	}
	if storedFeature.State != feature.StateReadyToMerge {
		t.Fatalf("expected recovered feature ready to merge, got %q", storedFeature.State)
	}
	sessions, err := executionService.SessionsForRun(t.Context(), request.ID)
	if err != nil {
		t.Fatalf("list recovered sessions: %v", err)
	}
	if len(sessions) != 6 {
		t.Fatalf("expected six sessions without duplicated work, got %+v", sessions)
	}
}

func TestRunnerAutomaticallyRecoversConsistentSession(t *testing.T) {
	workflowService, _, executionService := orchestrationDatabase(t)
	seedInterruptedConsultant(t, workflowService, executionService, "run_recovery_auto")
	runner := NewRunner(workflowService, executionService, NewActiveSessions())
	request := recoveryRunRequest("run_recovery_auto", project.RecoveryPolicyAutomatic)

	if err := runner.Recover(t.Context(), request); err != nil {
		t.Fatalf("recover run: %v", err)
	}
	waitForRunStatus(t, executionService, request.ID, execution.RunStatusSucceeded)

	session, err := executionService.GetSession(t.Context(), request.ID+":plan-consultant-1")
	if err != nil {
		t.Fatalf("get recovered session: %v", err)
	}
	if session.RecoveryAttempt != 1 || session.Status != execution.SessionStatusCompleted {
		t.Fatalf("unexpected automatically recovered session %+v", session)
	}
}

func TestRunnerRecoversInterruptionBetweenSessionsWithoutApproval(t *testing.T) {
	workflowService, _, executionService := orchestrationDatabase(t)
	runID := "run_recovery_between_sessions"
	if _, _, err := executionService.CreateRun(t.Context(), runID, "fea_test", 2, 3); err != nil {
		t.Fatalf("create interrupted run: %v", err)
	}
	runner := NewRunner(workflowService, executionService, NewActiveSessions())
	request := recoveryRunRequest(runID, project.RecoveryPolicyApprovalRequired)
	request.WorkflowPhase = feature.StateDraft

	if err := runner.Recover(t.Context(), request); err != nil {
		t.Fatalf("recover run between sessions: %v", err)
	}
	waitForRunStatus(t, executionService, runID, execution.RunStatusSucceeded)
	sessions, err := executionService.SessionsForRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("list recovered run sessions: %v", err)
	}
	if len(sessions) != 6 {
		t.Fatalf("expected normal six-session workflow, got %+v", sessions)
	}
	for _, session := range sessions {
		if session.RecoveryAttempt != 0 {
			t.Fatalf("new session was incorrectly treated as resumed: %+v", session)
		}
	}
}

func TestAutomaticRecoveryStopsForUncertainCommandDelivery(t *testing.T) {
	workflowService, _, executionService := orchestrationDatabase(t)
	runID := "run_recovery_command"
	seedInterruptedConsultant(t, workflowService, executionService, runID)
	sessionID := runID + ":plan-consultant-1"
	if _, created, err := executionService.CreateCommand(
		t.Context(),
		"cmd_interrupted",
		sessionID,
		worker.CommandMessage,
		"Did the previous operation finish?",
	); err != nil || !created {
		t.Fatalf("create interrupted command: created=%t err=%v", created, err)
	}
	activeSessions := NewActiveSessions()
	runner := NewRunner(workflowService, executionService, activeSessions)
	request := recoveryRunRequest(runID, project.RecoveryPolicyAutomatic)

	if err := runner.Recover(t.Context(), request); err != nil {
		t.Fatalf("recover run: %v", err)
	}
	waitForSessionStatus(t, executionService, sessionID, execution.SessionStatusPaused)
	waitForRunStatus(t, executionService, runID, execution.RunStatusWaitingForUser)
	storedRun, err := executionService.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("get gated run: %v", err)
	}
	if !strings.Contains(storedRun.Reason, "uncertain command delivery") {
		t.Fatalf("unexpected recovery reason %q", storedRun.Reason)
	}
	events, err := executionService.EventsForSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("list recovery events: %v", err)
	}
	if len(events) != 1 || !strings.Contains(events[0].Text, "1 command delivery outcome is uncertain") {
		t.Fatalf("pending command was not visible in recovery assessment: %+v", events)
	}

	controller := NewController(executionService, activeSessions)
	if _, err := controller.SendCommand(t.Context(), sessionID, worker.Command{
		ID: "approve-after-command-review", Type: worker.CommandContinue,
	}); err != nil {
		t.Fatalf("approve recovery after reviewing command: %v", err)
	}
	waitForRunStatus(t, executionService, runID, execution.RunStatusSucceeded)
}

func TestRecoveryDoesNotReplaceSessionWithUnconfirmedProviderIdentity(t *testing.T) {
	workflowService, _, executionService := orchestrationDatabase(t)
	runID := "run_recovery_missing_provider"
	if _, _, err := executionService.CreateRun(t.Context(), runID, "fea_test", 2, 3); err != nil {
		t.Fatalf("create interrupted run: %v", err)
	}
	if _, err := workflowService.TransitionFeature(
		t.Context(),
		"fea_test",
		feature.StatePlanning,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
		runID+":state:planning",
	); err != nil {
		t.Fatalf("move feature to planning: %v", err)
	}
	sessionID := runID + ":plan-lead-1"
	if _, _, err := executionService.CreateSession(
		t.Context(), sessionID, runID, "agt_fake_codex", worker.RoleLead,
	); err != nil {
		t.Fatalf("create ambiguous starting session: %v", err)
	}
	runner := NewRunner(workflowService, executionService, NewActiveSessions())
	request := recoveryRunRequest(runID, project.RecoveryPolicyAutomatic)

	if err := runner.Recover(t.Context(), request); err != nil {
		t.Fatalf("admit recovery: %v", err)
	}
	waitForRunStatus(t, executionService, runID, execution.RunStatusWaitingForUser)
	session, err := executionService.GetSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("get blocked session: %v", err)
	}
	if session.Status != execution.SessionStatusFailed || session.ProviderSessionID != "" {
		t.Fatalf("ambiguous session was replaced or left active: %+v", session)
	}
	events, err := executionService.EventsForSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("list blocked recovery activity: %v", err)
	}
	if len(events) != 1 || !strings.Contains(events[0].Text, "no durably confirmed provider identity") {
		t.Fatalf("missing provider identity was not observable: %+v", events)
	}
}

func TestRunnerWaitsForUserWhenReviewLimitIsReached(t *testing.T) {
	workflowService, featureStore, executionService := orchestrationDatabase(t)
	codex := autoAdvance(newTestCodex())
	claude := autoAdvance(newTestClaude(worker.DispositionChangesRequested))
	runner := NewRunner(workflowService, executionService, NewActiveSessions())

	result, err := runner.Run(t.Context(), testRunRequest(codex, claude, 1))
	if err != nil {
		t.Fatalf("run feature: %v", err)
	}
	if result.Status != RunStatusWaiting ||
		result.Reason != "review round limit reached with unresolved findings" {
		t.Errorf("unexpected run result %+v", result)
	}
	storedFeature, err := featureStore.GetByID(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if storedFeature.State != feature.StateReviewing {
		t.Errorf("expected state %q, got %q", feature.StateReviewing, storedFeature.State)
	}
	storedRun, err := executionService.GetRun(t.Context(), "run_test")
	if err != nil {
		t.Fatalf("get execution run: %v", err)
	}
	if storedRun.Status != execution.RunStatusWaitingForUser || storedRun.EndedAt != nil {
		t.Errorf("unexpected stored run %+v", storedRun)
	}
}

func TestRunnerWaitsForUserWhenPlanningLimitIsReached(t *testing.T) {
	workflowService, featureStore, executionService := orchestrationDatabase(t)
	codex := autoAdvance(worker.NewQueuedScriptedAdapter("fake-codex", map[worker.Role][]worker.Script{
		worker.RoleLead: {
			{
				Events:      []worker.Event{{Type: worker.EventMessage, Text: "plan still has a material disagreement"}},
				Disposition: worker.DispositionChangesRequested,
				Summary:     "planning disagreement",
			},
		},
	}))
	claude := autoAdvance(newTestClaude(worker.DispositionSucceeded))
	runner := NewRunner(workflowService, executionService, NewActiveSessions())
	request := testRunRequest(codex, claude, 1)
	request.MaxPlanningRounds = 1

	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("run feature: %v", err)
	}
	if result.Status != RunStatusWaiting ||
		result.Reason != "planning round limit reached without agent agreement" {
		t.Errorf("unexpected run result %+v", result)
	}
	storedFeature, err := featureStore.GetByID(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if storedFeature.State != feature.StatePlanning {
		t.Errorf("expected state %q, got %q", feature.StatePlanning, storedFeature.State)
	}
	storedRun, err := executionService.GetRun(t.Context(), "run_test")
	if err != nil {
		t.Fatalf("get execution run: %v", err)
	}
	if storedRun.Status != execution.RunStatusWaitingForUser || storedRun.EndedAt != nil {
		t.Errorf("unexpected stored run %+v", storedRun)
	}
}

func TestRunnerPersistsWorkerStartFailure(t *testing.T) {
	workflowService, _, executionService := orchestrationDatabase(t)
	codex := autoAdvance(worker.NewQueuedScriptedAdapter(
		"fake-codex",
		map[worker.Role][]worker.Script{},
	))
	claude := autoAdvance(newTestClaude(worker.DispositionSucceeded))
	runner := NewRunner(workflowService, executionService, NewActiveSessions())

	_, err := runner.Run(t.Context(), testRunRequest(codex, claude, 1))
	if !errors.Is(err, worker.ErrScriptNotFound) {
		t.Fatalf("expected error %v, got %v", worker.ErrScriptNotFound, err)
	}
	storedSession, getErr := executionService.GetSession(
		t.Context(),
		"run_test:plan-lead-1",
	)
	if getErr != nil {
		t.Fatalf("get failed session: %v", getErr)
	}
	if storedSession.Status != execution.SessionStatusFailed || storedSession.EndedAt == nil {
		t.Errorf("unexpected failed session %+v", storedSession)
	}
	storedRun, getErr := executionService.GetRun(t.Context(), "run_test")
	if getErr != nil {
		t.Fatalf("get failed run: %v", getErr)
	}
	if storedRun.Status != execution.RunStatusFailed ||
		storedRun.EndedAt == nil ||
		!strings.Contains(storedRun.Reason, worker.ErrScriptNotFound.Error()) {
		t.Errorf("unexpected failed run %+v", storedRun)
	}
}

func TestRunnerRejectsDuplicateActiveRun(t *testing.T) {
	workflowService, _, executionService := orchestrationDatabase(t)
	if _, created, err := executionService.CreateRun(
		t.Context(),
		"run_test",
		"fea_test",
		2,
		1,
	); err != nil {
		t.Fatalf("create active run: %v", err)
	} else if !created {
		t.Fatal("expected active run to be newly created")
	}
	runner := NewRunner(workflowService, executionService, NewActiveSessions())

	_, err := runner.Run(
		t.Context(),
		testRunRequest(autoAdvance(newTestCodex()), autoAdvance(newTestClaude()), 1),
	)
	if !errors.Is(err, ErrRunAlreadyActive) {
		t.Fatalf("expected error %v, got %v", ErrRunAlreadyActive, err)
	}
}

func TestRunnerRoutesCommandsThroughActiveSessionRegistry(t *testing.T) {
	workflowService, _, executionService := orchestrationDatabase(t)
	codex := worker.NewQueuedScriptedAdapter("fake-codex", map[worker.Role][]worker.Script{
		worker.RoleLead: {
			{
				Events:      []worker.Event{{Type: worker.EventActivity, Text: "drafting"}},
				Disposition: worker.DispositionSucceeded,
				Summary:     "drafted",
			},
		},
	})
	claude := newTestClaude(worker.DispositionSucceeded)
	activeSessions := NewActiveSessions()
	runner := NewRunner(workflowService, executionService, activeSessions)
	controller := NewController(executionService, activeSessions)
	type runOutcome struct {
		result RunResult
		err    error
	}
	finished := make(chan runOutcome, 1)
	go func() {
		result, err := runner.Run(t.Context(), testRunRequest(codex, claude, 1))
		finished <- runOutcome{result: result, err: err}
	}()

	waitForActiveSession(t, activeSessions, "run_test:plan-lead-1")
	startedSession, err := executionService.GetSession(t.Context(), "run_test:plan-lead-1")
	if err != nil {
		t.Fatalf("get newly started session: %v", err)
	}
	if startedSession.ProviderSessionID == "" {
		t.Fatal("provider session ID was not persisted while the session was active")
	}
	if _, err := controller.SendCommand(t.Context(), "run_test:plan-lead-1", worker.Command{
		ID: "cmd_pause", Type: worker.CommandPause,
	}); err != nil {
		t.Fatalf("pause active session: %v", err)
	}
	waitForSessionStatus(
		t,
		executionService,
		"run_test:plan-lead-1",
		execution.SessionStatusPaused,
	)
	if _, err := controller.SendCommand(t.Context(), "run_test:plan-lead-1", worker.Command{
		ID: "cmd_continue", Type: worker.CommandContinue,
	}); err != nil {
		t.Fatalf("continue active session: %v", err)
	}
	waitForSessionStatus(
		t,
		executionService,
		"run_test:plan-lead-1",
		execution.SessionStatusRunning,
	)
	if _, err := controller.SendCommand(t.Context(), "run_test:plan-lead-1", worker.Command{
		ID: "cmd_stop", Type: worker.CommandStop,
	}); err != nil {
		t.Fatalf("stop active session: %v", err)
	}

	select {
	case outcome := <-finished:
		if outcome.err != nil {
			t.Fatalf("finish stopped run: %v", outcome.err)
		}
		if outcome.result.Status != RunStatusStopped {
			t.Errorf("unexpected stopped result %+v", outcome.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stopped run")
	}
	storedRun, err := executionService.GetRun(t.Context(), "run_test")
	if err != nil {
		t.Fatalf("get stopped run: %v", err)
	}
	if storedRun.Status != execution.RunStatusStopped || storedRun.EndedAt == nil {
		t.Errorf("unexpected stopped run %+v", storedRun)
	}
	storedSession, err := executionService.GetSession(t.Context(), "run_test:plan-lead-1")
	if err != nil {
		t.Fatalf("get stopped session: %v", err)
	}
	if storedSession.Status != execution.SessionStatusStopped || storedSession.EndedAt == nil {
		t.Errorf("unexpected stopped session %+v", storedSession)
	}
	events, err := executionService.EventsForSession(t.Context(), "run_test:plan-lead-1")
	if err != nil {
		t.Fatalf("get controlled session events: %v", err)
	}
	if len(events) != 2 ||
		events[0].Type != worker.EventPauseAcknowledged ||
		events[1].Type != worker.EventContinued {
		t.Errorf("unexpected controlled session events %+v", events)
	}
	if _, active := activeSessions.Get("run_test:plan-lead-1"); active {
		t.Error("expected stopped session to be unregistered")
	}
}

func waitForActiveSession(
	t *testing.T,
	registry *ActiveSessions,
	sessionID string,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, found := registry.Get(sessionID); found {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for active session %q", sessionID)
}

func waitForSessionStatus(
	t *testing.T,
	service *execution.Service,
	sessionID string,
	status execution.SessionStatus,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last execution.SessionStatus
	var runID string
	for time.Now().Before(deadline) {
		session, err := service.GetSession(t.Context(), sessionID)
		if err != nil {
			t.Fatalf("get session %q: %v", sessionID, err)
		}
		if session.Status == status {
			return
		}
		last = session.Status
		runID = session.RunID
		if last == execution.SessionStatusFailed {
			storedRun, _ := service.GetRun(t.Context(), runID)
			t.Fatalf("session %q failed while waiting for %q; run: %+v", sessionID, status, storedRun)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf(
		"timed out waiting for session %q status %q; last status was %q",
		sessionID,
		status,
		last,
	)
}

func waitForRunStatus(
	t *testing.T,
	service *execution.Service,
	runID string,
	status execution.RunStatus,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last execution.Run
	for time.Now().Before(deadline) {
		var err error
		last, err = service.GetRun(t.Context(), runID)
		if err != nil {
			t.Fatalf("get run %q: %v", runID, err)
		}
		if last.Status == status {
			return
		}
		if last.Status.IsTerminal() && last.Status != status {
			t.Fatalf("run %q ended as %q while waiting for %q: %+v", runID, last.Status, status, last)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for run %q status %q; last run %+v", runID, status, last)
}

func seedInterruptedConsultant(
	t *testing.T,
	workflowService *workflow.Service,
	executionService *execution.Service,
	runID string,
) {
	t.Helper()
	if _, _, err := executionService.CreateRun(t.Context(), runID, "fea_test", 2, 3); err != nil {
		t.Fatalf("create interrupted run: %v", err)
	}
	if _, err := workflowService.TransitionFeature(
		t.Context(),
		"fea_test",
		feature.StatePlanning,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
		runID+":state:planning",
	); err != nil {
		t.Fatalf("move interrupted feature to planning: %v", err)
	}
	leadID := runID + ":plan-lead-1"
	if _, _, err := executionService.CreateSession(
		t.Context(), leadID, runID, "agt_fake_codex", worker.RoleLead,
	); err != nil {
		t.Fatalf("create completed lead session: %v", err)
	}
	leadProviderID := "fake-codex:lead:0:" + leadID
	if _, err := executionService.TransitionSession(
		t.Context(), leadID,
		execution.SessionStatusStarting,
		execution.SessionStatusRunning,
		leadProviderID,
	); err != nil {
		t.Fatalf("start completed lead session: %v", err)
	}
	if _, err := executionService.CompleteSession(
		t.Context(), leadID,
		execution.SessionStatusRunning,
		execution.SessionStatusCompleted,
		worker.Result{
			Outcome:           worker.OutcomeCompleted,
			Disposition:       worker.DispositionSucceeded,
			ProviderSessionID: leadProviderID,
			Summary:           "proposed an accepted implementation plan",
		},
	); err != nil {
		t.Fatalf("complete lead session: %v", err)
	}

	consultantID := runID + ":plan-consultant-1"
	if _, _, err := executionService.CreateSession(
		t.Context(), consultantID, runID, "agt_fake_claude", worker.RoleConsultant,
	); err != nil {
		t.Fatalf("create interrupted consultant session: %v", err)
	}
	if _, err := executionService.TransitionSession(
		t.Context(), consultantID,
		execution.SessionStatusStarting,
		execution.SessionStatusRunning,
		"fake-claude:consultant:0:"+consultantID,
	); err != nil {
		t.Fatalf("mark consultant session running: %v", err)
	}
}

func recoveryRunRequest(runID string, policy project.RecoveryPolicy) RunRequest {
	return RunRequest{
		ID: runID, FeatureID: "fea_test", Goal: "Test: recover a simulated workflow",
		Assignment:        NewSimulatedAssignment(time.Millisecond),
		MaxPlanningRounds: 2,
		MaxReviewRounds:   3,
		RecoveryPolicy:    policy,
		WorkflowPhase:     feature.StatePlanning,
	}
}

func newTestCodex() *worker.ScriptedAdapter {
	return worker.NewQueuedScriptedAdapter("fake-codex", map[worker.Role][]worker.Script{
		worker.RoleLead: {
			completedScript("proposed an accepted plan"),
		},
		worker.RoleCoder: {
			completedScript("implemented the plan"),
			completedScript("addressed the review findings"),
		},
	})
}

func newTestClaude(reviewDispositions ...worker.Disposition) *worker.ScriptedAdapter {
	reviews := make([]worker.Script, 0, len(reviewDispositions))
	for _, disposition := range reviewDispositions {
		reviews = append(reviews, worker.Script{
			Events:      []worker.Event{{Type: worker.EventMessage, Text: "completed independent review"}},
			Disposition: disposition,
			Summary:     string(disposition),
		})
	}
	return worker.NewQueuedScriptedAdapter("fake-claude", map[worker.Role][]worker.Script{
		worker.RoleConsultant: {
			completedScript("accepted the refined plan"),
		},
		worker.RoleReviewer: reviews,
	})
}

type autoAdvancingAdapter struct {
	inner    *worker.ScriptedAdapter
	requests []worker.SessionRequest
}

func autoAdvance(inner *worker.ScriptedAdapter) *autoAdvancingAdapter {
	return &autoAdvancingAdapter{inner: inner}
}

func (a *autoAdvancingAdapter) Start(
	ctx context.Context,
	request worker.SessionRequest,
) (worker.Session, error) {
	a.requests = append(a.requests, request)
	session, err := a.inner.Start(ctx, request)
	if err != nil {
		return nil, err
	}
	go a.advanceUntilFinished(request.SessionID)
	return session, nil
}

func (a *autoAdvancingAdapter) Resume(
	ctx context.Context,
	request worker.ResumeRequest,
) (worker.Session, error) {
	return a.inner.Resume(ctx, request)
}

func (a *autoAdvancingAdapter) advanceUntilFinished(sessionID string) {
	for {
		err := a.inner.Advance(context.Background(), sessionID)
		if errors.Is(err, worker.ErrSessionFinished) {
			return
		}
		if err != nil {
			return
		}
	}
}

func completedScript(message string) worker.Script {
	return worker.Script{
		Events:      []worker.Event{{Type: worker.EventMessage, Text: message}},
		Disposition: worker.DispositionSucceeded,
		Summary:     message,
	}
}

func testRunRequest(
	codex worker.Adapter,
	claude worker.Adapter,
	maxReviewRounds int,
) RunRequest {
	return RunRequest{
		ID:        "run_test",
		FeatureID: "fea_test",
		Goal:      "Add a deterministic feature",
		Assignment: Assignment{
			Lead:       Agent{ID: "agt_codex", Adapter: codex},
			Consultant: Agent{ID: "agt_claude", Adapter: claude},
			Coder:      Agent{ID: "agt_codex", Adapter: codex},
			Reviewer:   Agent{ID: "agt_claude", Adapter: claude},
		},
		MaxPlanningRounds: 2,
		MaxReviewRounds:   maxReviewRounds,
	}
}

func orchestrationDatabase(
	t *testing.T,
) (*workflow.Service, *database.FeatureStore, *execution.Service) {
	t.Helper()
	db, err := database.OpenSQLite(
		t.Context(),
		filepath.Join(t.TempDir(), "coordinator.db"),
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, time.September, 8, 21, 0, 0, 0, time.UTC)
	projectStore := database.NewProjectStore(db)
	if err := projectStore.Create(t.Context(), project.Project{
		ID: "prj_test", Name: "Test", CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	featureStore := database.NewFeatureStore(db)
	if err := featureStore.Create(t.Context(), feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", Title: "Test",
		State: feature.StateDraft, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	return workflow.NewService(database.NewWorkflowStore(db)),
		featureStore,
		execution.NewService(database.NewExecutionStore(db))
}
