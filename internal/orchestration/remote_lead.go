package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const (
	remoteLeadAgentID     = "codex-lead"
	remoteReviewerAgentID = "codex-reviewer"
)

type planningStage string

const (
	planningStageNone             planningStage = ""
	planningStageFirstReview      planningStage = "first_review"
	planningStageLeadRevision     planningStage = "lead_revision"
	planningStageReviewerDecision planningStage = "reviewer_decision"
)

var ErrPlanningNotAllowed = errors.New("planning cannot start from the current workflow state")

// RemoteLeadExecution is the durable coordinator state used by the first real
// lead turn. It deliberately contains no workflow transition: goal
// clarification leaves the feature in draft.
type RemoteLeadExecution interface {
	CreateRun(context.Context, string, string) (execution.Run, bool, error)
	CreateSession(context.Context, string, string, string, worker.Role) (execution.Session, bool, error)
	CreateWorkerAttempt(context.Context, string, string) (execution.WorkerAttemptCheckpoint, bool, error)
	GetRun(context.Context, string) (execution.Run, error)
	GetSession(context.Context, string) (execution.Session, error)
	ActiveSessionsForRun(context.Context, string) ([]execution.Session, error)
	EventsForSession(context.Context, string) ([]execution.Event, error)
	GetWorkerAttempt(context.Context, string) (execution.WorkerAttemptCheckpoint, error)
	TransitionRun(context.Context, string, execution.RunStatus, execution.RunStatus, string) (execution.Run, error)
	TransitionSession(context.Context, string, execution.SessionStatus, execution.SessionStatus, string) (execution.Session, error)
	RecordSessionEventWithID(context.Context, string, string, worker.Event) (execution.Event, error)
	GetCommand(context.Context, string) (execution.Command, error)
	PendingCommandsForSession(context.Context, string) ([]execution.Command, error)
	ResolveCommand(context.Context, string, execution.CommandStatus, string) (execution.Command, error)
	BeginWorkerTurn(context.Context, worker.Command, string, execution.WorkerAttemptCheckpoint, string, string) (execution.WorkerTurnAdmissionResult, bool, error)
	BeginAutonomousTurn(context.Context, string, execution.WorkerAttemptCheckpoint, string, string) (bool, error)
	BeginChainedTurn(context.Context, string, execution.WorkerAttemptCheckpoint, string, string) (bool, error)
	BeginNewSessionTurn(context.Context, string, string, string, worker.Role, string, string) (bool, error)
	LinkPlanningMessage(context.Context, string, string) (execution.PlanningMessage, bool, error)
	PlanningMessagesForRun(context.Context, string) ([]execution.PlanningMessage, error)
}

type RemoteLeadFeatureFinder interface {
	GetByID(context.Context, string) (feature.Feature, error)
}

type RemoteLeadGoalService interface {
	AcceptGoal(context.Context, string, string, string, workflow.Actor, string) (workflow.Event, error)
}

type RemoteLeadPlanningWorkflow interface {
	TransitionFeature(context.Context, string, feature.State, workflow.Actor, string) (workflow.Event, error)
}

type RemoteLeadWorkspaceService interface {
	Get(context.Context, string, string) (workspace.Workspace, error)
	Prepare(context.Context, string, string) (workspace.Workspace, bool, error)
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
	Features       RemoteLeadFeatureFinder
	Goals          RemoteLeadGoalService
	Planning       RemoteLeadPlanningWorkflow
	Workspaces     RemoteLeadWorkspaceService
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
	features       RemoteLeadFeatureFinder
	goals          RemoteLeadGoalService
	planning       RemoteLeadPlanningWorkflow
	workspaces     RemoteLeadWorkspaceService
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
	if config.Executions == nil || config.Features == nil || config.Goals == nil ||
		config.Worker == nil || config.Pump == nil {
		return nil, fmt.Errorf("%w: execution service, feature store, goal service, worker client, and event pump are required", ErrInvalidRunRequest)
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
		executions: config.Executions, features: config.Features, goals: config.Goals,
		planning: config.Planning, workspaces: config.Workspaces,
		worker: config.Worker, pump: config.Pump,
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
	request, err := starter.startRequest(runID, projectID, featureID, goal)
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
	activeSessions, err := starter.executions.ActiveSessionsForRun(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("find active real-agent session: %w", err)
	}
	var session execution.Session
	if len(activeSessions) == 1 {
		session = activeSessions[0]
	} else if len(activeSessions) == 0 {
		handled, idleErr := starter.recoverIdlePlanningRun(ctx, run, storedFeature)
		if idleErr != nil || handled {
			return idleErr
		}
		// Older lead-only states may have a running run at the edge of a
		// lifecycle update. Preserve their conservative recovery behavior.
		session, err = starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	} else {
		return fmt.Errorf("%w: run has multiple active real-agent sessions", ErrInvalidRunRequest)
	}
	if err != nil {
		return fmt.Errorf("load real-agent session: %w", err)
	}
	isLead := session.AgentID == remoteLeadAgentID && session.Role == worker.RoleLead
	isReviewer := session.AgentID == remoteReviewerAgentID && session.Role == worker.RoleReviewer
	if session.RunID != run.ID || (!isLead && !isReviewer) {
		return fmt.Errorf("%w: stored real-agent session does not match run", ErrInvalidRunRequest)
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("load lead worker attempt: %w", err)
	}
	request := remoteLeadRequest{
		runID:         run.ID,
		agentName:     "lead agent",
		waitingReason: waitingReasonForAttempt(session, checkpoint.AttemptID),
		planningStage: planningStageForAttempt(session, checkpoint.AttemptID),
		identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: session.ID, AttemptID: checkpoint.AttemptID,
		}},
	}
	if isReviewer {
		request.agentName = "reviewer"
	}
	pending, err := starter.executions.PendingCommandsForSession(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("load pending lead commands: %w", err)
	}
	if len(pending) > 1 {
		return fmt.Errorf("%w: lead session has multiple pending replies", ErrInvalidRunRequest)
	}
	if isReviewer && len(pending) != 0 {
		return fmt.Errorf("%w: reviewer session has an unexpected pending command", ErrInvalidRunRequest)
	}
	if isLead && len(pending) == 1 {
		if pending[0].Type != worker.CommandMessage ||
			replyAttemptID(session.ID, pending[0].ID) != checkpoint.AttemptID {
			return fmt.Errorf("%w: pending reply does not match the current worker attempt", ErrInvalidRunRequest)
		}
		request.commandID = pending[0].ID
	}
	if !starter.claim(run.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	go starter.reattach(request)
	return nil
}

// recoverIdlePlanningRun handles the safe points between provider turns. The
// run intentionally remains active across the lead-to-reviewer handoff, so a
// restart can distinguish unfinished automatic routing from a normal user gate.
func (starter *RemoteLeadStarter) recoverIdlePlanningRun(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
) (bool, error) {
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if errors.Is(err, execution.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("load reviewer session: %w", err)
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return true, fmt.Errorf("load lead session: %w", err)
	}
	leadCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return true, fmt.Errorf("load lead planning checkpoint: %w", err)
	}
	reviewerCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return true, fmt.Errorf("load reviewer planning checkpoint: %w", err)
	}
	if lead.Status != execution.SessionStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser {
		return true, fmt.Errorf("%w: idle planning run has a non-waiting session", ErrInvalidRunRequest)
	}

	switch {
	case leadCheckpoint.AttemptID == planningAttemptID(lead.ID) &&
		reviewerCheckpoint.AttemptID == planningAttemptID(reviewer.ID):
		// The first reviewer response is durable and no automatic correction
		// round has been requested yet.
		return true, starter.waitRun(
			ctx, run.ID, "The reviewer's first planning response is ready.",
		)
	case leadCheckpoint.AttemptID == planningRevisionAttemptID(lead.ID) &&
		reviewerCheckpoint.AttemptID == planningAttemptID(reviewer.ID):
		if !starter.claim(run.ID) {
			return true, fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
		}
		request, admitted, startErr := starter.startReviewerDecision(ctx, run, storedFeature)
		if startErr != nil {
			starter.release(run.ID)
			return true, startErr
		}
		if !admitted {
			starter.release(run.ID)
			return true, nil
		}
		go starter.launch(request)
		return true, nil
	case leadCheckpoint.AttemptID == planningRevisionAttemptID(lead.ID) &&
		reviewerCheckpoint.AttemptID == planningRevisionAttemptID(reviewer.ID):
		messages, listErr := starter.executions.PlanningMessagesForRun(ctx, run.ID)
		if listErr != nil {
			return true, listErr
		}
		if len(messages) != 4 || messages[3].Role != worker.RoleReviewer ||
			messages[3].Event.WorkerAttemptID != reviewerCheckpoint.AttemptID {
			return true, fmt.Errorf("%w: reviewer decision is not durably published", ErrInvalidRunRequest)
		}
		return true, starter.waitRun(
			ctx, run.ID, planningDecisionReason(messages[3].Event.Text),
		)
	default:
		return true, fmt.Errorf("%w: unsupported idle planning checkpoint pair", ErrInvalidRunRequest)
	}
}

type remoteLeadRequest struct {
	runID         string
	commandID     string
	agentName     string
	waitingReason string
	planningStage planningStage
	identity      workerhttp.MutationIdentity
	request       workerhttp.PutAttemptRequest
}

func (starter *RemoteLeadStarter) startRequest(
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
		runID:         runID,
		agentName:     "lead agent",
		waitingReason: "The lead agent is waiting for the user's response.",
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

func (starter *RemoteLeadStarter) replyRequest(
	run execution.Run,
	storedFeature feature.Feature,
	session execution.Session,
	command worker.Command,
) (remoteLeadRequest, error) {
	attemptID := replyAttemptID(session.ID, command.ID)
	request := remoteLeadRequest{
		runID: run.ID, commandID: command.ID,
		agentName:     "lead agent",
		waitingReason: "The lead agent is waiting for the user's response.",
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{
				SessionID: session.ID, AttemptID: attemptID,
			},
			IdempotencyKey: attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.agentProfileID,
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: starter.workspaceID,
			},
			Instructions:      remoteLeadReplyInstructions(command.Message),
			ProviderSessionID: session.ProviderSessionID,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func replyAttemptID(sessionID, commandID string) string {
	// Public idempotency keys are free-form, while worker IDs are bounded to a
	// small safe character set. A stable digest keeps the attempt ID valid and
	// guarantees that retrying one reply addresses the same worker attempt.
	digest := sha256.Sum256([]byte(commandID))
	return sessionID + ":reply:" + hex.EncodeToString(digest[:16])
}

func remoteLeadSessionID(runID string) string { return runID + ":lead" }

func remoteReviewerSessionID(runID string) string { return runID + ":reviewer" }

func planningAttemptID(sessionID string) string { return sessionID + ":planning:1" }

func planningRevisionAttemptID(sessionID string) string { return sessionID + ":planning:2" }

func planningStageForAttempt(session execution.Session, attemptID string) planningStage {
	switch {
	case session.Role == worker.RoleReviewer && attemptID == planningAttemptID(session.ID):
		return planningStageFirstReview
	case session.Role == worker.RoleLead && attemptID == planningRevisionAttemptID(session.ID):
		return planningStageLeadRevision
	case session.Role == worker.RoleReviewer && attemptID == planningRevisionAttemptID(session.ID):
		return planningStageReviewerDecision
	default:
		return planningStageNone
	}
}

func waitingReasonForAttempt(session execution.Session, attemptID string) string {
	if session.Role == worker.RoleReviewer && attemptID == planningAttemptID(session.ID) {
		return "The reviewer's first planning response is ready."
	}
	if attemptID == planningAttemptID(session.ID) {
		return "The lead planning proposal is ready for reviewer consultation."
	}
	if session.Role == worker.RoleReviewer && attemptID == planningRevisionAttemptID(session.ID) {
		return "The reviewer has decided whether to accept the revised plan."
	}
	if attemptID == planningRevisionAttemptID(session.ID) {
		return "The revised plan is ready."
	}
	return "The lead agent is waiting for the user's response."
}

func remoteLeadInstructions(goal string) string {
	return "You are the lead agent helping the user define a software-development goal. " +
		"This is goal clarification only: do not modify files, run destructive commands, " +
		"create commits, or begin implementation. Inspect the available project read-only " +
		"when useful. Restate your understanding, identify important ambiguity or risk, and " +
		"ask the user the smallest useful set of questions needed before planning. " +
		"The user's current goal is:\n\n" + goal
}

func remoteLeadReplyInstructions(message string) string {
	return "Continue the same goal-clarification conversation. This is still clarification only: " +
		"do not modify files, run destructive commands, create commits, or begin implementation. " +
		"Use the existing conversation context, incorporate the user's reply, and ask only the " +
		"next questions genuinely needed before planning. The user replied:\n\n" + message
}

// StartPlanning resumes the existing lead conversation in the feature's
// managed workspace. It deliberately produces only the lead proposal; the
// separate reviewer turn is the next bounded workflow slice.
func (starter *RemoteLeadStarter) StartPlanning(
	ctx context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(idempotencyKey) == "" ||
		starter.planning == nil || starter.workspaces == nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, false, err
	}
	session, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return execution.Run{}, false, err
	}
	if session.RunID != run.ID || session.AgentID != remoteLeadAgentID ||
		session.Role != worker.RoleLead || session.ProviderSessionID == "" {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if strings.TrimSpace(storedFeature.AcceptedGoal) == "" ||
		storedFeature.GoalAcceptedAt == nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	nextAttemptID := planningAttemptID(session.ID)
	if checkpoint.AttemptID == nextAttemptID {
		if storedFeature.State != feature.StatePlanning {
			return execution.Run{}, false, ErrPlanningNotAllowed
		}
		return run, false, nil
	}
	if run.Status != execution.RunStatusWaitingForUser ||
		session.Status != execution.SessionStatusWaitingForUser ||
		(storedFeature.State != feature.StateDraft && storedFeature.State != feature.StatePlanning) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}

	var prepared workspace.Workspace
	if storedFeature.State == feature.StateDraft {
		prepared, _, err = starter.workspaces.Prepare(
			ctx, storedFeature.ProjectID, storedFeature.ID,
		)
	} else {
		prepared, err = starter.workspaces.Get(
			ctx, storedFeature.ProjectID, storedFeature.ID,
		)
	}
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("prepare planning workspace: %w", err)
	}
	if !prepared.CheckoutReady() || !prepared.PullRequestReady() {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	prior, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: session.ID, AttemptID: checkpoint.AttemptID,
	})
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("confirm prior lead attempt: %w", err)
	}
	if err := validateCompletedTurn(session, checkpoint, prior); err != nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	request, err := starter.planningRequest(
		run, storedFeature, session, prepared, nextAttemptID,
	)
	if err != nil {
		return execution.Run{}, false, err
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if _, err := starter.planning.TransitionFeature(
		ctx, storedFeature.ID, feature.StatePlanning,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
		idempotencyKey,
	); err != nil {
		starter.release(run.ID)
		return execution.Run{}, false, err
	}
	admitted, err := starter.executions.BeginAutonomousTurn(
		ctx, session.ID, checkpoint, nextAttemptID,
		"The lead agent is inspecting the managed workspace and preparing a plan.",
	)
	if err != nil {
		starter.release(run.ID)
		if errors.Is(err, execution.ErrStateConflict) ||
			errors.Is(err, execution.ErrWorkerAttemptConflict) {
			return execution.Run{}, false, ErrPlanningNotAllowed
		}
		return execution.Run{}, false, err
	}
	if !admitted {
		starter.release(run.ID)
		storedRun, getErr := starter.executions.GetRun(ctx, run.ID)
		return storedRun, false, getErr
	}
	go starter.launch(request)
	startedRun, err := starter.executions.GetRun(ctx, run.ID)
	return startedRun, true, err
}

func (starter *RemoteLeadStarter) planningRequest(
	run execution.Run,
	storedFeature feature.Feature,
	session execution.Session,
	prepared workspace.Workspace,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID:         run.ID,
		agentName:     "lead agent",
		waitingReason: "The lead planning proposal is ready for reviewer consultation.",
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{
				SessionID: session.ID, AttemptID: attemptID,
			},
			IdempotencyKey: attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.agentProfileID,
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: session.ProviderSessionID,
			Instructions:      planningInstructions(storedFeature, prepared),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

// StartPlanningReview starts exactly one new reviewer conversation and passes
// it the lead's exact, already persisted planning proposal.
func (starter *RemoteLeadStarter) StartPlanningReview(
	ctx context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(idempotencyKey) == "" ||
		starter.workspaces == nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, false, err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if storedFeature.State != feature.StatePlanning ||
		strings.TrimSpace(storedFeature.AcceptedGoal) == "" || storedFeature.GoalAcceptedAt == nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}

	reviewerID := remoteReviewerSessionID(run.ID)
	if existing, getErr := starter.executions.GetSession(ctx, reviewerID); getErr == nil {
		if existing.RunID != run.ID || existing.AgentID != remoteReviewerAgentID ||
			existing.Role != worker.RoleReviewer {
			return execution.Run{}, false, execution.ErrRecordConflict
		}
		checkpoint, checkpointErr := starter.executions.GetWorkerAttempt(ctx, existing.ID)
		if checkpointErr != nil || checkpoint.AttemptID != planningAttemptID(existing.ID) {
			return execution.Run{}, false, execution.ErrRecordConflict
		}
		return run, false, nil
	} else if !errors.Is(getErr, execution.ErrNotFound) {
		return execution.Run{}, false, getErr
	}
	if run.Status != execution.RunStatusWaitingForUser {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}

	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return execution.Run{}, false, err
	}
	leadCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if lead.AgentID != remoteLeadAgentID || lead.Role != worker.RoleLead ||
		lead.Status != execution.SessionStatusWaitingForUser ||
		leadCheckpoint.AttemptID != planningAttemptID(lead.ID) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	prior, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: lead.ID, AttemptID: leadCheckpoint.AttemptID,
	})
	if err != nil || validateCompletedTurn(lead, leadCheckpoint, prior) != nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	proposal, err := starter.messageForAttempt(ctx, lead.ID, leadCheckpoint.AttemptID)
	if err != nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if _, _, err := starter.executions.LinkPlanningMessage(ctx, run.ID, proposal.ID); err != nil {
		return execution.Run{}, false, fmt.Errorf("publish lead planning proposal: %w", err)
	}

	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil || !prepared.CheckoutReady() || !prepared.PullRequestReady() {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	attemptID := planningAttemptID(reviewerID)
	request, err := starter.reviewerPlanningRequest(run, storedFeature, prepared, proposal.Text, attemptID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	admitted, err := starter.executions.BeginNewSessionTurn(
		ctx, reviewerID, run.ID, remoteReviewerAgentID, worker.RoleReviewer,
		attemptID, "The reviewer is inspecting the lead's planning proposal.",
	)
	if err != nil {
		starter.release(run.ID)
		if errors.Is(err, execution.ErrStateConflict) {
			return execution.Run{}, false, ErrPlanningNotAllowed
		}
		return execution.Run{}, false, err
	}
	if !admitted {
		starter.release(run.ID)
		storedRun, getErr := starter.executions.GetRun(ctx, run.ID)
		return storedRun, false, getErr
	}
	go starter.launch(request)
	startedRun, err := starter.executions.GetRun(ctx, run.ID)
	return startedRun, true, err
}

func (starter *RemoteLeadStarter) reviewerPlanningRequest(
	run execution.Run,
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	proposal string,
	attemptID string,
) (remoteLeadRequest, error) {
	sessionID := remoteReviewerSessionID(run.ID)
	request := remoteLeadRequest{
		runID: run.ID, agentName: "reviewer",
		waitingReason: "The reviewer's first planning response is ready.",
		planningStage: planningStageFirstReview,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":start",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeStart,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.agentProfileID,
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			Instructions: reviewerPlanningInstructions(storedFeature, prepared, proposal),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func reviewerPlanningInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	proposal string,
) string {
	return "You are the independent reviewer in a collaborative planning discussion. " +
		"Inspect the managed repository and current Git state before responding. Do not modify files, " +
		"install dependencies, create commits, push, or begin implementation. Challenge the lead's " +
		"proposal against the accepted goal and the actual repository. Identify missing steps, unsafe " +
		"assumptions, scope problems, and weak test coverage. Finish by clearly saying whether you accept " +
		"the proposal as written or what must change. Durable repository and workflow state are " +
		"authoritative over the supplied proposal.\n\nAccepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nBase commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\n\nLead's exact proposal:\n" + proposal
}

// StartPlanningRound runs one bounded correction round: the existing lead
// revises its proposal from the reviewer's response, then the existing
// reviewer decides whether that revision is acceptable.
func (starter *RemoteLeadStarter) StartPlanningRound(
	ctx context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(idempotencyKey) == "" ||
		starter.workspaces == nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, false, err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if storedFeature.State != feature.StatePlanning ||
		strings.TrimSpace(storedFeature.AcceptedGoal) == "" || storedFeature.GoalAcceptedAt == nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return execution.Run{}, false, err
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return execution.Run{}, false, err
	}
	leadCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	reviewerCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	nextLeadAttemptID := planningRevisionAttemptID(lead.ID)
	nextReviewerAttemptID := planningRevisionAttemptID(reviewer.ID)
	if leadCheckpoint.AttemptID == nextLeadAttemptID {
		if reviewerCheckpoint.AttemptID != planningAttemptID(reviewer.ID) &&
			reviewerCheckpoint.AttemptID != nextReviewerAttemptID {
			return execution.Run{}, false, execution.ErrRecordConflict
		}
		return run, false, nil
	}
	if reviewerCheckpoint.AttemptID == nextReviewerAttemptID {
		return execution.Run{}, false, execution.ErrRecordConflict
	}
	if run.Status != execution.RunStatusWaitingForUser ||
		lead.Status != execution.SessionStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser ||
		lead.ProviderSessionID == "" || reviewer.ProviderSessionID == "" ||
		lead.AgentID != remoteLeadAgentID || lead.Role != worker.RoleLead ||
		reviewer.AgentID != remoteReviewerAgentID || reviewer.Role != worker.RoleReviewer ||
		leadCheckpoint.AttemptID != planningAttemptID(lead.ID) ||
		reviewerCheckpoint.AttemptID != planningAttemptID(reviewer.ID) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if err := starter.confirmCompletedTurn(ctx, lead, leadCheckpoint); err != nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if err := starter.confirmCompletedTurn(ctx, reviewer, reviewerCheckpoint); err != nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	messages, err := starter.executions.PlanningMessagesForRun(ctx, run.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if len(messages) != 2 || messages[0].Role != worker.RoleLead ||
		messages[0].Event.WorkerAttemptID != leadCheckpoint.AttemptID ||
		messages[1].Role != worker.RoleReviewer ||
		messages[1].Event.WorkerAttemptID != reviewerCheckpoint.AttemptID {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil || !prepared.CheckoutReady() || !prepared.PullRequestReady() {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	request, err := starter.leadRevisionRequest(
		run, storedFeature, lead, prepared, messages[1].Event.Text, nextLeadAttemptID,
	)
	if err != nil {
		return execution.Run{}, false, err
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	admitted, err := starter.executions.BeginAutonomousTurn(
		ctx, lead.ID, leadCheckpoint, nextLeadAttemptID,
		"The lead is revising the plan from the reviewer's response.",
	)
	if err != nil {
		starter.release(run.ID)
		if errors.Is(err, execution.ErrStateConflict) ||
			errors.Is(err, execution.ErrWorkerAttemptConflict) {
			return execution.Run{}, false, ErrPlanningNotAllowed
		}
		return execution.Run{}, false, err
	}
	if !admitted {
		starter.release(run.ID)
		storedRun, getErr := starter.executions.GetRun(ctx, run.ID)
		return storedRun, false, getErr
	}
	go starter.launch(request)
	startedRun, err := starter.executions.GetRun(ctx, run.ID)
	return startedRun, true, err
}

func (starter *RemoteLeadStarter) leadRevisionRequest(
	run execution.Run,
	storedFeature feature.Feature,
	lead execution.Session,
	prepared workspace.Workspace,
	reviewerResponse string,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent",
		waitingReason: "The revised plan is ready.", planningStage: planningStageLeadRevision,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.agentProfileID,
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions:      leadRevisionInstructions(storedFeature, prepared, reviewerResponse),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func leadRevisionInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	reviewerResponse string,
) string {
	return "Continue the same planning conversation as the lead. The independent reviewer has " +
		"responded to your proposal. Inspect the managed repository and current Git state again before " +
		"answering. Do not modify files, install dependencies, commit, push, or begin implementation. " +
		"Address every material reviewer concern and provide a complete revised plan that can replace " +
		"the earlier proposal. If you disagree with a concern, explain why using repository evidence. " +
		"Durable repository and workflow state are authoritative over conversational memory.\n\n" +
		"Accepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nBase commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\n\nReviewer's exact response:\n" + reviewerResponse
}

func (starter *RemoteLeadStarter) startReviewerDecision(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
) (remoteLeadRequest, bool, error) {
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	nextAttemptID := planningRevisionAttemptID(reviewer.ID)
	if checkpoint.AttemptID == nextAttemptID {
		return remoteLeadRequest{}, false, nil
	}
	if reviewer.Status != execution.SessionStatusWaitingForUser ||
		reviewer.AgentID != remoteReviewerAgentID || reviewer.Role != worker.RoleReviewer ||
		reviewer.ProviderSessionID == "" || checkpoint.AttemptID != planningAttemptID(reviewer.ID) {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	if err := starter.confirmCompletedTurn(ctx, reviewer, checkpoint); err != nil {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	messages, err := starter.executions.PlanningMessagesForRun(ctx, run.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if len(messages) != 3 || messages[1].Role != worker.RoleReviewer ||
		messages[2].Role != worker.RoleLead ||
		messages[2].Event.WorkerAttemptID != planningRevisionAttemptID(remoteLeadSessionID(run.ID)) {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil || !prepared.CheckoutReady() || !prepared.PullRequestReady() {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	request, err := starter.reviewerDecisionRequest(
		run, storedFeature, reviewer, prepared,
		messages[1].Event.Text, messages[2].Event.Text, nextAttemptID,
	)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	admitted, err := starter.executions.BeginChainedTurn(
		ctx, reviewer.ID, checkpoint, nextAttemptID,
		"The reviewer is deciding whether to accept the revised plan.",
	)
	return request, admitted, err
}

func (starter *RemoteLeadStarter) reviewerDecisionRequest(
	run execution.Run,
	storedFeature feature.Feature,
	reviewer execution.Session,
	prepared workspace.Workspace,
	previousResponse string,
	revisedPlan string,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID: run.ID, agentName: "reviewer",
		waitingReason: "The reviewer has decided whether to accept the revised plan.",
		planningStage: planningStageReviewerDecision,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.agentProfileID,
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: reviewer.ProviderSessionID,
			Instructions: reviewerDecisionInstructions(
				storedFeature, prepared, previousResponse, revisedPlan,
			),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func reviewerDecisionInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	previousResponse string,
	revisedPlan string,
) string {
	return "Continue the same planning conversation as the independent reviewer. Inspect the managed " +
		"repository and current Git state again. Do not modify files, install dependencies, commit, push, " +
		"or begin implementation. Decide whether the lead's complete revised plan now satisfies the accepted " +
		"goal and resolves every material concern. Explain your decision, then finish with exactly one of " +
		"these lines: PLANNING_DECISION: ACCEPTED or PLANNING_DECISION: CHANGES_REQUESTED. Use the second " +
		"line if any material ambiguity, missing validation, unsafe assumption, or scope disagreement remains. " +
		"Durable repository and workflow state are authoritative over conversational memory.\n\n" +
		"Accepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nBase commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\n\nYour previous response:\n" + previousResponse +
		"\n\nLead's exact revised plan:\n" + revisedPlan
}

func planningDecisionReason(response string) string {
	accepted := false
	changesRequested := false
	for _, line := range strings.Split(response, "\n") {
		switch strings.TrimSpace(line) {
		case "PLANNING_DECISION: ACCEPTED":
			accepted = true
		case "PLANNING_DECISION: CHANGES_REQUESTED":
			changesRequested = true
		}
	}
	switch {
	case accepted && !changesRequested:
		return "The reviewer accepted the revised plan. It is ready to publish to Forgejo."
	case changesRequested && !accepted:
		return "The reviewer still requests planning changes. User input is required before another round."
	default:
		return "The reviewer did not provide one clear planning decision. User input is required."
	}
}

func (starter *RemoteLeadStarter) messageForAttempt(
	ctx context.Context,
	sessionID string,
	attemptID string,
) (execution.Event, error) {
	events, err := starter.executions.EventsForSession(ctx, sessionID)
	if err != nil {
		return execution.Event{}, err
	}
	var message execution.Event
	for _, event := range events {
		if event.WorkerAttemptID != attemptID || event.Type != worker.EventMessage {
			continue
		}
		// Providers may emit conversational preambles before their completed
		// response. Session activity keeps every message; the planning exchange
		// links the last completed message as the turn's final authored response.
		message = event
	}
	if message.ID == "" {
		return execution.Event{}, errors.New("provider turn has no final message")
	}
	return message, nil
}

func planningInstructions(storedFeature feature.Feature, prepared workspace.Workspace) string {
	return "The goal is now accepted and planning has begun. Continue as the same lead agent. " +
		"First inspect the managed repository and current Git state. Do not modify files, install " +
		"dependencies, create commits, push, or begin implementation. Produce a concrete proposed " +
		"implementation plan for a separate reviewer agent to challenge. Call out assumptions, risks, " +
		"likely files or components, and how the result should be tested. Durable repository and " +
		"workflow state are authoritative over conversational memory.\n\nAccepted goal:\n" +
		storedFeature.AcceptedGoal + "\n\nRepository: " + prepared.RepositoryOwner + "/" +
		prepared.RepositoryName + "\nBase branch: " + prepared.BaseBranch +
		"\nFeature branch: " + prepared.Branch + "\nBase commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL)
}

func (starter *RemoteLeadStarter) AcceptGoal(
	ctx context.Context,
	sessionID string,
	goal string,
	actor workflow.Actor,
	idempotencyKey string,
) (workflow.Event, error) {
	session, err := starter.executions.GetSession(ctx, sessionID)
	if err != nil {
		return workflow.Event{}, err
	}
	if session.AgentID != remoteLeadAgentID || session.Role != worker.RoleLead {
		return workflow.Event{}, workflow.ErrGoalAcceptanceNotAllowed
	}
	run, err := starter.executions.GetRun(ctx, session.RunID)
	if err != nil {
		return workflow.Event{}, err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return workflow.Event{}, err
	}
	return starter.goals.AcceptGoal(
		ctx, storedFeature.ID, session.ID, goal, actor, idempotencyKey,
	)
}

// SendCommand turns a public message command into a new resume attempt. It is
// intentionally narrower than the simulated controller: this real-agent slice
// does not claim that Codex can safely pause or accept messages mid-turn.
func (starter *RemoteLeadStarter) SendCommand(
	ctx context.Context,
	sessionID string,
	command worker.Command,
) (execution.Command, error) {
	if err := command.Validate(); err != nil {
		return execution.Command{}, fmt.Errorf("%w: %v", execution.ErrInvalidCommand, err)
	}
	if command.Type != worker.CommandMessage {
		return execution.Command{}, ErrCommandNotAllowed
	}
	if existing, err := starter.executions.GetCommand(ctx, command.ID); err == nil {
		if existing.SessionID != sessionID || existing.Type != command.Type ||
			existing.Message != command.Message {
			return execution.Command{}, execution.ErrCommandConflict
		}
		return existing, nil
	} else if !errors.Is(err, execution.ErrNotFound) {
		return execution.Command{}, fmt.Errorf("look up lead reply: %w", err)
	}

	session, err := starter.executions.GetSession(ctx, sessionID)
	if err != nil {
		return execution.Command{}, err
	}
	if session.AgentID != remoteLeadAgentID || session.Role != worker.RoleLead ||
		session.Status != execution.SessionStatusWaitingForUser ||
		session.ProviderSessionID == "" {
		return execution.Command{}, commandStateError(command.Type, session.Status)
	}
	run, err := starter.executions.GetRun(ctx, session.RunID)
	if err != nil {
		return execution.Command{}, err
	}
	if run.Status != execution.RunStatusWaitingForUser {
		return execution.Command{}, ErrCommandNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Command{}, fmt.Errorf("load lead feature: %w", err)
	}
	if storedFeature.State != feature.StateDraft {
		return execution.Command{}, ErrCommandNotAllowed
	}
	if storedFeature.AcceptedGoal != "" || storedFeature.GoalAcceptedAt != nil {
		return execution.Command{}, ErrCommandNotAllowed
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		return execution.Command{}, fmt.Errorf("load prior lead attempt: %w", err)
	}
	prior, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: session.ID, AttemptID: checkpoint.AttemptID,
	})
	if err != nil {
		return execution.Command{}, fmt.Errorf("confirm prior lead attempt: %w", err)
	}
	if err := validateCompletedTurn(session, checkpoint, prior); err != nil {
		return execution.Command{}, err
	}
	request, err := starter.replyRequest(run, storedFeature, session, command)
	if err != nil {
		return execution.Command{}, err
	}
	if !starter.claim(run.ID) {
		return execution.Command{}, ErrCommandNotAllowed
	}
	result, admitted, err := starter.executions.BeginWorkerTurn(
		ctx, command, session.ID, checkpoint, request.identity.AttemptID,
		"The lead agent is responding to the user's message.",
	)
	if err != nil {
		starter.release(run.ID)
		if errors.Is(err, execution.ErrStateConflict) ||
			errors.Is(err, execution.ErrWorkerAttemptConflict) {
			return execution.Command{}, ErrCommandNotAllowed
		}
		return execution.Command{}, err
	}
	if !admitted {
		starter.release(run.ID)
		return result.Command, nil
	}
	go starter.launch(request)
	return result.Command, nil
}

func (starter *RemoteLeadStarter) confirmCompletedTurn(
	ctx context.Context,
	session execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
) error {
	prior, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: session.ID, AttemptID: checkpoint.AttemptID,
	})
	if err != nil {
		return err
	}
	return validateCompletedTurn(session, checkpoint, prior)
}

func validateCompletedTurn(
	session execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	attempt workerhttp.Attempt,
) error {
	if err := attempt.Validate(); err != nil {
		return fmt.Errorf("%w: prior worker attempt is invalid: %v", ErrCommandNotAllowed, err)
	}
	if attempt.SessionID != session.ID || attempt.AttemptID != checkpoint.AttemptID ||
		attempt.State != workerhttp.AttemptStateTerminal || attempt.Result == nil ||
		attempt.Result.Outcome != workerhttp.OutcomeCompleted ||
		attempt.LatestEventSequence != checkpoint.LastEventSequence ||
		attempt.ProviderSessionID != session.ProviderSessionID {
		return fmt.Errorf("%w: prior turn is not safely completed and fully recorded", ErrCommandNotAllowed)
	}
	return nil
}

func (starter *RemoteLeadStarter) launch(request remoteLeadRequest) {
	defer starter.release(request.runID)
	starter.launchAdmitted(request)
}

// launchAdmitted runs a turn whose coordinator admission is already durable.
// It does not acquire or release the run claim, allowing one goroutine to keep
// ownership while handing a completed lead revision directly to the reviewer.
func (starter *RemoteLeadStarter) launchAdmitted(request remoteLeadRequest) {
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
	if err := starter.applyReply(ctx, request); err != nil {
		starter.requireReview(ctx, request, err)
		return
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
	if err := starter.applyReply(ctx, request); err != nil {
		starter.requireReview(ctx, request, err)
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
		request.identity.AttemptID+":recovery:existing-attempt",
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

func (starter *RemoteLeadStarter) applyReply(ctx context.Context, request remoteLeadRequest) error {
	if request.commandID == "" {
		return nil
	}
	_, err := starter.executions.ResolveCommand(
		ctx, request.commandID, execution.CommandStatusApplied, "",
	)
	if err != nil {
		return fmt.Errorf("mark lead reply applied: %w", err)
	}
	return nil
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
		var planningMessage execution.Event
		if request.planningStage != planningStageNone {
			planningMessage, err = starter.messageForAttempt(
				ctx, session.ID, request.identity.AttemptID,
			)
			if err != nil {
				starter.requireReview(ctx, request, err)
				return
			}
			if _, _, err = starter.executions.LinkPlanningMessage(
				ctx, request.runID, planningMessage.ID,
			); err != nil {
				starter.requireReview(ctx, request, err)
				return
			}
		}
		if session.Status == execution.SessionStatusPauseRequested {
			_, err = starter.executions.TransitionSession(
				ctx, session.ID, session.Status, execution.SessionStatusWaitingForUser, attempt.ProviderSessionID,
			)
		} else if session.Status == execution.SessionStatusRunning {
			_, err = starter.executions.TransitionSession(
				ctx, session.ID, session.Status, execution.SessionStatusWaitingForUser, attempt.ProviderSessionID,
			)
		}
		if err != nil {
			break
		}
		if request.planningStage == planningStageLeadRevision {
			run, loadErr := starter.executions.GetRun(ctx, request.runID)
			if loadErr != nil {
				err = loadErr
				break
			}
			storedFeature, loadErr := starter.features.GetByID(ctx, run.FeatureID)
			if loadErr != nil {
				err = loadErr
				break
			}
			next, admitted, startErr := starter.startReviewerDecision(ctx, run, storedFeature)
			if startErr != nil {
				err = startErr
				break
			}
			if admitted {
				starter.launchAdmitted(next)
			}
			return
		}
		waitingReason := request.waitingReason
		if request.planningStage == planningStageReviewerDecision {
			waitingReason = planningDecisionReason(planningMessage.Text)
		}
		err = starter.waitRun(ctx, request.runID, waitingReason)
	case workerhttp.OutcomeStopped:
		err = starter.failSession(ctx, session, execution.SessionStatusStopped, attempt, "The "+request.agentName+" was stopped.")
	case workerhttp.OutcomeFailed:
		err = starter.failSession(ctx, session, execution.SessionStatusFailed, attempt, "The "+request.agentName+" could not complete this turn.")
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
		ctx, request.identity.AttemptID+":recovery:review-required",
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
