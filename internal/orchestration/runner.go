package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

type SessionEvent struct {
	SessionID string
	AgentID   string
	Role      worker.Role
	Event     worker.Event
}

type EventSink interface {
	RecordSessionEvent(ctx context.Context, event SessionEvent) error
}

type Runner struct {
	workflow Workflow
	events   EventSink
}

var ErrInvalidRunRequest = errors.New("invalid orchestration run request")
var ErrUnexpectedDisposition = errors.New("unexpected worker disposition")

func NewRunner(workflowService Workflow, events EventSink) *Runner {
	return &Runner{workflow: workflowService, events: events}
}

func (r *Runner) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if err := request.Validate(); err != nil {
		return RunResult{}, err
	}

	result := RunResult{Sessions: make([]SessionSummary, 0)}
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

func (r *Runner) runAgent(
	ctx context.Context,
	request RunRequest,
	agent Agent,
	role worker.Role,
	instructions string,
	sessionSuffix string,
) (SessionSummary, error) {
	sessionID := request.ID + ":" + sessionSuffix
	session, err := agent.Adapter.Start(ctx, worker.SessionRequest{
		SessionID:    sessionID,
		FeatureID:    request.FeatureID,
		Role:         role,
		Instructions: instructions,
	})
	if err != nil {
		return SessionSummary{}, fmt.Errorf("start %s session: %w", role, err)
	}
	for event := range session.Events() {
		if r.events == nil {
			continue
		}
		if err := r.events.RecordSessionEvent(ctx, SessionEvent{
			SessionID: sessionID,
			AgentID:   agent.ID,
			Role:      role,
			Event:     event,
		}); err != nil {
			return SessionSummary{}, fmt.Errorf("record %s session event: %w", role, err)
		}
	}
	workerResult, err := session.Wait(ctx)
	if err != nil {
		return SessionSummary{}, fmt.Errorf("wait for %s session: %w", role, err)
	}
	if err := workerResult.Validate(); err != nil {
		return SessionSummary{}, fmt.Errorf("validate %s session result: %w", role, err)
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
