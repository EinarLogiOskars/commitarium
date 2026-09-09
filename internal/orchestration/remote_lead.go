package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
)

const remoteLeadAgentID = "codex-lead"

// RemoteLeadExecution is the durable coordinator state used by the first real
// lead turn. It deliberately contains no workflow transition: goal
// clarification leaves the feature in draft.
type RemoteLeadExecution interface {
	CreateRun(context.Context, string, string) (execution.Run, bool, error)
	CreateSession(context.Context, string, string, string, worker.Role) (execution.Session, bool, error)
	CreateWorkerAttempt(context.Context, string, string) (execution.WorkerAttemptCheckpoint, bool, error)
	GetRun(context.Context, string) (execution.Run, error)
	GetSession(context.Context, string) (execution.Session, error)
	GetWorkerAttempt(context.Context, string) (execution.WorkerAttemptCheckpoint, error)
	TransitionRun(context.Context, string, execution.RunStatus, execution.RunStatus, string) (execution.Run, error)
	TransitionSession(context.Context, string, execution.SessionStatus, execution.SessionStatus, string) (execution.Session, error)
	RecordSessionEventWithID(context.Context, string, string, worker.Event) (execution.Event, error)
}

type RemoteLeadWorker interface {
	PutAttempt(context.Context, workerhttp.MutationIdentity, workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error)
	GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error)
}

type RemoteLeadPump interface {
	Run(context.Context, string) (workeringest.PumpResult, error)
}

type RemoteLeadConfig struct {
	Executions     RemoteLeadExecution
	Worker         RemoteLeadWorker
	Pump           RemoteLeadPump
	Lifetime       context.Context
	AgentProfileID string
	WorkspaceID    string
	ReportError    func(error)
}

// RemoteLeadStarter owns the deliberately small real-provider path used while
// a user and the lead agent clarify a draft goal. A deterministic session and
// attempt identity make replaying PutAttempt after a restart safe: the worker
// returns the existing attempt instead of launching a duplicate process.
type RemoteLeadStarter struct {
	executions     RemoteLeadExecution
	worker         RemoteLeadWorker
	pump           RemoteLeadPump
	lifetime       context.Context
	agentProfileID string
	workspaceID    string
	reportError    func(error)

	activeMu sync.Mutex
	active   map[string]struct{}
}

func NewRemoteLeadStarter(config RemoteLeadConfig) (*RemoteLeadStarter, error) {
	if config.Executions == nil || config.Worker == nil || config.Pump == nil {
		return nil, fmt.Errorf("%w: execution service, worker client, and event pump are required", ErrInvalidRunRequest)
	}
	if config.Lifetime == nil {
		return nil, fmt.Errorf("%w: lifetime context is required", ErrInvalidRunRequest)
	}
	if strings.TrimSpace(config.AgentProfileID) == "" || strings.TrimSpace(config.WorkspaceID) == "" {
		return nil, fmt.Errorf("%w: agent profile and workspace are required", ErrInvalidRunRequest)
	}
	reportError := config.ReportError
	if reportError == nil {
		reportError = func(error) {}
	}
	return &RemoteLeadStarter{
		executions: config.Executions, worker: config.Worker, pump: config.Pump,
		lifetime: config.Lifetime, agentProfileID: config.AgentProfileID,
		workspaceID: config.WorkspaceID, reportError: reportError,
		active: make(map[string]struct{}),
	}, nil
}

func (starter *RemoteLeadStarter) Start(
	ctx context.Context,
	runID string,
	projectID string,
	featureID string,
	goal string,
) (execution.Run, bool, error) {
	request, err := starter.request(runID, projectID, featureID, goal)
	if err != nil {
		return execution.Run{}, false, err
	}
	run, created, err := starter.executions.CreateRun(ctx, runID, featureID)
	if err != nil || !created {
		return run, created, err
	}
	sessionID := remoteLeadSessionID(runID)
	if _, _, err := starter.executions.CreateSession(
		ctx, sessionID, runID, remoteLeadAgentID, worker.RoleLead,
	); err != nil {
		starter.failAdmission(ctx, run, sessionID, fmt.Errorf("create lead session: %w", err))
		return execution.Run{}, false, err
	}
	if _, _, err := starter.executions.CreateWorkerAttempt(
		ctx, sessionID, request.identity.AttemptID,
	); err != nil {
		starter.failAdmission(ctx, run, sessionID, fmt.Errorf("create worker attempt: %w", err))
		return execution.Run{}, false, err
	}
	if !starter.claim(runID) {
		return execution.Run{}, false, fmt.Errorf("%w: %q", ErrRunAlreadyActive, runID)
	}
	go starter.launch(request)
	return run, true, nil
}

func (starter *RemoteLeadStarter) Recover(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	_ project.RecoveryPolicy,
) error {
	goal := storedFeature.Title
	if storedFeature.Description != "" {
		goal += ": " + storedFeature.Description
	}
	request, err := starter.request(run.ID, storedFeature.ProjectID, storedFeature.ID, goal)
	if err != nil {
		return err
	}
	session, err := starter.executions.GetSession(ctx, request.identity.SessionID)
	if err != nil {
		return fmt.Errorf("load lead session: %w", err)
	}
	if session.RunID != run.ID || session.AgentID != remoteLeadAgentID || session.Role != worker.RoleLead {
		return fmt.Errorf("%w: stored lead session does not match run", ErrInvalidRunRequest)
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("load lead worker attempt: %w", err)
	}
	if checkpoint.AttemptID != request.identity.AttemptID {
		return fmt.Errorf("%w: stored lead attempt does not match run", ErrInvalidRunRequest)
	}
	if !starter.claim(run.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	go starter.reattach(request)
	return nil
}

type remoteLeadRequest struct {
	runID    string
	identity workerhttp.MutationIdentity
	request  workerhttp.PutAttemptRequest
}

func (starter *RemoteLeadStarter) request(
	runID string,
	projectID string,
	featureID string,
	goal string,
) (remoteLeadRequest, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(projectID) == "" ||
		strings.TrimSpace(featureID) == "" || strings.TrimSpace(goal) == "" {
		return remoteLeadRequest{}, ErrInvalidRunRequest
	}
	sessionID := remoteLeadSessionID(runID)
	attemptID := sessionID + ":turn:1"
	request := remoteLeadRequest{
		runID: runID,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":start",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeStart,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.agentProfileID,
				ProjectID:      projectID, FeatureID: featureID,
				Role: workerhttp.RoleLead, WorkspaceID: starter.workspaceID,
			},
			Instructions: remoteLeadInstructions(goal),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func remoteLeadSessionID(runID string) string { return runID + ":lead" }

func remoteLeadInstructions(goal string) string {
	return "You are the lead agent helping the user define a software-development goal. " +
		"This is goal clarification only: do not modify files, run destructive commands, " +
		"create commits, or begin implementation. Inspect the available project read-only " +
		"when useful. Restate your understanding, identify important ambiguity or risk, and " +
		"ask the user the smallest useful set of questions needed before planning. " +
		"The user's current goal is:\n\n" + goal
}

func (starter *RemoteLeadStarter) launch(request remoteLeadRequest) {
	defer starter.release(request.runID)
	ctx := starter.lifetime
	attempt, _, putErr := starter.worker.PutAttempt(ctx, request.identity, request.request)
	if putErr != nil {
		// The PUT response may have been lost after the worker durably admitted
		// the attempt. Inspecting the same deterministic identity avoids guessing.
		inspected, inspectErr := starter.worker.GetAttempt(ctx, request.identity.AttemptReference)
		if inspectErr != nil {
			starter.requireReview(ctx, request, errors.Join(putErr, inspectErr))
			return
		}
		attempt = inspected
	}
	starter.observe(ctx, request, attempt)
}

func (starter *RemoteLeadStarter) reattach(request remoteLeadRequest) {
	defer starter.release(request.runID)
	ctx := starter.lifetime
	attempt, err := starter.worker.GetAttempt(ctx, request.identity.AttemptReference)
	if err != nil {
		starter.requireReview(ctx, request, fmt.Errorf("inspect interrupted worker attempt: %w", err))
		return
	}
	if attempt.State == workerhttp.AttemptStateIndeterminate {
		starter.requireReview(ctx, request, errors.New("worker reports an indeterminate provider attempt"))
		return
	}
	if err := starter.captureProviderSession(ctx, request.identity.SessionID, attempt); err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	// Recovery policy gates starting or resuming model work. Merely consuming
	// the original attempt's durable/live output is read-only and stays safe in
	// both policy modes.
	_, err = starter.executions.RecordSessionEventWithID(
		ctx,
		request.identity.SessionID+":recovery:existing-attempt",
		request.identity.SessionID,
		worker.Event{
			Type: worker.EventRecoveryAssessment,
			Text: "The coordinator found the original worker attempt and reattached to its event stream without starting another agent.",
			RecoveryAssessment: &worker.RecoveryAssessment{
				Consistent: true, RequiresUserReview: false,
			},
		},
	)
	if err != nil {
		starter.requireReview(ctx, request, fmt.Errorf("record recovery assessment: %w", err))
		return
	}
	starter.observe(ctx, request, attempt)
}

func (starter *RemoteLeadStarter) observe(
	ctx context.Context,
	request remoteLeadRequest,
	attempt workerhttp.Attempt,
) {
	if err := starter.captureProviderSession(ctx, request.identity.SessionID, attempt); err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	if attempt.State == workerhttp.AttemptStateIndeterminate {
		starter.requireReview(ctx, request, errors.New("worker reports an indeterminate provider attempt"))
		return
	}

	result, err := starter.pump.Run(ctx, request.identity.SessionID)
	if err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	if !result.AttemptInspected || result.Attempt.State != workerhttp.AttemptStateTerminal {
		starter.requireReview(ctx, request, errors.New("worker stream ended without a terminal attempt"))
		return
	}
	starter.finish(ctx, request, result.Attempt)
}

func (starter *RemoteLeadStarter) captureProviderSession(
	ctx context.Context,
	sessionID string,
	attempt workerhttp.Attempt,
) error {
	if attempt.ProviderSessionID == "" {
		return nil
	}
	session, err := starter.executions.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.ProviderSessionID != "" && session.ProviderSessionID != attempt.ProviderSessionID {
		return errors.New("worker provider session conflicts with durable coordinator state")
	}
	if session.Status != execution.SessionStatusStarting {
		return nil
	}
	_, err = starter.executions.TransitionSession(
		ctx, session.ID, session.Status, execution.SessionStatusRunning, attempt.ProviderSessionID,
	)
	return err
}

func (starter *RemoteLeadStarter) finish(
	ctx context.Context,
	request remoteLeadRequest,
	attempt workerhttp.Attempt,
) {
	if attempt.Result == nil {
		starter.requireReview(ctx, request, errors.New("terminal worker attempt has no result"))
		return
	}
	if err := starter.captureProviderSession(ctx, request.identity.SessionID, attempt); err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	session, err := starter.executions.GetSession(ctx, request.identity.SessionID)
	if err != nil {
		starter.requireReview(ctx, request, err)
		return
	}

	switch attempt.Result.Outcome {
	case workerhttp.OutcomeCompleted:
		if session.Status == execution.SessionStatusPauseRequested {
			_, err = starter.executions.TransitionSession(
				ctx, session.ID, session.Status, execution.SessionStatusWaitingForUser, attempt.ProviderSessionID,
			)
		} else if session.Status == execution.SessionStatusRunning {
			_, err = starter.executions.TransitionSession(
				ctx, session.ID, session.Status, execution.SessionStatusWaitingForUser, attempt.ProviderSessionID,
			)
		}
		if err == nil {
			err = starter.waitRun(ctx, request.runID, "The lead agent is waiting for the user's response.")
		}
	case workerhttp.OutcomeStopped:
		err = starter.failSession(ctx, session, execution.SessionStatusStopped, attempt, "The lead agent was stopped.")
	case workerhttp.OutcomeFailed:
		err = starter.failSession(ctx, session, execution.SessionStatusFailed, attempt, "The lead agent could not complete this turn.")
	default:
		err = fmt.Errorf("unsupported worker outcome %q", attempt.Result.Outcome)
	}
	if err != nil {
		starter.requireReview(ctx, request, err)
	}
}

func (starter *RemoteLeadStarter) failSession(
	ctx context.Context,
	session execution.Session,
	status execution.SessionStatus,
	attempt workerhttp.Attempt,
	publicReason string,
) error {
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, session.ID+":"+attempt.AttemptID+":coordinator-result", session.ID,
		worker.Event{Type: worker.EventActivity, Text: publicReason + " " + attempt.Result.Summary},
	); err != nil {
		return err
	}
	if !session.Status.IsTerminal() {
		if _, err := starter.executions.TransitionSession(
			ctx, session.ID, session.Status, status, attempt.ProviderSessionID,
		); err != nil {
			return err
		}
	}
	runStatus := execution.RunStatusFailed
	if status == execution.SessionStatusStopped {
		runStatus = execution.RunStatusStopped
	}
	run, err := starter.executions.GetRun(ctx, session.RunID)
	if err != nil || run.Status.IsTerminal() {
		return err
	}
	_, err = starter.executions.TransitionRun(ctx, run.ID, run.Status, runStatus, publicReason)
	return err
}

func (starter *RemoteLeadStarter) requireReview(
	ctx context.Context,
	request remoteLeadRequest,
	cause error,
) {
	starter.reportError(fmt.Errorf("observe real lead run %q: %w", request.runID, cause))
	text := "The coordinator could not safely confirm the real Codex turn's state. " +
		"It did not start a replacement agent. Check the worker and approve recovery before continuing."
	_, _ = starter.executions.RecordSessionEventWithID(
		ctx, request.identity.SessionID+":recovery:review-required",
		request.identity.SessionID,
		worker.Event{
			Type: worker.EventRecoveryAssessment, Text: text,
			RecoveryAssessment: &worker.RecoveryAssessment{Consistent: false, RequiresUserReview: true},
		},
	)
	session, err := starter.executions.GetSession(ctx, request.identity.SessionID)
	if err == nil && session.Status == execution.SessionStatusRunning {
		_, _ = starter.executions.TransitionSession(
			ctx, session.ID, session.Status, execution.SessionStatusPauseRequested, session.ProviderSessionID,
		)
	}
	_ = starter.waitRun(ctx, request.runID, text)
}

func (starter *RemoteLeadStarter) waitRun(ctx context.Context, runID string, reason string) error {
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil || run.Status == execution.RunStatusWaitingForUser || run.Status.IsTerminal() {
		return err
	}
	_, err = starter.executions.TransitionRun(
		ctx, run.ID, run.Status, execution.RunStatusWaitingForUser, reason,
	)
	return err
}

func (starter *RemoteLeadStarter) failAdmission(
	ctx context.Context,
	run execution.Run,
	sessionID string,
	cause error,
) {
	starter.reportError(cause)
	if session, err := starter.executions.GetSession(ctx, sessionID); err == nil && !session.Status.IsTerminal() {
		_, _ = starter.executions.TransitionSession(
			ctx, session.ID, session.Status, execution.SessionStatusFailed, session.ProviderSessionID,
		)
	}
	_, _ = starter.executions.TransitionRun(
		ctx, run.ID, run.Status, execution.RunStatusFailed, "The real lead turn could not be admitted.",
	)
}

func (starter *RemoteLeadStarter) claim(runID string) bool {
	starter.activeMu.Lock()
	defer starter.activeMu.Unlock()
	if _, exists := starter.active[runID]; exists {
		return false
	}
	starter.active[runID] = struct{}{}
	return true
}

func (starter *RemoteLeadStarter) release(runID string) {
	starter.activeMu.Lock()
	delete(starter.active, runID)
	starter.activeMu.Unlock()
}
