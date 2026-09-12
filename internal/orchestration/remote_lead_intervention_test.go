package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
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
	intervention execution.Intervention
	pauseCalled  bool
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

func TestRemoteLeadResumeCannotBypassUnresolvedInterventionEffect(t *testing.T) {
	executions := &interventionResumeExecutionStub{intervention: execution.Intervention{
		ID: "int_answered", Status: execution.InterventionStatusAnswered,
		Effect: worker.InterventionEffectReplanningRequired,
	}}
	starter := &RemoteLeadStarter{executions: executions}

	if _, _, err := starter.Resume(t.Context(), "run_test", "resume_test"); !errors.Is(err, ErrInterventionResolutionPending) {
		t.Fatalf("expected unresolved intervention error, got %v", err)
	}
	if executions.pauseCalled {
		t.Fatal("resume changed pause state before the intervention effect was resolved")
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
