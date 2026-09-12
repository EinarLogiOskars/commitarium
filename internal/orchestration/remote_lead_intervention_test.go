package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type interventionResumeExecutionStub struct {
	RemoteLeadExecution
	intervention     execution.Intervention
	run              execution.Run
	pauseCalled      bool
	resolutionCalled bool
}

func (stub *interventionResumeExecutionStub) GetRun(
	context.Context,
	string,
) (execution.Run, error) {
	return stub.run, nil
}

func (stub *interventionResumeExecutionStub) ResolveInterventionGuidance(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (execution.Run, bool, error) {
	stub.resolutionCalled = true
	return stub.run, true, nil
}

func (stub *interventionResumeExecutionStub) GetLatestIntervention(
	context.Context,
	string,
) (execution.Intervention, error) {
	return stub.intervention, nil
}

func (stub *interventionResumeExecutionStub) ApplyRunPause(
	context.Context,
	string,
	string,
	execution.RunPauseAction,
) (execution.Run, bool, error) {
	stub.pauseCalled = true
	return execution.Run{}, false, nil
}

func TestRemoteLeadResumeCannotBypassUnansweredIntervention(t *testing.T) {
	executions := &interventionResumeExecutionStub{intervention: execution.Intervention{
		ID: "int_pending", Status: execution.InterventionStatusQueued,
	}}
	starter := &RemoteLeadStarter{executions: executions}

	if _, _, err := starter.Resume(t.Context(), "run_test", "resume_test"); !errors.Is(err, ErrInterventionPending) {
		t.Fatalf("expected pending intervention error, got %v", err)
	}
	if executions.pauseCalled {
		t.Fatal("resume changed pause state before the intervention was answered")
	}
}

func TestRemoteLeadResumeConsumesGuidanceBeforeContinuing(t *testing.T) {
	executions := &interventionResumeExecutionStub{
		intervention: execution.Intervention{
			ID: "int_answered", Status: execution.InterventionStatusAnswered,
			Effect: worker.InterventionEffectGuidanceApplied,
		},
		run: execution.Run{
			ID: "run_test", Status: execution.RunStatusWaitingForUser,
			WaitKind:       execution.RunWaitKindPhaseCheckpoint,
			AutonomyPolicy: project.AutonomyPolicyReviewEachPhase,
		},
	}
	starter := &RemoteLeadStarter{executions: executions}

	if _, applied, err := starter.Resume(t.Context(), "run_test", "resume_test"); err != nil || !applied {
		t.Fatalf("resume guidance: applied=%t err=%v", applied, err)
	}
	if !executions.resolutionCalled || executions.pauseCalled {
		t.Fatalf("resume did not use atomic guidance resolution: %+v", executions)
	}
}

func TestRemoteLeadResumeKeepsClarificationAndReplanningPaused(t *testing.T) {
	for _, test := range []struct {
		name   string
		effect worker.InterventionEffect
		want   error
	}{
		{name: "clarification", effect: worker.InterventionEffectClarificationRequired, want: ErrInterventionClarificationRequired},
		{name: "replanning", effect: worker.InterventionEffectReplanningRequired, want: ErrInterventionReplanningRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			executions := &interventionResumeExecutionStub{intervention: execution.Intervention{
				ID: "int_answered", Status: execution.InterventionStatusAnswered,
				Effect: test.effect,
			}}
			starter := &RemoteLeadStarter{executions: executions}

			if _, _, err := starter.Resume(t.Context(), "run_test", "resume_test"); !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if executions.pauseCalled || executions.resolutionCalled {
				t.Fatal("resume changed state before the non-guidance effect was safely handled")
			}
		})
	}
}

func TestRemoteLeadDeliversInterventionAndKeepsWorkflowPaused(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_intervention_delivery"
	run, session, checkpoint := prepareInterventionBoundary(
		t, executions, runID, storedFeature.ID, storedProject.AgentProviders, worker.RoleLead,
	)
	interventionID := "int_delivery"
	attemptID := interventionAttemptID(session.ID, interventionID)
	workerStub := interventionWorker(
		run, storedProject.ID, storedFeature.ID, session, attemptID,
		worker.InterventionEffectGuidanceApplied,
	)
	starter := interventionStarter(t, db, executions, workerStub, storedFeature)

	intervention, created, err := starter.QueueIntervention(
		t.Context(), run.ID, interventionID, worker.RoleLead,
		"Keep the public explanation concise.",
	)
	if err != nil || !created || (intervention.Status != execution.InterventionStatusQueued &&
		intervention.Status != execution.InterventionStatusBeingAnswered) {
		t.Fatalf("queue deliverable intervention: intervention=%+v created=%t err=%v", intervention, created, err)
	}
	answered := waitForInterventionStatus(
		t, executions, run.ID, execution.InterventionStatusAnswered,
	)
	if answered.AttemptID != attemptID ||
		answered.Effect != worker.InterventionEffectGuidanceApplied {
		t.Fatalf("unexpected answered intervention %+v", answered)
	}
	paused, err := executions.GetRun(t.Context(), run.ID)
	if err != nil || !paused.Paused || paused.Status != execution.RunStatusWaitingForUser ||
		paused.WaitKind != execution.RunWaitKindPaused {
		t.Fatalf("intervention resumed workflow: run=%+v err=%v", paused, err)
	}
	resting, err := executions.GetSession(t.Context(), session.ID)
	if err != nil || resting.Status != execution.SessionStatusWaitingForUser ||
		resting.ProviderSessionID != session.ProviderSessionID {
		t.Fatalf("target conversation did not return to rest: session=%+v err=%v", resting, err)
	}
	workerStub.mu.Lock()
	putRequests := append([]workerhttp.PutAttemptRequest(nil), workerStub.putRequests...)
	workerStub.mu.Unlock()
	if len(putRequests) != 1 || putRequests[0].Mode != workerhttp.AttemptModeResume ||
		putRequests[0].ProviderSessionID != session.ProviderSessionID ||
		putRequests[0].OutputContract != workerhttp.OutputContractIntervention ||
		putRequests[0].Assignment.Role != workerhttp.RoleLead {
		t.Fatalf("unexpected intervention resume request %+v", putRequests)
	}
	if checkpoint.AttemptID == attemptID {
		t.Fatal("test setup did not rotate the prior checkpoint")
	}
	events, err := executions.EventsForSession(t.Context(), session.ID)
	if err != nil || len(events) != 3 || events[0].Type != worker.EventUserMessage ||
		events[1].Type != worker.EventMessage || events[1].Text != "I will keep it concise." {
		t.Fatalf("unexpected intervention activity=%+v err=%v", events, err)
	}
}

func TestRemoteLeadRecoveryReattachesInterventionWithoutSendingAgain(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_intervention_recovery"
	run, session, checkpoint := prepareInterventionBoundary(
		t, executions, runID, storedFeature.ID, storedProject.AgentProviders, worker.RoleLead,
	)
	interventionID := "int_recovery"
	if _, _, err := executions.QueueIntervention(
		t.Context(), interventionID, run.ID, worker.RoleLead,
		"Explain the recovery state.",
	); err != nil {
		t.Fatalf("queue interrupted intervention: %v", err)
	}
	attemptID := interventionAttemptID(session.ID, interventionID)
	if admitted, err := executions.BeginInterventionTurn(
		t.Context(), interventionID, checkpoint, attemptID, interventionAnsweringReason,
	); err != nil || !admitted {
		t.Fatalf("admit interrupted intervention: admitted=%t err=%v", admitted, err)
	}
	workerStub := interventionWorker(
		run, storedProject.ID, storedFeature.ID, session, attemptID,
		worker.InterventionEffectClarificationRequired,
	)
	starter := interventionStarter(t, db, executions, workerStub, storedFeature)
	recoverable, err := executions.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("reload interrupted run: %v", err)
	}
	if err := starter.Recover(
		t.Context(), recoverable, storedFeature,
		project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover intervention: %v", err)
	}
	answered := waitForInterventionStatus(
		t, executions, run.ID, execution.InterventionStatusAnswered,
	)
	if answered.AttemptID != attemptID ||
		answered.Effect != worker.InterventionEffectClarificationRequired {
		t.Fatalf("unexpected recovered intervention %+v", answered)
	}
	workerStub.mu.Lock()
	putCalls := len(workerStub.putRequests)
	workerStub.mu.Unlock()
	if putCalls != 0 {
		t.Fatalf("recovery sent the intervention %d additional times", putCalls)
	}
}

func TestRemoteLeadRoutesInterventionToReviewerConversation(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_reviewer_intervention"
	run, session, _ := prepareInterventionBoundary(
		t, executions, runID, storedFeature.ID, storedProject.AgentProviders, worker.RoleReviewer,
	)
	interventionID := "int_reviewer_delivery"
	workerStub := interventionWorker(
		run, storedProject.ID, storedFeature.ID, session,
		interventionAttemptID(session.ID, interventionID),
		worker.InterventionEffectReplanningRequired,
	)
	starter := interventionStarter(t, db, executions, workerStub, storedFeature)
	if _, _, err := starter.QueueIntervention(
		t.Context(), run.ID, interventionID, worker.RoleReviewer,
		"This changes the agreed scope.",
	); err != nil {
		t.Fatalf("queue reviewer intervention: %v", err)
	}
	answered := waitForInterventionStatus(
		t, executions, run.ID, execution.InterventionStatusAnswered,
	)
	if answered.Target != worker.RoleReviewer ||
		answered.Effect != worker.InterventionEffectReplanningRequired {
		t.Fatalf("unexpected reviewer answer %+v", answered)
	}
	workerStub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), workerStub.putRequests...)
	workerStub.mu.Unlock()
	if len(requests) != 1 || requests[0].Assignment.Role != workerhttp.RoleReviewer ||
		requests[0].ProviderSessionID != session.ProviderSessionID {
		t.Fatalf("intervention was not routed to reviewer: %+v", requests)
	}
}

func TestRemoteLeadContinuesScopeChangeIntoVersionedReplanning(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	run, lead, checkpoint := prepareInterventionBoundary(
		t, executions, "run_replanning", storedFeature.ID,
		storedProject.AgentProviders, worker.RoleLead,
	)
	now := run.UpdatedAt.Add(time.Second)
	acceptedGoal := "Update the export workflow."
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE features
		 SET state = ?, accepted_goal = ?, goal_accepted_at = ?, updated_at = ?
		 WHERE id = ?`,
		feature.StateReviewing, acceptedGoal, now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), storedFeature.ID,
	); err != nil {
		t.Fatalf("prepare accepted feature: %v", err)
	}
	plan, err := executions.RecordSessionEventWithID(
		t.Context(), "sev_replanning_v1", lead.ID,
		worker.Event{Type: worker.EventPlanSubmitted, Text: "Original agreed plan"},
	)
	if err != nil {
		t.Fatalf("record original plan: %v", err)
	}
	if _, _, err := executions.LinkPlanningMessage(t.Context(), run.ID, plan.ID); err != nil {
		t.Fatalf("link original plan: %v", err)
	}
	baseline := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := db.ExecContext(
		t.Context(),
		`INSERT INTO feature_workspaces (
		    feature_id, id, project_id, repository_owner, repository_name,
		    base_branch, branch_name, base_commit_id, status, branch_created_at,
		    checkout_relative_path, checkout_created_at,
		    pull_request_number, pull_request_url, pull_request_recorded_at,
		    approved_commit_id, merge_ready_at, merge_commit_id, merged_at,
		    created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, ?, ?)`,
		storedFeature.ID, "wsp_replanning", storedProject.ID, "owner", "repository",
		"main", "commitarium/"+storedFeature.ID, baseline, "branch_ready",
		now.Format(time.RFC3339Nano), "wsp_replanning", now.Format(time.RFC3339Nano),
		9, "http://forgejo/owner/repository/pulls/9", now.Format(time.RFC3339Nano),
		baseline, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("create managed workspace: %v", err)
	}
	amendment := "Include a CSV export in the accepted scope."
	intervention, _, err := executions.QueueIntervention(
		t.Context(), "int_replanning", run.ID, worker.RoleLead, amendment,
	)
	if err != nil {
		t.Fatalf("queue scope change: %v", err)
	}
	answeredAt := time.Now().UTC()
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE run_interventions
		 SET status = ?, attempt_id = ?, effect = ?, answered_at = ?, updated_at = ?
		 WHERE id = ?`,
		execution.InterventionStatusAnswered, "att_scope_change",
		worker.InterventionEffectReplanningRequired,
		answeredAt.Format(time.RFC3339Nano), answeredAt.Format(time.RFC3339Nano), intervention.ID,
	); err != nil {
		t.Fatalf("answer scope change: %v", err)
	}
	version := 2
	nextAttemptID := replanningAttemptID(lead.ID, version, 1)
	oldReference := workerhttp.AttemptReference{
		SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
	}
	newReference := workerhttp.AttemptReference{
		SessionID: lead.ID, AttemptID: nextAttemptID,
	}
	assignment := workerhttp.Assignment{
		AgentProfileID: "codex-default", ProjectID: storedProject.ID,
		FeatureID: storedFeature.ID, Role: workerhttp.RoleLead,
		WorkspaceID: "wsp_replanning",
	}
	oldEndedAt := now
	oldTerminal := workerhttp.Attempt{
		AttemptReference: oldReference, Mode: workerhttp.AttemptModeResume,
		Assignment: assignment, ProviderSessionID: lead.ProviderSessionID,
		State: workerhttp.AttemptStateTerminal, LatestEventSequence: checkpoint.LastEventSequence,
		StartedAt: lead.StartedAt, UpdatedAt: oldEndedAt, EndedAt: &oldEndedAt,
		Result: &workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
			Summary: "Original planning finished.",
		},
	}
	startedAt := now.Add(2 * time.Second)
	endedAt := startedAt.Add(time.Second)
	initial := workerhttp.Attempt{
		AttemptReference: newReference, Mode: workerhttp.AttemptModeResume,
		Assignment: assignment, ProviderSessionID: lead.ProviderSessionID,
		State: workerhttp.AttemptStateRunning, StartedAt: startedAt, UpdatedAt: startedAt,
	}
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 2
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired,
		Summary: "Revised proposal is ready for review.",
	}
	workerStub := &conversationalRemoteLeadWorker{
		initial: map[workerhttp.AttemptReference]workerhttp.Attempt{newReference: initial},
		terminal: map[workerhttp.AttemptReference]workerhttp.Attempt{
			oldReference: oldTerminal, newReference: terminal,
		},
		events: map[workerhttp.AttemptReference][]workerhttp.Event{
			newReference: {
				{AttemptReference: newReference, Sequence: 1, Type: workerhttp.EventMessage,
					Text: "Revised plan proposal", OccurredAt: startedAt},
				{AttemptReference: newReference, Sequence: 2, Type: workerhttp.EventAttemptTerminal,
					Text: terminal.Result.Summary, OccurredAt: endedAt},
			},
		},
	}
	pullRequestAt := now
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_replanning", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "owner", RepositoryName: "repository", BaseBranch: "main",
		Branch: "commitarium/" + storedFeature.ID, BaseCommitID: baseline,
		Status: workspace.StatusBranchReady, BranchCreatedAt: &pullRequestAt,
		CheckoutRelativePath: "wsp_replanning", CheckoutCreatedAt: &pullRequestAt,
		PullRequestNumber: 9, PullRequestURL: "http://forgejo/owner/repository/pulls/9",
		PullRequestRecordedAt: &pullRequestAt, CreatedAt: now, UpdatedAt: now,
	}}
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db),
		Goals: workflowService, Planning: workflowService, Workspaces: workspaceStub,
		Worker: workerStub, Pump: &conversationalRemoteLeadPump{
			executions: executions, worker: workerStub,
		},
		Lifetime: t.Context(), AgentProfileID: "codex-default",
	})
	if err != nil {
		t.Fatalf("create replanning starter: %v", err)
	}

	active, applied, err := starter.Resume(t.Context(), run.ID, "resume_replanning")
	if err != nil || !applied || active.PlanVersion != 2 || active.Paused {
		t.Fatalf("continue into replanning: run=%+v applied=%t err=%v", active, applied, err)
	}
	var revisedEventID string
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, getErr := executions.GetRun(t.Context(), run.ID)
		messages, messagesErr := executions.PlanningMessagesForRun(t.Context(), run.ID)
		if getErr == nil && messagesErr == nil && current.Status == execution.RunStatusWaitingForUser &&
			current.PlanVersion == 2 && len(messages) == 2 {
			if messages[0].PlanVersion != 1 || messages[1].PlanVersion != 2 ||
				messages[1].Event.Text != "Revised plan proposal" {
				t.Fatalf("unexpected versioned planning history %+v", messages)
			}
			revisedEventID = messages[1].Event.ID
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replanning proposal did not settle: run=%+v messages=%+v run_err=%v messages_err=%v", current, messages, getErr, messagesErr)
		}
		time.Sleep(time.Millisecond)
	}
	workerStub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), workerStub.putRequests...)
	workerStub.mu.Unlock()
	if len(requests) != 1 || requests[0].Mode != workerhttp.AttemptModeResume ||
		requests[0].ProviderSessionID != lead.ProviderSessionID ||
		!strings.Contains(requests[0].Instructions, amendment) ||
		!strings.Contains(requests[0].Instructions, "Replanning baseline commit: "+baseline) {
		t.Fatalf("unexpected replanning request %+v", requests)
	}
	if workspaceStub.replanningCalls != 1 || workspaceStub.replanningEventID != plan.ID ||
		workspaceStub.replanningPlan != plan.Text || workspaceStub.publishCalls != 0 {
		t.Fatalf("replanning mutated the existing PR: %+v", workspaceStub)
	}

	// Model a coordinator stop after the worker event and terminal checkpoint
	// were durable, but before the revised proposal was linked into the shared
	// planning history and the run returned to its user checkpoint.
	if _, err := db.ExecContext(
		t.Context(), `DELETE FROM planning_messages WHERE session_event_id = ?`, revisedEventID,
	); err != nil {
		t.Fatalf("remove interrupted planning link: %v", err)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE sessions SET status = ?, updated_at = ? WHERE id = ?`,
		execution.SessionStatusRunning, time.Now().UTC().Format(time.RFC3339Nano), lead.ID,
	); err != nil {
		t.Fatalf("restore interrupted lead state: %v", err)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE runs SET status = ?, reason = ?, wait_kind = '', updated_at = ? WHERE id = ?`,
		execution.RunStatusRunning, replanningRunningReason,
		time.Now().UTC().Format(time.RFC3339Nano), run.ID,
	); err != nil {
		t.Fatalf("restore interrupted run state: %v", err)
	}
	recoverable, err := executions.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("load interrupted replanning run: %v", err)
	}
	if err := starter.Recover(
		t.Context(), recoverable, feature.Feature{
			ID: storedFeature.ID, ProjectID: storedFeature.ProjectID,
			State: feature.StatePlanning,
		}, project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover revised proposal: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, run.ID, execution.RunStatusWaitingForUser)
	recoveredRun, err := executions.GetRun(t.Context(), run.ID)
	if err != nil || recoveredRun.PlanVersion != 2 ||
		recoveredRun.Reason != replanningProposalReason {
		t.Fatalf("unexpected recovered replanning checkpoint: run=%+v err=%v", recoveredRun, err)
	}
	recoveredMessages, err := executions.PlanningMessagesForRun(t.Context(), run.ID)
	if err != nil || len(recoveredMessages) != 2 ||
		recoveredMessages[1].PlanVersion != 2 ||
		recoveredMessages[1].Event.ID != revisedEventID {
		t.Fatalf("revised proposal was not safely relinked: messages=%+v err=%v", recoveredMessages, err)
	}
	workerStub.mu.Lock()
	recoveredPutCount := len(workerStub.putRequests)
	workerStub.mu.Unlock()
	if recoveredPutCount != 1 {
		t.Fatalf("recovery launched the versioned attempt again: PUTs=%d", recoveredPutCount)
	}

	reviewer := prepareRestingReviewerForReplanning(
		t, executions, workerStub, run, storedProject, storedFeature,
	)
	addVersionedPlanningAttempt(
		workerStub, run.ID, storedProject.ID, storedFeature.ID,
		reviewer.ID, reviewer.ProviderSessionID, workerhttp.RoleReviewer,
		2, 1, workerhttp.EventMessage,
		"The revised proposal needs one compatibility test.",
	)
	addVersionedPlanningAttempt(
		workerStub, run.ID, storedProject.ID, storedFeature.ID,
		lead.ID, lead.ProviderSessionID, workerhttp.RoleLead,
		2, 2, workerhttp.EventPlanSubmitted,
		"Revised agreed implementation plan with compatibility coverage.",
	)
	addVersionedImplementationBlocker(
		workerStub, run.ID, storedProject.ID, storedFeature.ID,
		lead.ID, lead.ProviderSessionID, 2,
	)
	if _, err := db.ExecContext(
		t.Context(), `UPDATE runs SET autonomy_policy = ? WHERE id = ?`,
		project.AutonomyPolicyRunToCompletion, run.ID,
	); err != nil {
		t.Fatalf("enable automatic revised workflow: %v", err)
	}
	if _, started, err := starter.StartPlanningReview(
		t.Context(), run.ID, "start_replanning_review",
	); err != nil || !started {
		t.Fatalf("start existing reviewer for revised plan: started=%t err=%v", started, err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		current, getErr := executions.GetRun(t.Context(), run.ID)
		implementationCheckpoint, checkpointErr := executions.GetWorkerAttempt(t.Context(), lead.ID)
		if getErr == nil && checkpointErr == nil &&
			current.Status == execution.RunStatusWaitingForUser &&
			current.WaitKind == execution.RunWaitKindBlocker &&
			implementationCheckpoint.AttemptID == implementationAttemptForVersion(lead.ID, 2, 1) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic revised workflow did not reach implementation blocker: run=%+v checkpoint=%+v run_err=%v checkpoint_err=%v", current, implementationCheckpoint, getErr, checkpointErr)
		}
		time.Sleep(time.Millisecond)
	}

	versionedMessages, err := executions.PlanningMessagesForRun(t.Context(), run.ID)
	if err != nil || len(versionedMessages) != 4 ||
		versionedMessages[2].PlanVersion != 2 ||
		versionedMessages[2].Role != worker.RoleReviewer ||
		versionedMessages[3].PlanVersion != 2 ||
		versionedMessages[3].Event.Type != worker.EventPlanSubmitted {
		t.Fatalf("revised agreement history is incomplete: messages=%+v err=%v", versionedMessages, err)
	}
	if workspaceStub.revisedPublishCalls != 1 ||
		workspaceStub.publishedBaseline != baseline ||
		workspaceStub.publishedPlan != versionedMessages[3].Event.Text ||
		workspaceStub.publishCalls != 0 {
		t.Fatalf("revised plan was not appended to the existing PR: %+v", workspaceStub)
	}

	implementationCheckpoint, err := executions.GetWorkerAttempt(t.Context(), lead.ID)
	if err != nil || implementationCheckpoint.AttemptID !=
		implementationAttemptForVersion(lead.ID, 2, 1) {
		t.Fatalf("revised implementation reused an old attempt: checkpoint=%+v err=%v", implementationCheckpoint, err)
	}
	workerStub.mu.Lock()
	requests = append([]workerhttp.PutAttemptRequest(nil), workerStub.putRequests...)
	workerStub.mu.Unlock()
	if len(requests) != 4 || requests[1].Mode != workerhttp.AttemptModeResume ||
		requests[1].ProviderSessionID != reviewer.ProviderSessionID ||
		requests[2].Mode != workerhttp.AttemptModeResume ||
		requests[3].OutputContract != workerhttp.OutputContractImplementationLead ||
		!strings.Contains(requests[3].Instructions, amendment) {
		t.Fatalf("unexpected versioned reviewer/lead/implementation requests: %+v", requests)
	}
	if workspaceStub.revisedVerifyCalls != 1 || workspaceStub.publishedBaseline != baseline {
		t.Fatalf("implementation did not verify the revised PR plan: %+v", workspaceStub)
	}
}

func prepareRestingReviewerForReplanning(
	t *testing.T,
	executions *execution.Service,
	stub *conversationalRemoteLeadWorker,
	run execution.Run,
	storedProject project.Project,
	storedFeature feature.Feature,
) execution.Session {
	t.Helper()
	reviewerID := remoteReviewerSessionID(run.ID)
	reviewer, _, err := executions.CreateSession(
		t.Context(), reviewerID, run.ID,
		agentID(storedProject.AgentProviders.Reviewer, worker.RoleReviewer), worker.RoleReviewer,
	)
	if err != nil {
		t.Fatalf("create prior reviewer conversation: %v", err)
	}
	reviewer, err = executions.TransitionSession(
		t.Context(), reviewer.ID, execution.SessionStatusStarting,
		execution.SessionStatusRunning, "provider-reviewer-thread",
	)
	if err != nil {
		t.Fatalf("capture prior reviewer conversation: %v", err)
	}
	reviewer, err = executions.TransitionSession(
		t.Context(), reviewer.ID, execution.SessionStatusRunning,
		execution.SessionStatusWaitingForUser, reviewer.ProviderSessionID,
	)
	if err != nil {
		t.Fatalf("rest prior reviewer conversation: %v", err)
	}
	priorAttemptID := reviewer.ID + ":review:1"
	if _, _, err := executions.CreateWorkerAttempt(
		t.Context(), reviewer.ID, priorAttemptID,
	); err != nil {
		t.Fatalf("create prior reviewer checkpoint: %v", err)
	}
	now := time.Now().UTC()
	endedAt := now.Add(time.Second)
	reference := workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: priorAttemptID}
	stub.mu.Lock()
	stub.terminal[reference] = workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: storedProject.ID,
			FeatureID: storedFeature.ID, Role: workerhttp.RoleReviewer,
			WorkspaceID: "wsp_replanning",
		},
		ProviderSessionID: reviewer.ProviderSessionID,
		State:             workerhttp.AttemptStateTerminal,
		Result: &workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
			Summary: "Prior review completed.",
		},
		StartedAt: now, UpdatedAt: endedAt, EndedAt: &endedAt,
	}
	stub.mu.Unlock()
	return reviewer
}

func addVersionedPlanningAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
	sessionID string,
	providerSessionID string,
	role workerhttp.Role,
	version int,
	turn int,
	eventType workerhttp.EventType,
	text string,
) {
	reference := workerhttp.AttemptReference{
		SessionID: sessionID,
		AttemptID: planningAttemptForVersion(sessionID, version, turn),
	}
	now := time.Now().UTC()
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID,
			FeatureID: featureID, Role: role, WorkspaceID: "wsp_replanning",
		},
		ProviderSessionID: providerSessionID,
		State:             workerhttp.AttemptStateRunning, StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 2
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Completed a versioned planning turn.",
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{AttemptReference: reference, Sequence: 1, Type: eventType, Text: text, OccurredAt: now},
		{AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt},
	}
}

func addVersionedImplementationBlocker(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
	sessionID string,
	providerSessionID string,
	version int,
) {
	reference := workerhttp.AttemptReference{
		SessionID: sessionID,
		AttemptID: implementationAttemptForVersion(sessionID, version, 1),
	}
	now := time.Now().UTC()
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID,
			FeatureID: featureID, Role: workerhttp.RoleLead, WorkspaceID: "wsp_replanning",
		},
		ProviderSessionID: providerSessionID,
		State:             workerhttp.AttemptStateRunning, StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 2
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired,
		Summary: "Implementation paused for a test fixture.",
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage,
			Text: "I need the missing test fixture.", OccurredAt: now},
		{AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt},
	}
}

func prepareInterventionBoundary(
	t *testing.T,
	executions *execution.Service,
	runID string,
	featureID string,
	providers project.AgentProviders,
	role worker.Role,
) (execution.Run, execution.Session, execution.WorkerAttemptCheckpoint) {
	t.Helper()
	run, _, err := executions.CreateRun(
		t.Context(), runID, featureID,
		project.DefaultDialogueRoundLimit, project.DefaultDialogueRoundLimit,
		providers, project.DefaultMergePolicy(), project.DefaultAutonomyPolicy(),
	)
	if err != nil {
		t.Fatalf("create intervention run: %v", err)
	}
	sessionID := remoteLeadSessionID(run.ID)
	if role == worker.RoleReviewer {
		sessionID = remoteReviewerSessionID(run.ID)
	}
	provider := providers.Lead
	if role == worker.RoleReviewer {
		provider = providers.Reviewer
	}
	session, _, err := executions.CreateSession(
		t.Context(), sessionID, run.ID, agentID(provider, role), role,
	)
	if err != nil {
		t.Fatalf("create intervention session: %v", err)
	}
	session, err = executions.TransitionSession(
		t.Context(), session.ID, execution.SessionStatusStarting,
		execution.SessionStatusRunning, "provider-intervention-thread",
	)
	if err != nil {
		t.Fatalf("capture intervention provider session: %v", err)
	}
	session, err = executions.TransitionSession(
		t.Context(), session.ID, execution.SessionStatusRunning,
		execution.SessionStatusWaitingForUser, session.ProviderSessionID,
	)
	if err != nil {
		t.Fatalf("rest intervention session: %v", err)
	}
	checkpoint, _, err := executions.CreateWorkerAttempt(
		t.Context(), session.ID, session.ID+":turn:1",
	)
	if err != nil {
		t.Fatalf("create intervention checkpoint: %v", err)
	}
	run, err = executions.TransitionRun(
		t.Context(), run.ID, execution.RunStatusRunning,
		execution.RunStatusWaitingForUser, "Waiting for clarification.",
		execution.RunWaitKindClarification,
	)
	if err != nil {
		t.Fatalf("create intervention boundary: %v", err)
	}
	return run, session, checkpoint
}

func interventionWorker(
	run execution.Run,
	projectID string,
	featureID string,
	session execution.Session,
	attemptID string,
	effect worker.InterventionEffect,
) *conversationalRemoteLeadWorker {
	stub := &conversationalRemoteLeadWorker{
		initial:  make(map[workerhttp.AttemptReference]workerhttp.Attempt),
		terminal: make(map[workerhttp.AttemptReference]workerhttp.Attempt),
		events:   make(map[workerhttp.AttemptReference][]workerhttp.Event),
	}
	reference := workerhttp.AttemptReference{SessionID: session.ID, AttemptID: attemptID}
	now := run.UpdatedAt.Add(time.Second)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.Role(session.Role), WorkspaceID: "wsp_intervention",
		},
		ProviderSessionID: session.ProviderSessionID,
		State:             workerhttp.AttemptStateRunning, StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 2
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome:            workerhttp.OutcomeCompleted,
		Disposition:        workerhttp.DispositionSucceeded,
		Summary:            "I will keep it concise.",
		InterventionEffect: workerhttp.InterventionEffect(effect),
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage,
			Text: terminal.Result.Summary, OccurredAt: now},
		{AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt},
	}
	return stub
}

func interventionStarter(
	t *testing.T,
	db *sql.DB,
	executions *execution.Service,
	workerStub *conversationalRemoteLeadWorker,
	storedFeature feature.Feature,
) *RemoteLeadStarter {
	t.Helper()
	handler, err := workerhttp.NewServer(workerhttp.ServerConfig{
		BearerToken: remoteLeadTestToken, Provider: workerhttp.ProviderCodex,
		Capabilities: []workerhttp.Capability{
			workerhttp.CapabilityResume, workerhttp.CapabilityEventReplay,
		},
		MaxConcurrentAttempts: 1, EventSource: workerStub,
	}, workerStub)
	if err != nil {
		t.Fatalf("create intervention worker server: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := workerhttp.NewClient(workerhttp.ClientConfig{
		BaseURL: server.URL, BearerToken: remoteLeadTestToken,
		RequestTimeout: time.Second, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create intervention worker client: %v", err)
	}
	ingester := workeringest.NewService(executions, workeringest.FilterFunc(
		func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
			return event, nil
		},
	))
	createdAt := time.Now().UTC()
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_intervention", ProjectID: storedFeature.ProjectID,
		FeatureID: storedFeature.ID, CheckoutRelativePath: "wsp_intervention",
		CheckoutCreatedAt: &createdAt,
	}}
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db),
		Goals:      workflow.NewService(database.NewWorkflowStore(db)),
		Workspaces: workspaceStub, Worker: client,
		Pump:     workeringest.NewPump(executions, ingester, workeringest.NewHTTPAttemptSource(client)),
		Lifetime: t.Context(), AgentProfileID: "codex-default",
	})
	if err != nil {
		t.Fatalf("create intervention starter: %v", err)
	}
	return starter
}

func waitForInterventionStatus(
	t *testing.T,
	executions *execution.Service,
	runID string,
	want execution.InterventionStatus,
) execution.Intervention {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		intervention, err := executions.GetLatestIntervention(t.Context(), runID)
		if err == nil && intervention.Status == want {
			return intervention
		}
		if time.Now().After(deadline) {
			t.Fatalf("intervention did not reach %q: intervention=%+v err=%v", want, intervention, err)
		}
		time.Sleep(time.Millisecond)
	}
}
