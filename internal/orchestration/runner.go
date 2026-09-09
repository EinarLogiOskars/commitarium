package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

const coordinatorActorID = "coordinator"

type Workflow interface {
	TransitionFeature(
		ctx context.Context,
		featureID string,
		state feature.State,
		actor workflow.Actor,
		idempotencyKey string,
	) (workflow.Event, error)
}

type Agent struct {
	ID      string
	Adapter worker.Adapter
}

type Assignment struct {
	Lead       Agent
	Consultant Agent
	Coder      Agent
	Reviewer   Agent
}

type RunRequest struct {
	ID                string
	FeatureID         string
	Goal              string
	Assignment        Assignment
	MaxPlanningRounds int
	MaxReviewRounds   int
	RecoveryPolicy    project.RecoveryPolicy
	WorkflowPhase     feature.State
}

type RunStatus string

const (
	RunStatusReadyToMerge RunStatus = "ready_to_merge"
	RunStatusWaiting      RunStatus = "waiting_for_user"
	RunStatusStopped      RunStatus = "stopped"
)

type SessionSummary struct {
	SessionID string
	AgentID   string
	Role      worker.Role
	Result    worker.Result
}

type RunResult struct {
	Status         RunStatus
	Reason         string
	PlanningRounds int
	ReviewRounds   int
	Sessions       []SessionSummary
}

type Execution interface {
	CreateRun(ctx context.Context, id string, featureID string) (execution.Run, bool, error)
	TransitionRun(
		ctx context.Context,
		id string,
		expected execution.RunStatus,
		status execution.RunStatus,
		reason string,
	) (execution.Run, error)
	CreateSession(
		ctx context.Context,
		id string,
		runID string,
		agentID string,
		role worker.Role,
	) (execution.Session, bool, error)
	TransitionSession(
		ctx context.Context,
		id string,
		expected execution.SessionStatus,
		status execution.SessionStatus,
		providerSessionID string,
	) (execution.Session, error)
	RecordSessionEventWithID(
		ctx context.Context,
		id string,
		sessionID string,
		event worker.Event,
	) (execution.Event, error)
	GetSession(ctx context.Context, id string) (execution.Session, error)
	GetRun(ctx context.Context, id string) (execution.Run, error)
	CompleteSession(
		ctx context.Context,
		id string,
		expected execution.SessionStatus,
		status execution.SessionStatus,
		result worker.Result,
	) (execution.Session, error)
	BeginSessionRecovery(
		ctx context.Context,
		id string,
		expected execution.SessionStatus,
	) (execution.Session, error)
	EventsForSession(ctx context.Context, sessionID string) ([]execution.Event, error)
	PendingCommandsForSession(ctx context.Context, sessionID string) ([]execution.Command, error)
	ResolveCommand(
		ctx context.Context,
		id string,
		status execution.CommandStatus,
		errorMessage string,
	) (execution.Command, error)
}

type Runner struct {
	workflow     Workflow
	executions   Execution
	sessions     SessionRegistry
	activeRunsMu sync.Mutex
	activeRuns   map[string]struct{}
}

var ErrInvalidRunRequest = errors.New("invalid orchestration run request")
var ErrUnexpectedDisposition = errors.New("unexpected worker disposition")
var ErrRunAlreadyActive = errors.New("orchestration run is already active")
var ErrStoredRunFailed = errors.New("orchestration run previously failed")
var ErrSessionAlreadyExists = errors.New("orchestration session already exists")
var ErrRecoveryBlocked = errors.New("orchestration recovery requires user review")

func NewRunner(
	workflowService Workflow,
	executions Execution,
	sessions SessionRegistry,
) *Runner {
	return &Runner{
		workflow: workflowService, executions: executions, sessions: sessions,
		activeRuns: make(map[string]struct{}),
	}
}

func (r *Runner) Run(
	ctx context.Context,
	request RunRequest,
) (result RunResult, runErr error) {
	if err := request.Validate(); err != nil {
		return RunResult{}, err
	}

	if r.executions != nil {
		storedRun, created, err := r.executions.CreateRun(
			ctx,
			request.ID,
			request.FeatureID,
		)
		if err != nil {
			return RunResult{}, fmt.Errorf("begin run %q: %w", request.ID, err)
		}
		if !created {
			return resultForStoredRun(storedRun)
		}
	}
	return r.runStarted(ctx, request)
}

// Start durably admits a run before returning and then executes it outside the
// caller's cancellation scope. Retrying the same run ID returns the existing
// record without launching a second goroutine.
func (r *Runner) Start(
	ctx context.Context,
	request RunRequest,
) (execution.Run, bool, error) {
	if err := request.Validate(); err != nil {
		return execution.Run{}, false, err
	}
	if r.executions == nil {
		return execution.Run{}, false, errors.New("asynchronous run requires durable execution storage")
	}

	storedRun, created, err := r.executions.CreateRun(
		ctx,
		request.ID,
		request.FeatureID,
	)
	if err != nil || !created {
		return storedRun, created, err
	}
	if !r.claimRun(request.ID) {
		return execution.Run{}, false, fmt.Errorf("%w: %q", ErrRunAlreadyActive, request.ID)
	}

	go func() {
		defer r.releaseRun(request.ID)
		_, _ = r.runStarted(context.WithoutCancel(ctx), request)
	}()
	return storedRun, true, nil
}

// Recover resumes a durably active run without creating a second run record.
// Completed sessions are replayed from storage and only its one interrupted
// provider session is resumed.
func (r *Runner) Recover(ctx context.Context, request RunRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	storedRun, err := r.executions.GetRun(ctx, request.ID)
	if err != nil {
		return fmt.Errorf("load run %q for recovery: %w", request.ID, err)
	}
	if storedRun.FeatureID != request.FeatureID || storedRun.Status.IsTerminal() {
		return fmt.Errorf("%w: run %q is not recoverable", ErrInvalidRunRequest, request.ID)
	}
	if !r.claimRun(request.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, request.ID)
	}
	go func() {
		defer r.releaseRun(request.ID)
		_, _ = r.runStarted(context.WithoutCancel(ctx), request)
	}()
	return nil
}

func (r *Runner) claimRun(runID string) bool {
	r.activeRunsMu.Lock()
	defer r.activeRunsMu.Unlock()
	if _, exists := r.activeRuns[runID]; exists {
		return false
	}
	r.activeRuns[runID] = struct{}{}
	return true
}

func (r *Runner) releaseRun(runID string) {
	r.activeRunsMu.Lock()
	delete(r.activeRuns, runID)
	r.activeRunsMu.Unlock()
}

func (r *Runner) runStarted(
	ctx context.Context,
	request RunRequest,
) (result RunResult, runErr error) {
	result = RunResult{Sessions: make([]SessionSummary, 0)}
	if r.executions != nil {
		defer func() {
			if err := r.finishRun(ctx, request.ID, result, runErr); err != nil {
				if runErr != nil {
					runErr = errors.Join(runErr, err)
					return
				}
				result = RunResult{}
				runErr = err
			}
		}()
	}
	if err := r.transition(ctx, request, feature.StatePlanning); err != nil {
		return RunResult{}, err
	}

	planningAccepted := false
	planningFeedback := ""
	acceptedPlanSummary := ""
	for round := 1; round <= request.MaxPlanningRounds; round++ {
		result.PlanningRounds = round
		leadInstructions := fmt.Sprintf(
			"%s: draft a planning proposal for the accepted goal",
			request.Goal,
		)
		if planningFeedback != "" {
			leadInstructions += ". Address the consultant's prior feedback: " + planningFeedback
		}
		lead, err := r.runAgent(
			ctx,
			request,
			request.Assignment.Lead,
			worker.RoleLead,
			leadInstructions,
			fmt.Sprintf("plan-lead-%d", round),
		)
		if err != nil {
			return RunResult{}, err
		}
		result.Sessions = append(result.Sessions, lead)
		if status, reason, done, err := interpretPlanningResult(lead); done || err != nil {
			result.Status, result.Reason = status, reason
			return result, err
		}

		consultant, err := r.runAgent(
			ctx,
			request,
			request.Assignment.Consultant,
			worker.RoleConsultant,
			fmt.Sprintf(
				"%s: critique and refine the lead proposal: %s",
				request.Goal,
				lead.Result.Summary,
			),
			fmt.Sprintf("plan-consultant-%d", round),
		)
		if err != nil {
			return RunResult{}, err
		}
		result.Sessions = append(result.Sessions, consultant)
		if status, reason, done, err := interpretPlanningResult(consultant); done || err != nil {
			result.Status, result.Reason = status, reason
			return result, err
		}

		if lead.Result.Disposition == worker.DispositionSucceeded &&
			consultant.Result.Disposition == worker.DispositionSucceeded {
			planningAccepted = true
			acceptedPlanSummary = consultant.Result.Summary
			break
		}
		planningFeedback = consultant.Result.Summary
	}
	if !planningAccepted {
		result.Status = RunStatusWaiting
		result.Reason = "planning round limit reached without agent agreement"
		return result, nil
	}

	if err := r.transition(ctx, request, feature.StateImplementing); err != nil {
		return RunResult{}, err
	}
	implementation, err := r.runAgent(
		ctx,
		request,
		request.Assignment.Coder,
		worker.RoleCoder,
		fmt.Sprintf(
			"%s: implement the accepted plan: %s",
			request.Goal,
			acceptedPlanSummary,
		),
		"implementation",
	)
	if err != nil {
		return RunResult{}, err
	}
	result.Sessions = append(result.Sessions, implementation)
	if status, reason, done, err := interpretWorkResult(implementation, "implementation"); done || err != nil {
		result.Status, result.Reason = status, reason
		return result, err
	}

	if err := r.transition(ctx, request, feature.StateReviewing); err != nil {
		return RunResult{}, err
	}
	reviewContext := implementation.Result.Summary
	for round := 1; round <= request.MaxReviewRounds; round++ {
		result.ReviewRounds = round
		review, err := r.runAgent(
			ctx,
			request,
			request.Assignment.Reviewer,
			worker.RoleReviewer,
			fmt.Sprintf(
				"%s: independently review the implementation result: %s",
				request.Goal,
				reviewContext,
			),
			fmt.Sprintf("review-%d", round),
		)
		if err != nil {
			return RunResult{}, err
		}
		result.Sessions = append(result.Sessions, review)
		switch review.Result.Outcome {
		case worker.OutcomeStopped:
			result.Status = RunStatusStopped
			result.Reason = "reviewer session stopped"
			return result, nil
		case worker.OutcomeCompleted:
		default:
			return RunResult{}, fmt.Errorf("review session: %w", worker.ErrInvalidResult)
		}
		switch review.Result.Disposition {
		case worker.DispositionSucceeded:
			if err := r.transition(ctx, request, feature.StateReadyToMerge); err != nil {
				return RunResult{}, err
			}
			result.Status = RunStatusReadyToMerge
			return result, nil
		case worker.DispositionInputRequired:
			result.Status = RunStatusWaiting
			result.Reason = "reviewer requested user input"
			return result, nil
		case worker.DispositionChangesRequested:
			if round == request.MaxReviewRounds {
				result.Status = RunStatusWaiting
				result.Reason = "review round limit reached with unresolved findings"
				return result, nil
			}
		default:
			return RunResult{}, fmt.Errorf(
				"review session: %w %q",
				ErrUnexpectedDisposition,
				review.Result.Disposition,
			)
		}

		fix, err := r.runAgent(
			ctx,
			request,
			request.Assignment.Coder,
			worker.RoleCoder,
			fmt.Sprintf(
				"%s: address every in-scope review finding: %s",
				request.Goal,
				review.Result.Summary,
			),
			fmt.Sprintf("review-fix-%d", round),
		)
		if err != nil {
			return RunResult{}, err
		}
		result.Sessions = append(result.Sessions, fix)
		if status, reason, done, err := interpretWorkResult(fix, "review fix"); done || err != nil {
			result.Status, result.Reason = status, reason
			return result, err
		}
		reviewContext = fix.Result.Summary
	}

	return RunResult{}, errors.New("review loop exhausted unexpectedly")
}

func resultForStoredRun(run execution.Run) (RunResult, error) {
	result := RunResult{Reason: run.Reason, Sessions: make([]SessionSummary, 0)}
	switch run.Status {
	case execution.RunStatusWaitingForUser:
		result.Status = RunStatusWaiting
		return result, nil
	case execution.RunStatusSucceeded:
		result.Status = RunStatusReadyToMerge
		return result, nil
	case execution.RunStatusStopped:
		result.Status = RunStatusStopped
		return result, nil
	case execution.RunStatusRunning:
		return RunResult{}, fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	case execution.RunStatusFailed:
		return RunResult{}, fmt.Errorf("%w: %s", ErrStoredRunFailed, run.Reason)
	default:
		return RunResult{}, fmt.Errorf(
			"restore run %q: unrecognized status %q",
			run.ID,
			run.Status,
		)
	}
}

func (r *Runner) finishRun(
	ctx context.Context,
	runID string,
	result RunResult,
	runErr error,
) error {
	status := execution.RunStatusFailed
	reason := "orchestration failed"
	if runErr != nil {
		reason = runErr.Error()
		if errors.Is(runErr, ErrRecoveryBlocked) {
			status = execution.RunStatusWaitingForUser
		}
	} else {
		switch result.Status {
		case RunStatusReadyToMerge:
			status = execution.RunStatusSucceeded
			reason = ""
		case RunStatusWaiting:
			status = execution.RunStatusWaitingForUser
			reason = result.Reason
		case RunStatusStopped:
			status = execution.RunStatusStopped
			reason = result.Reason
		default:
			reason = "orchestration returned without a final status"
		}
	}

	storedRun, err := r.executions.GetRun(context.WithoutCancel(ctx), runID)
	if err != nil {
		return fmt.Errorf("get run %q before finishing: %w", runID, err)
	}
	if storedRun.Status == status {
		return nil
	}
	if storedRun.Status.IsTerminal() {
		return execution.ErrStateConflict
	}
	_, err = r.executions.TransitionRun(
		context.WithoutCancel(ctx),
		runID,
		storedRun.Status,
		status,
		reason,
	)
	if err != nil {
		return fmt.Errorf("finish run %q as %q: %w", runID, status, err)
	}
	return nil
}

func (request RunRequest) Validate() error {
	switch {
	case strings.TrimSpace(request.ID) == "":
		return fmt.Errorf("%w: run ID is required", ErrInvalidRunRequest)
	case strings.TrimSpace(request.FeatureID) == "":
		return fmt.Errorf("%w: feature ID is required", ErrInvalidRunRequest)
	case strings.TrimSpace(request.Goal) == "":
		return fmt.Errorf("%w: goal is required", ErrInvalidRunRequest)
	case request.MaxPlanningRounds < 1:
		return fmt.Errorf("%w: planning round limit must be positive", ErrInvalidRunRequest)
	case request.MaxReviewRounds < 1:
		return fmt.Errorf("%w: review round limit must be positive", ErrInvalidRunRequest)
	case request.WorkflowPhase != "" && !request.WorkflowPhase.IsValid():
		return fmt.Errorf("%w: workflow phase %q is not recognized", ErrInvalidRunRequest, request.WorkflowPhase)
	}
	if _, err := project.NormalizeRecoveryPolicy(request.RecoveryPolicy); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	for role, agent := range map[worker.Role]Agent{
		worker.RoleLead:       request.Assignment.Lead,
		worker.RoleConsultant: request.Assignment.Consultant,
		worker.RoleCoder:      request.Assignment.Coder,
		worker.RoleReviewer:   request.Assignment.Reviewer,
	} {
		if strings.TrimSpace(agent.ID) == "" || agent.Adapter == nil {
			return fmt.Errorf("%w: %s agent is required", ErrInvalidRunRequest, role)
		}
	}
	return nil
}

func (r *Runner) transition(
	ctx context.Context,
	request RunRequest,
	state feature.State,
) error {
	_, err := r.workflow.TransitionFeature(
		ctx,
		request.FeatureID,
		state,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
		request.ID+":state:"+string(state),
	)
	if err != nil {
		return fmt.Errorf("advance run %q to %q: %w", request.ID, state, err)
	}
	return nil
}

func (r *Runner) applySessionEvent(
	ctx context.Context,
	sessionID string,
	event worker.Event,
) error {
	var expected execution.SessionStatus
	var status execution.SessionStatus
	switch event.Type {
	case worker.EventPauseAcknowledged:
		expected = execution.SessionStatusPauseRequested
		status = execution.SessionStatusPaused
	case worker.EventContinued:
		expected = execution.SessionStatusPaused
		status = execution.SessionStatusRunning
	default:
		return nil
	}

	session, err := r.executions.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get session state: %w", err)
	}
	if session.Status == status {
		return nil
	}
	if session.Status != expected {
		return fmt.Errorf(
			"%w: event %q expected %q, found %q",
			execution.ErrStateConflict,
			event.Type,
			expected,
			session.Status,
		)
	}
	if _, err := r.executions.TransitionSession(
		ctx,
		sessionID,
		expected,
		status,
		"",
	); err != nil {
		return fmt.Errorf("transition from event %q: %w", event.Type, err)
	}
	if event.Type == worker.EventContinued {
		run, err := r.executions.GetRun(ctx, session.RunID)
		if err != nil {
			return fmt.Errorf("get run for continued session: %w", err)
		}
		if run.Status == execution.RunStatusWaitingForUser {
			if _, err := r.executions.TransitionRun(
				ctx,
				run.ID,
				execution.RunStatusWaitingForUser,
				execution.RunStatusRunning,
				"",
			); err != nil {
				return fmt.Errorf("resume run after recovery approval: %w", err)
			}
		}
	}
	return nil
}

func (r *Runner) finishSession(
	ctx context.Context,
	sessionID string,
	status execution.SessionStatus,
	result worker.Result,
) error {
	session, err := r.executions.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get final session state: %w", err)
	}
	if session.Status.IsTerminal() {
		if session.Status == status &&
			session.ProviderSessionID == result.ProviderSessionID &&
			session.Outcome == result.Outcome &&
			session.Disposition == result.Disposition &&
			session.Summary == result.Summary {
			return nil
		}
		return execution.ErrStateConflict
	}
	if _, err := r.executions.CompleteSession(
		ctx,
		sessionID,
		session.Status,
		status,
		result,
	); err != nil {
		return err
	}
	return nil
}

func (r *Runner) failSession(
	ctx context.Context,
	sessionID string,
	role worker.Role,
	cause error,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	session, err := r.executions.GetSession(cleanupCtx, sessionID)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("get failed %s session: %w", role, err))
	}
	if session.Status.IsTerminal() {
		return cause
	}
	if _, err := r.executions.TransitionSession(
		cleanupCtx,
		sessionID,
		session.Status,
		execution.SessionStatusFailed,
		"",
	); err != nil {
		return errors.Join(cause, fmt.Errorf("mark %s session failed: %w", role, err))
	}
	return cause
}

func (r *Runner) runAgent(
	ctx context.Context,
	request RunRequest,
	agent Agent,
	role worker.Role,
	instructions string,
	sessionSuffix string,
) (SessionSummary, error) {
	sessionID := request.ID + ":" + sessionSuffix
	var stored execution.Session
	if r.executions != nil {
		var created bool
		var err error
		stored, created, err = r.executions.CreateSession(
			ctx,
			sessionID,
			request.ID,
			agent.ID,
			role,
		)
		if err != nil {
			return SessionSummary{}, fmt.Errorf("create %s session record: %w", role, err)
		}
		if !created {
			if stored.Status.IsTerminal() {
				return storedSessionSummary(stored)
			}
			return r.resumeAgent(ctx, request, stored, agent, role, instructions)
		}
	}
	fail := func(cause error) error {
		if r.executions == nil {
			return cause
		}
		return r.failSession(ctx, sessionID, role, cause)
	}

	session, err := agent.Adapter.Start(ctx, worker.SessionRequest{
		SessionID:    sessionID,
		AttemptID:    sessionID, // The legacy in-process path has one attempt per session.
		FeatureID:    request.FeatureID,
		Role:         role,
		Instructions: instructions,
	})
	if err != nil {
		return SessionSummary{}, fail(fmt.Errorf("start %s session: %w", role, err))
	}
	providerSessionID := strings.TrimSpace(session.ProviderSessionID())
	if providerSessionID == "" {
		return SessionSummary{}, fail(fmt.Errorf(
			"start %s session: provider session ID was not available",
			role,
		))
	}
	if r.executions != nil {
		if _, err := r.executions.TransitionSession(
			ctx,
			sessionID,
			execution.SessionStatusStarting,
			execution.SessionStatusRunning,
			providerSessionID,
		); err != nil {
			return SessionSummary{}, fail(fmt.Errorf(
				"mark %s session running: %w",
				role,
				err,
			))
		}
	}
	return r.collectAgentSession(ctx, request, agent, role, sessionID, session, nil)
}

type resumedSessionState struct {
	attempt         int
	previousStatus  execution.SessionStatus
	pendingCommands int
}

func (r *Runner) resumeAgent(
	ctx context.Context,
	request RunRequest,
	stored execution.Session,
	agent Agent,
	role worker.Role,
	originalInstructions string,
) (SessionSummary, error) {
	fail := func(cause error) error {
		return r.failSession(ctx, stored.ID, role, cause)
	}
	if stored.Status == execution.SessionStatusStarting ||
		strings.TrimSpace(stored.ProviderSessionID) == "" {
		cause := fmt.Errorf(
			"%w: session %q has no durably confirmed provider identity",
			ErrRecoveryBlocked,
			stored.ID,
		)
		_ = r.recordRecoveryBlock(ctx, stored.ID, stored.RecoveryAttempt+1, cause.Error())
		return SessionSummary{}, fail(cause)
	}
	if stored.Status != execution.SessionStatusRunning &&
		stored.Status != execution.SessionStatusPauseRequested &&
		stored.Status != execution.SessionStatusPaused {
		return SessionSummary{}, fail(fmt.Errorf(
			"%w: session %q has unsupported state %q",
			ErrRecoveryBlocked,
			stored.ID,
			stored.Status,
		))
	}

	events, err := r.executions.EventsForSession(ctx, stored.ID)
	if err != nil {
		return SessionSummary{}, fail(fmt.Errorf("load completed session activity: %w", err))
	}
	pending, err := r.executions.PendingCommandsForSession(ctx, stored.ID)
	if err != nil {
		return SessionSummary{}, fail(fmt.Errorf("load pending session commands: %w", err))
	}
	recovering, err := r.executions.BeginSessionRecovery(ctx, stored.ID, stored.Status)
	if err != nil {
		return SessionSummary{}, fail(fmt.Errorf("claim interrupted %s session: %w", role, err))
	}

	workerEvents := make([]worker.Event, 0, len(events))
	for _, event := range events {
		workerEvents = append(workerEvents, worker.Event{Type: event.Type, Text: event.Text})
	}
	workerCommands := make([]worker.Command, 0, len(pending))
	for _, command := range pending {
		workerCommands = append(workerCommands, worker.Command{
			ID: command.ID, Type: command.Type, Message: command.Message,
		})
		if _, err := r.executions.ResolveCommand(
			context.WithoutCancel(ctx),
			command.ID,
			execution.CommandStatusRejected,
			"delivery outcome is unknown after coordinator restart; command was not replayed",
		); err != nil {
			return SessionSummary{}, fail(fmt.Errorf("quarantine pending command %q: %w", command.ID, err))
		}
	}

	briefing := recoveryBriefing(
		stored,
		request.WorkflowPhase,
		originalInstructions,
		events,
		pending,
	)
	session, err := agent.Adapter.Resume(ctx, worker.ResumeRequest{
		SessionRequest: worker.SessionRequest{
			SessionID: stored.ID,
			AttemptID: stored.ID, // Recovery resumes that same in-process attempt.
			FeatureID: request.FeatureID,
			Role:      role, Instructions: briefing,
		},
		ProviderSessionID: stored.ProviderSessionID,
		Recovery: worker.RecoveryContext{
			Briefing: briefing, CompletedEvents: workerEvents,
			PendingCommands: workerCommands,
			PreviousState:   string(stored.Status),
			WorkflowPhase:   string(request.WorkflowPhase),
		},
	})
	if err != nil {
		cause := fmt.Errorf("%w: resume %s session: %v", ErrRecoveryBlocked, role, err)
		_ = r.recordRecoveryBlock(ctx, stored.ID, recovering.RecoveryAttempt, cause.Error())
		return SessionSummary{}, fail(cause)
	}
	if session.ProviderSessionID() != stored.ProviderSessionID {
		cause := fmt.Errorf("%w: resumed provider identity changed", ErrRecoveryBlocked)
		_ = r.recordRecoveryBlock(ctx, stored.ID, recovering.RecoveryAttempt, cause.Error())
		return SessionSummary{}, fail(cause)
	}

	return r.collectAgentSession(ctx, request, agent, role, stored.ID, session, &resumedSessionState{
		attempt: recovering.RecoveryAttempt, previousStatus: stored.Status,
		pendingCommands: len(pending),
	})
}

func (r *Runner) collectAgentSession(
	ctx context.Context,
	request RunRequest,
	agent Agent,
	role worker.Role,
	sessionID string,
	session worker.Session,
	recovery *resumedSessionState,
) (SessionSummary, error) {
	fail := func(cause error) error {
		if r.executions == nil {
			return cause
		}
		return r.failSession(ctx, sessionID, role, cause)
	}
	removeActive := func() {}
	var err error
	if r.sessions != nil {
		removeActive, err = r.sessions.Register(sessionID, session)
		if err != nil {
			return SessionSummary{}, fail(fmt.Errorf(
				"register %s session: %w",
				role,
				err,
			))
		}
	}
	defer removeActive()
	eventNumber := 0
	if r.executions != nil {
		existing, err := r.executions.EventsForSession(ctx, sessionID)
		if err != nil {
			return SessionSummary{}, fail(fmt.Errorf("load session event position: %w", err))
		}
		eventNumber = len(existing)
	}
	assessmentSeen := recovery == nil
	for event := range session.Events() {
		if r.executions == nil {
			continue
		}
		eventID := ""
		if event.Type == worker.EventRecoveryAssessment && recovery != nil {
			eventID = sessionID + ":recovery:" + strconv.Itoa(recovery.attempt) + ":assessment"
		} else {
			eventNumber++
			eventID = sessionID + ":event:" + strconv.Itoa(eventNumber)
		}
		if _, err := r.executions.RecordSessionEventWithID(
			ctx,
			eventID,
			sessionID,
			event,
		); err != nil {
			return SessionSummary{}, fail(fmt.Errorf(
				"record %s session event: %w",
				role,
				err,
			))
		}
		if event.Type == worker.EventRecoveryAssessment && recovery != nil {
			if event.RecoveryAssessment == nil {
				return SessionSummary{}, fail(fmt.Errorf(
					"%w: provider omitted structured recovery assessment",
					ErrRecoveryBlocked,
				))
			}
			assessmentSeen = true
			if err := r.applyRecoveryAssessment(ctx, request, sessionID, session, event, *recovery); err != nil {
				return SessionSummary{}, fail(err)
			}
			continue
		}
		if !assessmentSeen {
			return SessionSummary{}, fail(fmt.Errorf(
				"%w: provider continued before publishing a recovery assessment",
				ErrRecoveryBlocked,
			))
		}
		if err := r.applySessionEvent(ctx, sessionID, event); err != nil {
			return SessionSummary{}, fail(fmt.Errorf(
				"apply %s session event: %w",
				role,
				err,
			))
		}
	}
	if !assessmentSeen {
		return SessionSummary{}, fail(fmt.Errorf(
			"%w: provider ended before publishing a recovery assessment",
			ErrRecoveryBlocked,
		))
	}
	workerResult, err := session.Wait(ctx)
	if err != nil {
		return SessionSummary{}, fail(fmt.Errorf("wait for %s session: %w", role, err))
	}
	if err := workerResult.Validate(); err != nil {
		return SessionSummary{}, fail(fmt.Errorf(
			"validate %s session result: %w",
			role,
			err,
		))
	}
	if r.executions != nil {
		status := execution.SessionStatusCompleted
		if workerResult.Outcome == worker.OutcomeStopped {
			status = execution.SessionStatusStopped
		}
		if err := r.finishSession(
			ctx,
			sessionID,
			status,
			workerResult,
		); err != nil {
			return SessionSummary{}, fail(fmt.Errorf(
				"finish %s session: %w",
				role,
				err,
			))
		}
	}
	return SessionSummary{
		SessionID: sessionID,
		AgentID:   agent.ID,
		Role:      role,
		Result:    workerResult,
	}, nil
}

func storedSessionSummary(session execution.Session) (SessionSummary, error) {
	if session.Status == execution.SessionStatusFailed {
		return SessionSummary{}, fmt.Errorf(
			"%w: stored session %q previously failed",
			ErrStoredRunFailed,
			session.ID,
		)
	}
	result := worker.Result{
		Outcome: session.Outcome, Disposition: session.Disposition,
		ProviderSessionID: session.ProviderSessionID, Summary: session.Summary,
	}
	if err := result.Validate(); err != nil {
		return SessionSummary{}, fmt.Errorf(
			"%w: stored session %q has no replayable result: %v",
			ErrRecoveryBlocked,
			session.ID,
			err,
		)
	}
	return SessionSummary{
		SessionID: session.ID, AgentID: session.AgentID,
		Role: session.Role, Result: result,
	}, nil
}

func (r *Runner) applyRecoveryAssessment(
	ctx context.Context,
	request RunRequest,
	sessionID string,
	liveSession worker.Session,
	event worker.Event,
	recovery resumedSessionState,
) error {
	stored, err := r.executions.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get recovering session: %w", err)
	}
	if stored.Status == execution.SessionStatusPauseRequested {
		stored, err = r.executions.TransitionSession(
			ctx,
			sessionID,
			execution.SessionStatusPauseRequested,
			execution.SessionStatusPaused,
			"",
		)
		if err != nil {
			return fmt.Errorf("pause at recovery assessment: %w", err)
		}
	}
	if stored.Status != execution.SessionStatusPaused {
		return fmt.Errorf(
			"%w: recovery assessment reached unexpected session state %q",
			ErrRecoveryBlocked,
			stored.Status,
		)
	}

	policy, err := project.NormalizeRecoveryPolicy(request.RecoveryPolicy)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRecoveryBlocked, err)
	}
	assessment := event.RecoveryAssessment
	requiresReview := policy == project.RecoveryPolicyApprovalRequired ||
		!assessment.Consistent ||
		assessment.RequiresUserReview ||
		recovery.pendingCommands > 0 ||
		recovery.previousStatus == execution.SessionStatusPauseRequested ||
		recovery.previousStatus == execution.SessionStatusPaused
	if requiresReview {
		reason := "recovery assessment for session " + sessionID + " requires user approval"
		if !assessment.Consistent {
			reason = "recovery assessment for session " + sessionID + " found contradictory durable state"
		} else if recovery.pendingCommands > 0 {
			reason = "recovery assessment for session " + sessionID + " found uncertain command delivery"
		} else if recovery.previousStatus == execution.SessionStatusPauseRequested ||
			recovery.previousStatus == execution.SessionStatusPaused {
			reason = "recovery assessment for session " + sessionID + " preserved the pre-restart pause"
		}
		return r.ensureRunWaiting(ctx, request.ID, reason)
	}

	if err := liveSession.Send(ctx, worker.Command{
		ID:   sessionID + ":recovery:" + strconv.Itoa(recovery.attempt) + ":continue",
		Type: worker.CommandContinue,
	}); err != nil {
		return fmt.Errorf("%w: automatically continue recovered session: %v", ErrRecoveryBlocked, err)
	}
	return nil
}

func (r *Runner) ensureRunWaiting(
	ctx context.Context,
	runID string,
	reason string,
) error {
	run, err := r.executions.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run for recovery gate: %w", err)
	}
	if run.Status == execution.RunStatusWaitingForUser {
		return nil
	}
	if run.Status != execution.RunStatusRunning {
		return fmt.Errorf("%w: run %q has status %q", ErrRecoveryBlocked, runID, run.Status)
	}
	if _, err := r.executions.TransitionRun(
		ctx,
		runID,
		execution.RunStatusRunning,
		execution.RunStatusWaitingForUser,
		reason,
	); err != nil {
		return fmt.Errorf("gate recovered run for user: %w", err)
	}
	return nil
}

func (r *Runner) recordRecoveryBlock(
	ctx context.Context,
	sessionID string,
	attempt int,
	reason string,
) error {
	_, err := r.executions.RecordSessionEventWithID(
		context.WithoutCancel(ctx),
		sessionID+":recovery:"+strconv.Itoa(attempt)+":blocked",
		sessionID,
		worker.Event{Type: worker.EventRecoveryAssessment, Text: reason},
	)
	return err
}

func recoveryBriefing(
	session execution.Session,
	phase feature.State,
	originalInstructions string,
	events []execution.Event,
	pending []execution.Command,
) string {
	var briefing strings.Builder
	fmt.Fprintf(
		&briefing,
		"Resume provider session %s for coordinator session %s. Inspect before modifying anything. Durable external state is authoritative. Current workflow phase: %s. Original task: %s. Reconcile restored conversation context; repository and worktree state; Git HEAD, status, and diff; interrupted tests or commands; completed session events and pending commands; and the Forgejo PR plan, review discussion, and decisions.",
		session.ProviderSessionID,
		session.ID,
		phase,
		originalInstructions,
	)
	fmt.Fprintf(&briefing, " Durable session events: %d.", len(events))
	for _, event := range events {
		fmt.Fprintf(&briefing, " [%d %s: %s]", event.Sequence, event.Type, event.Text)
	}
	fmt.Fprintf(&briefing, " Pending commands at interruption: %d.", len(pending))
	for _, command := range pending {
		fmt.Fprintf(&briefing, " [%s %s]", command.ID, command.Type)
	}
	briefing.WriteString(" For the simulated worker, repository, Git, tests, and Forgejo state are not provisioned and must be reported as not applicable. Publish a recovery assessment before continuing.")
	return briefing.String()
}

func interpretPlanningResult(
	session SessionSummary,
) (RunStatus, string, bool, error) {
	if session.Result.Outcome == worker.OutcomeStopped {
		return RunStatusStopped, string(session.Role) + " session stopped", true, nil
	}
	switch session.Result.Disposition {
	case worker.DispositionSucceeded, worker.DispositionChangesRequested:
		return "", "", false, nil
	case worker.DispositionInputRequired:
		return RunStatusWaiting, string(session.Role) + " requested user input", true, nil
	default:
		return "", "", false, fmt.Errorf(
			"%s session: %w %q",
			session.Role,
			ErrUnexpectedDisposition,
			session.Result.Disposition,
		)
	}
}

func interpretWorkResult(
	session SessionSummary,
	activity string,
) (RunStatus, string, bool, error) {
	if session.Result.Outcome == worker.OutcomeStopped {
		return RunStatusStopped, activity + " session stopped", true, nil
	}
	switch session.Result.Disposition {
	case worker.DispositionSucceeded:
		return "", "", false, nil
	case worker.DispositionInputRequired:
		return RunStatusWaiting, activity + " requested user input", true, nil
	default:
		return "", "", false, fmt.Errorf(
			"%s: %w %q",
			activity,
			ErrUnexpectedDisposition,
			session.Result.Disposition,
		)
	}
}
