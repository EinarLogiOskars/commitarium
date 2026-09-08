package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
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
}

type Runner struct {
	workflow   Workflow
	executions Execution
	sessions   SessionRegistry
}

var ErrInvalidRunRequest = errors.New("invalid orchestration run request")
var ErrUnexpectedDisposition = errors.New("unexpected worker disposition")
var ErrRunAlreadyActive = errors.New("orchestration run is already active")
var ErrStoredRunFailed = errors.New("orchestration run previously failed")
var ErrSessionAlreadyExists = errors.New("orchestration session already exists")

func NewRunner(
	workflowService Workflow,
	executions Execution,
	sessions SessionRegistry,
) *Runner {
	return &Runner{
		workflow: workflowService, executions: executions, sessions: sessions,
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

	go func() {
		_, _ = r.runStarted(context.WithoutCancel(ctx), request)
	}()
	return storedRun, true, nil
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

	_, err := r.executions.TransitionRun(
		context.WithoutCancel(ctx),
		runID,
		execution.RunStatusRunning,
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
	return nil
}

func (r *Runner) finishSession(
	ctx context.Context,
	sessionID string,
	status execution.SessionStatus,
	providerSessionID string,
) error {
	session, err := r.executions.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get final session state: %w", err)
	}
	if session.Status.IsTerminal() {
		if session.Status == status &&
			session.ProviderSessionID == providerSessionID {
			return nil
		}
		return execution.ErrStateConflict
	}
	if _, err := r.executions.TransitionSession(
		ctx,
		sessionID,
		session.Status,
		status,
		providerSessionID,
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
	if r.executions != nil {
		_, created, err := r.executions.CreateSession(
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
			return SessionSummary{}, fmt.Errorf(
				"%w: %q",
				ErrSessionAlreadyExists,
				sessionID,
			)
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
		FeatureID:    request.FeatureID,
		Role:         role,
		Instructions: instructions,
	})
	if err != nil {
		return SessionSummary{}, fail(fmt.Errorf("start %s session: %w", role, err))
	}
	if r.executions != nil {
		if _, err := r.executions.TransitionSession(
			ctx,
			sessionID,
			execution.SessionStatusStarting,
			execution.SessionStatusRunning,
			"",
		); err != nil {
			return SessionSummary{}, fail(fmt.Errorf(
				"mark %s session running: %w",
				role,
				err,
			))
		}
	}
	removeActive := func() {}
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
	for event := range session.Events() {
		if r.executions == nil {
			continue
		}
		eventNumber++
		eventID := sessionID + ":event:" + strconv.Itoa(eventNumber)
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
		if err := r.applySessionEvent(ctx, sessionID, event); err != nil {
			return SessionSummary{}, fail(fmt.Errorf(
				"apply %s session event: %w",
				role,
				err,
			))
		}
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
			workerResult.ProviderSessionID,
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
