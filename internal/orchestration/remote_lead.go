package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const (
	remoteLeadAgentID                     = "codex-lead"
	remoteReviewerAgentID                 = "codex-reviewer"
	planningPlanSubmittedReason           = "The lead submitted the final plan after reaching agreement with the reviewer. It is ready to publish to Forgejo."
	planningPlanPublishedReason           = "The agreed implementation plan was published to Forgejo. It is ready for implementation."
	planningPublicationReviewReason       = "The coordinator could not safely confirm publication of the agreed plan to Forgejo. No agent or implementation work was started. Inspect the managed workspace and pull request before retrying."
	implementationRunningReason           = "The lead is implementing the agreed plan in the managed workspace."
	implementationContinuationReason      = "The lead is continuing implementation after the user's guidance."
	implementationVerificationReason      = "The coordinator is rechecking the implementation revision already published by the lead."
	implementationReadyReason             = "The lead needs user input before it can publish the implementation for review."
	implementationPublishedReason         = "The lead published its implementation commit and Forgejo audit entry. The verified revision is ready for automated review."
	implementationReviewRunningReason     = "The reviewer is inspecting the lead's exact implementation commit and preparing a formal Forgejo review."
	implementationCorrectionRunningReason = "The lead is addressing the verified review findings and publishing a corrected commit."
	implementationReadinessRunningReason  = "The lead is deciding whether it agrees with the reviewer's approval of the exact commit."
	implementationApprovedReason          = "The reviewer and lead agreed that the exact verified revision is ready for the merge gate."
	implementationMergedReason            = "Forgejo merged the exact commit approved by the reviewer and lead."
	implementationMergeBlockedReason      = "The exact approved revision could not be merged safely. Inspect the merge activity and Forgejo pull request before retrying."
	interventionAnsweringReason           = "The selected agent is answering the user's intervention while the workflow remains paused."
	interventionAnsweredReason            = "The selected agent answered the intervention. The workflow remains paused until the user explicitly continues it."
	replanningRunningReason               = "The lead is reconciling the preserved feature branch and proposing a revised plan."
	replanningProposalReason              = "The lead's revised planning proposal is ready for reviewer consultation."
	defaultLeadForgejoAuthor              = "codex-lead"
	defaultReviewerForgejoAuthor          = "codex-reviewer"
)

type planningStage string

type implementationReviewAction string

const (
	planningStageNone             planningStage = ""
	planningStageLeadProposal     planningStage = "lead_proposal"
	planningStageFirstReview      planningStage = "first_review"
	planningStageLeadResponse     planningStage = "lead_response"
	planningStageReviewerResponse planningStage = "reviewer_response"
)

const (
	implementationReviewActionAcknowledge implementationReviewAction = "acknowledge"
	implementationReviewActionCorrect     implementationReviewAction = "correct"
)

var ErrPlanningNotAllowed = errors.New("planning cannot start from the current workflow state")
var ErrImplementationNotAllowed = errors.New("implementation cannot start from the current workflow state")
var ErrRunControlNotAllowed = errors.New("run control is not allowed from the current workflow state")
var ErrRecoveryNotAllowed = errors.New("run is not waiting on a recovery blocker")
var ErrInterventionPending = errors.New("an intervention must be answered before the workflow can resume")
var ErrInterventionClarificationRequired = errors.New("the intervention answer requires more user clarification")
var ErrInterventionReplanningRequired = errors.New("the intervention answer requires a safe replanning decision")

// RemoteLeadExecution is the durable coordinator state used by the first real
// lead turn. It deliberately contains no workflow transition: goal
// clarification leaves the feature in draft.
type RemoteLeadExecution interface {
	CreateRunWithModels(context.Context, string, string, int, int, project.AgentProviders, project.AgentModels, project.MergePolicy, ...project.AutonomyPolicy) (execution.Run, bool, error)
	CreateSession(context.Context, string, string, string, worker.Role) (execution.Session, bool, error)
	CreateWorkerAttempt(context.Context, string, string) (execution.WorkerAttemptCheckpoint, bool, error)
	GetRun(context.Context, string) (execution.Run, error)
	GetSession(context.Context, string) (execution.Session, error)
	ActiveSessionsForRun(context.Context, string) ([]execution.Session, error)
	EventsForSession(context.Context, string) ([]execution.Event, error)
	GetWorkerAttempt(context.Context, string) (execution.WorkerAttemptCheckpoint, error)
	TransitionRun(context.Context, string, execution.RunStatus, execution.RunStatus, string, ...execution.RunWaitKind) (execution.Run, error)
	ApplyRunPause(context.Context, string, string, execution.RunPauseAction) (execution.Run, bool, error)
	QueueIntervention(context.Context, string, string, worker.Role, string) (execution.Intervention, bool, error)
	GetLatestIntervention(context.Context, string) (execution.Intervention, error)
	BeginInterventionTurn(context.Context, string, execution.WorkerAttemptCheckpoint, string, string) (bool, error)
	CompleteIntervention(context.Context, string, string, string, worker.InterventionEffect, string) (execution.Intervention, bool, error)
	ResolveInterventionGuidance(context.Context, string, string, string) (execution.Run, bool, error)
	BeginReplanningTurn(context.Context, string, string, string, int, string, string, string, execution.WorkerAttemptCheckpoint, string, string) (execution.Run, execution.PlanRevision, bool, error)
	GetPlanRevision(context.Context, string, int) (execution.PlanRevision, error)
	TransitionSession(context.Context, string, execution.SessionStatus, execution.SessionStatus, string) (execution.Session, error)
	RecordSessionEventWithID(context.Context, string, string, worker.Event) (execution.Event, error)
	GetCommand(context.Context, string) (execution.Command, error)
	PendingCommandsForSession(context.Context, string) ([]execution.Command, error)
	ResolveCommand(context.Context, string, execution.CommandStatus, string) (execution.Command, error)
	BeginWorkerTurn(context.Context, worker.Command, string, execution.WorkerAttemptCheckpoint, string, feature.State, string) (execution.WorkerTurnAdmissionResult, bool, error)
	BeginAutonomousTurn(context.Context, string, execution.WorkerAttemptCheckpoint, string, feature.State, string) (bool, error)
	BeginChainedTurn(context.Context, string, execution.WorkerAttemptCheckpoint, string, string) (bool, error)
	BeginChainedTurnInState(context.Context, string, execution.WorkerAttemptCheckpoint, string, feature.State, string) (bool, error)
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

type RemoteLeadArtifactService interface {
	GetFeatureArtifact(context.Context, string, featureartifact.Kind) (workflow.FeatureArtifact, error)
	UpsertGoalDraft(context.Context, string, featureartifact.GoalDraft, workflow.Actor, string) (workflow.FeatureArtifact, error)
	UpsertImplementationPlan(context.Context, string, featureartifact.ImplementationPlan, workflow.Actor, string) (workflow.FeatureArtifact, error)
}

type RemoteLeadWorkspaceService interface {
	Get(context.Context, string, string) (workspace.Workspace, error)
	PrepareForClarification(context.Context, string, string) (workspace.Workspace, bool, error)
	PublishPlan(context.Context, string, string, string, string) (workspace.Workspace, bool, error)
	PublishRevisedPlan(context.Context, string, string, string, string, string) (workspace.Workspace, bool, error)
	VerifyPublishedPlan(context.Context, string, string, string, string) (workspace.Workspace, error)
	VerifyRevisedPublishedPlan(context.Context, string, string, string, string, string) (workspace.Workspace, error)
	VerifyImplementationContinuation(context.Context, string, string, string, string) (workspace.Workspace, error)
	PrepareReplanningBaseline(context.Context, string, string, string, string) (workspace.Workspace, string, error)
	VerifyImplementationPublication(context.Context, string, string, string, string, string, string, string, int64, string) (workspace.Workspace, error)
	VerifyRevisedImplementationPublication(context.Context, string, string, string, string, string, string, string, int64, string, string) (workspace.Workspace, error)
	VerifyImplementationReviewResponse(context.Context, string, string, string, string, string, string, string, string, int64, string) (workspace.Workspace, error)
	VerifyImplementationMergeReadiness(context.Context, string, string, string, string, string, string, string, int64, string) (workspace.Workspace, error)
	VerifyImplementationReview(context.Context, string, string, string, string, string, string, string, int64, int64, string, string) (workspace.Workspace, error)
	RecordMergeReady(context.Context, string, string, string, int64) (workspace.Workspace, error)
	MergeApproved(context.Context, string, string) (workspace.Workspace, bool, error)
}

type RemoteLeadWorker interface {
	PutAttempt(context.Context, workerhttp.MutationIdentity, workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error)
	GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error)
}

type RemoteLeadPump interface {
	Run(context.Context, string) (workeringest.PumpResult, error)
}

type RemoteLeadEnvironmentRequests interface {
	Request(context.Context, projectenvironment.Request) (projectenvironment.Request, bool, error)
}

type RemoteLeadValidationGate interface {
	EnsureJob(context.Context, string, string, string, string, string) (validation.Job, bool, error)
	RequirePassed(context.Context, string, string) error
}

// EnsureValidationJob lets the UI create the pending job after a project that
// reached the merge gate without validation configuration is configured, and
// re-snapshot commands after the configuration changes.
func (starter *RemoteLeadStarter) EnsureValidationJob(
	ctx context.Context,
	runID string,
) (validation.Job, bool, error) {
	if starter.validation == nil || strings.TrimSpace(runID) == "" {
		return validation.Job{}, false, validation.ErrInvalid
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return validation.Job{}, false, err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return validation.Job{}, false, err
	}
	if storedFeature.State != feature.StateReadyToMerge || run.Status.IsTerminal() {
		return validation.Job{}, false, validation.ErrConflict
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return validation.Job{}, false, err
	}
	if !workspace.ValidCommitID(prepared.ApprovedCommitID) {
		return validation.Job{}, false, validation.ErrConflict
	}
	return starter.validation.EnsureJob(
		ctx, storedFeature.ProjectID, storedFeature.ID, run.ID, prepared.ID, prepared.ApprovedCommitID,
	)
}

type RemoteLeadConfig struct {
	Executions                   RemoteLeadExecution
	Features                     RemoteLeadFeatureFinder
	Goals                        RemoteLeadGoalService
	Planning                     RemoteLeadPlanningWorkflow
	Artifacts                    RemoteLeadArtifactService
	Workspaces                   RemoteLeadWorkspaceService
	Worker                       RemoteLeadWorker
	Pump                         RemoteLeadPump
	EnvironmentRequests          RemoteLeadEnvironmentRequests
	Validation                   RemoteLeadValidationGate
	Lifetime                     context.Context
	AgentProfileID               string
	ReviewerAgentProfileID       string
	ClaudeAgentProfileID         string
	ClaudeReviewerAgentProfileID string
	ForgejoAuthor                string
	ReviewerForgejoAuthor        string
	ClaudeForgejoAuthor          string
	ClaudeReviewerForgejoAuthor  string
	ReportError                  func(error)
}

// RemoteLeadStarter owns the deliberately small real-provider path used while
// a user and the lead agent clarify a draft goal. A deterministic session and
// attempt identity make replaying PutAttempt after a restart safe: the worker
// returns the existing attempt instead of launching a duplicate process.
type RemoteLeadStarter struct {
	executions                   RemoteLeadExecution
	features                     RemoteLeadFeatureFinder
	goals                        RemoteLeadGoalService
	planning                     RemoteLeadPlanningWorkflow
	artifacts                    RemoteLeadArtifactService
	workspaces                   RemoteLeadWorkspaceService
	worker                       RemoteLeadWorker
	pump                         RemoteLeadPump
	environmentRequests          RemoteLeadEnvironmentRequests
	validation                   RemoteLeadValidationGate
	lifetime                     context.Context
	agentProfileID               string
	reviewerAgentProfileID       string
	claudeAgentProfileID         string
	claudeReviewerAgentProfileID string
	forgejoAuthor                string
	reviewerForgejoAuthor        string
	claudeForgejoAuthor          string
	claudeReviewerForgejoAuthor  string
	reportError                  func(error)

	activeMu sync.Mutex
	active   map[string]struct{}
	mergeMu  sync.Mutex
}

func NewRemoteLeadStarter(config RemoteLeadConfig) (*RemoteLeadStarter, error) {
	if config.Executions == nil || config.Features == nil || config.Goals == nil ||
		config.Worker == nil || config.Pump == nil {
		return nil, fmt.Errorf("%w: execution service, feature store, goal service, worker client, and event pump are required", ErrInvalidRunRequest)
	}
	if config.Lifetime == nil {
		return nil, fmt.Errorf("%w: lifetime context is required", ErrInvalidRunRequest)
	}
	if strings.TrimSpace(config.AgentProfileID) == "" {
		return nil, fmt.Errorf("%w: agent profile is required", ErrInvalidRunRequest)
	}
	reportError := config.ReportError
	if reportError == nil {
		reportError = func(error) {}
	}
	forgejoAuthor := strings.TrimSpace(config.ForgejoAuthor)
	if forgejoAuthor == "" {
		forgejoAuthor = defaultLeadForgejoAuthor
	}
	reviewerAgentProfileID := strings.TrimSpace(config.ReviewerAgentProfileID)
	if reviewerAgentProfileID == "" {
		reviewerAgentProfileID = config.AgentProfileID
	}
	reviewerForgejoAuthor := strings.TrimSpace(config.ReviewerForgejoAuthor)
	if reviewerForgejoAuthor == "" {
		reviewerForgejoAuthor = defaultReviewerForgejoAuthor
	}
	claudeForgejoAuthor := strings.TrimSpace(config.ClaudeForgejoAuthor)
	if claudeForgejoAuthor == "" {
		claudeForgejoAuthor = "claude-lead"
	}
	claudeReviewerForgejoAuthor := strings.TrimSpace(config.ClaudeReviewerForgejoAuthor)
	if claudeReviewerForgejoAuthor == "" {
		claudeReviewerForgejoAuthor = "claude-reviewer"
	}
	return &RemoteLeadStarter{
		executions: config.Executions, features: config.Features, goals: config.Goals,
		planning: config.Planning, artifacts: config.Artifacts, workspaces: config.Workspaces,
		worker: config.Worker, pump: config.Pump,
		environmentRequests: config.EnvironmentRequests, validation: config.Validation,
		lifetime: config.Lifetime, agentProfileID: config.AgentProfileID,
		reviewerAgentProfileID:       reviewerAgentProfileID,
		claudeAgentProfileID:         strings.TrimSpace(config.ClaudeAgentProfileID),
		claudeReviewerAgentProfileID: strings.TrimSpace(config.ClaudeReviewerAgentProfileID),
		forgejoAuthor:                forgejoAuthor,
		reviewerForgejoAuthor:        reviewerForgejoAuthor,
		claudeForgejoAuthor:          claudeForgejoAuthor,
		claudeReviewerForgejoAuthor:  claudeReviewerForgejoAuthor,
		reportError:                  reportError,
		active:                       make(map[string]struct{}),
	}, nil
}

func agentID(provider project.AgentProvider, role worker.Role) string {
	return string(provider) + "-" + string(role)
}

func (starter *RemoteLeadStarter) profileID(provider project.AgentProvider, role worker.Role) string {
	switch {
	case provider == project.AgentProviderCodex && role == worker.RoleLead:
		return starter.agentProfileID
	case provider == project.AgentProviderCodex && role == worker.RoleReviewer:
		return starter.reviewerAgentProfileID
	case provider == project.AgentProviderClaude && role == worker.RoleLead:
		return starter.claudeAgentProfileID
	case provider == project.AgentProviderClaude && role == worker.RoleReviewer:
		return starter.claudeReviewerAgentProfileID
	default:
		return ""
	}
}

func (starter *RemoteLeadStarter) forgejoAuthorFor(run execution.Run, role worker.Role) string {
	provider := run.AgentProviders.Lead
	if role == worker.RoleReviewer {
		provider = run.AgentProviders.Reviewer
	}
	switch {
	case provider == project.AgentProviderCodex && role == worker.RoleLead:
		return starter.forgejoAuthor
	case provider == project.AgentProviderCodex && role == worker.RoleReviewer:
		return starter.reviewerForgejoAuthor
	case provider == project.AgentProviderClaude && role == worker.RoleLead:
		return starter.claudeForgejoAuthor
	case provider == project.AgentProviderClaude && role == worker.RoleReviewer:
		return starter.claudeReviewerForgejoAuthor
	default:
		return ""
	}
}

func (starter *RemoteLeadStarter) Start(
	ctx context.Context,
	runID string,
	projectID string,
	featureID string,
	goal string,
	dialogueLimits project.DialogueLimits,
	agentProviders project.AgentProviders,
	mergePolicy project.MergePolicy,
	autonomyPolicies ...project.AutonomyPolicy,
) (execution.Run, bool, error) {
	return starter.StartWithModels(
		ctx, runID, projectID, featureID, goal, dialogueLimits, agentProviders,
		project.AgentModels{}, mergePolicy, autonomyPolicies...,
	)
}

func (starter *RemoteLeadStarter) StartWithModels(
	ctx context.Context,
	runID string,
	projectID string,
	featureID string,
	goal string,
	dialogueLimits project.DialogueLimits,
	agentProviders project.AgentProviders,
	agentModels project.AgentModels,
	mergePolicy project.MergePolicy,
	autonomyPolicies ...project.AutonomyPolicy,
) (execution.Run, bool, error) {
	var err error
	mergePolicy, err = project.NormalizeMergePolicy(mergePolicy)
	if err != nil {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	if len(autonomyPolicies) > 1 {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	var autonomyPolicy project.AutonomyPolicy
	if len(autonomyPolicies) == 1 {
		autonomyPolicy = autonomyPolicies[0]
	}
	autonomyPolicy, err = project.NormalizeAutonomyPolicy(autonomyPolicy)
	if err != nil {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(projectID) == "" ||
		strings.TrimSpace(featureID) == "" || strings.TrimSpace(goal) == "" {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	if starter.workspaces == nil {
		return execution.Run{}, false, fmt.Errorf("%w: workspace service is required", ErrInvalidRunRequest)
	}
	prepared, _, err := starter.workspaces.PrepareForClarification(ctx, projectID, featureID)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("prepare goal-clarification workspace: %w", err)
	}
	if !prepared.CheckoutReady() {
		return execution.Run{}, false, fmt.Errorf("%w: goal-clarification checkout is not ready", ErrInvalidRunRequest)
	}
	agentProviders, err = agentProviders.Normalize()
	if err != nil {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	agentModels, err = agentModels.Normalize()
	if err != nil {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	request, err := starter.startRequest(runID, projectID, featureID, prepared.ID, goal, agentProviders)
	if err != nil {
		return execution.Run{}, false, err
	}
	run, created, err := starter.executions.CreateRunWithModels(
		ctx,
		runID,
		featureID,
		dialogueLimits.PlanningRounds,
		dialogueLimits.ImplementationReviewRounds,
		agentProviders,
		agentModels,
		mergePolicy,
		autonomyPolicy,
	)
	if err != nil || !created {
		return run, created, err
	}
	sessionID := remoteLeadSessionID(runID)
	if _, _, err := starter.executions.CreateSession(
		ctx, sessionID, runID, agentID(agentProviders.Lead, worker.RoleLead), worker.RoleLead,
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
	if storedFeature.State == feature.StateCompleted {
		_, _, err := starter.Merge(ctx, run.ID, run.ID+":recovery-merge")
		return err
	}
	var unfinished *execution.Intervention
	intervention, interventionErr := starter.executions.GetLatestIntervention(ctx, run.ID)
	if interventionErr == nil && intervention.Status != execution.InterventionStatusAnswered {
		unfinished = &intervention
	} else if interventionErr != nil && !errors.Is(interventionErr, execution.ErrNotFound) {
		return fmt.Errorf("load unfinished intervention: %w", interventionErr)
	}
	if unfinished != nil && unfinished.Status == execution.InterventionStatusQueued {
		_, err := starter.advanceIntervention(ctx, run.ID)
		return err
	}
	// Versions before accepted-goal auto-planning left this durable boundary
	// labeled as clarification. Repair that label before dispatching so an
	// upgraded coordinator can resume the same run without another user click.
	if run.Status == execution.RunStatusWaitingForUser && !run.Paused &&
		run.WaitKind == execution.RunWaitKindClarification &&
		run.AutonomyPolicy == project.AutonomyPolicyRunToCompletion &&
		storedFeature.State == feature.StateDraft &&
		strings.TrimSpace(storedFeature.AcceptedGoal) != "" &&
		storedFeature.GoalAcceptedAt != nil {
		if err := starter.waitRun(
			ctx, run.ID, workflow.GoalAcceptedPlanningReason,
			execution.RunWaitKindPhaseCheckpoint,
		); err != nil {
			return err
		}
		refreshedRun, err := starter.executions.GetRun(ctx, run.ID)
		if err != nil {
			return err
		}
		run = refreshedRun
	}
	if run.Status == execution.RunStatusWaitingForUser && !run.Paused &&
		run.WaitKind == execution.RunWaitKindPhaseCheckpoint &&
		run.AutonomyPolicy == project.AutonomyPolicyRunToCompletion {
		return starter.advanceWaitingRun(ctx, run.ID)
	}
	activeSessions, err := starter.executions.ActiveSessionsForRun(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("find active real-agent session: %w", err)
	}
	if len(activeSessions) == 0 && run.Paused {
		if unfinished != nil && unfinished.Status == execution.InterventionStatusBeingAnswered {
			return fmt.Errorf("%w: answering intervention has no active session", ErrInvalidRunRequest)
		}
		if err := starter.waitRun(
			ctx, run.ID,
			"The run was paused before its next provider turn could start.",
			execution.RunWaitKindPhaseCheckpoint,
		); err != nil {
			return err
		}
		_, err = starter.advanceIntervention(ctx, run.ID)
		return err
	}
	var session execution.Session
	if len(activeSessions) == 1 {
		session = activeSessions[0]
	} else if len(activeSessions) == 0 {
		if storedFeature.State == feature.StateReadyToMerge {
			return starter.recoverIdleReadyToMergeRun(ctx, run)
		}
		if storedFeature.State == feature.StateImplementing {
			return starter.recoverIdleImplementationRun(ctx, run)
		}
		if storedFeature.State == feature.StateReviewing {
			return starter.recoverIdleReviewingRun(ctx, run)
		}
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
	isLead := session.AgentID == agentID(run.AgentProviders.Lead, worker.RoleLead) && session.Role == worker.RoleLead
	isReviewer := session.AgentID == agentID(run.AgentProviders.Reviewer, worker.RoleReviewer) && session.Role == worker.RoleReviewer
	if session.RunID != run.ID || (!isLead && !isReviewer) {
		return fmt.Errorf("%w: stored real-agent session does not match run", ErrInvalidRunRequest)
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("load lead worker attempt: %w", err)
	}
	isIntervention := unfinished != nil &&
		unfinished.Status == execution.InterventionStatusBeingAnswered
	if isIntervention && (unfinished.SessionID != session.ID ||
		unfinished.AttemptID != checkpoint.AttemptID || unfinished.Target != session.Role) {
		return fmt.Errorf("%w: intervention does not match its active worker attempt", ErrInvalidRunRequest)
	}
	if !isIntervention && storedFeature.State == feature.StateImplementing {
		attemptVersion, _, implementing := workflowAttemptVersionAndTurn(
			session.ID, "implementation", checkpoint.AttemptID,
		)
		if !isLead || !implementing || attemptVersion != run.PlanVersion {
			return fmt.Errorf("%w: implementing feature has an unexpected active attempt", ErrInvalidRunRequest)
		}
	}
	if !isIntervention && storedFeature.State == feature.StateReviewing {
		reviewVersion, _, reviewing := workflowAttemptVersionAndTurn(session.ID, "review", checkpoint.AttemptID)
		correctionVersion, _, correcting := workflowAttemptVersionAndTurn(session.ID, "correction", checkpoint.AttemptID)
		readinessVersion, _, acknowledging := workflowAttemptVersionAndTurn(session.ID, "readiness", checkpoint.AttemptID)
		reviewing = reviewing && reviewVersion == run.PlanVersion
		correcting = correcting && correctionVersion == run.PlanVersion
		acknowledging = acknowledging && readinessVersion == run.PlanVersion
		if (!isReviewer || !reviewing) && (!isLead || (!correcting && !acknowledging)) {
			return fmt.Errorf("%w: reviewing feature has an unexpected active attempt", ErrInvalidRunRequest)
		}
	}
	if !isIntervention && storedFeature.State == feature.StatePlanning {
		attemptVersion, _, planning := planningAttemptVersionAndTurn(session.ID, checkpoint.AttemptID)
		if !planning || attemptVersion != run.PlanVersion {
			return fmt.Errorf("%w: planning attempt does not match the run's current plan version", ErrInvalidRunRequest)
		}
		if attemptVersion > 1 {
			if _, err := starter.executions.GetPlanRevision(ctx, run.ID, attemptVersion); err != nil {
				return fmt.Errorf("load active plan revision: %w", err)
			}
		}
	}
	request := remoteLeadRequest{
		runID:         run.ID,
		agentName:     "lead agent",
		waitingReason: waitingReasonForAttempt(session, checkpoint.AttemptID),
		waitKind:      waitKindForAttempt(session, checkpoint.AttemptID),
		planningStage: planningStageForAttempt(session, checkpoint.AttemptID),
		identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: session.ID, AttemptID: checkpoint.AttemptID,
		}},
	}
	if run.PlanVersion > 1 && isLead && request.planningStage == planningStageLeadProposal {
		request.waitingReason = replanningProposalReason
	}
	if isIntervention {
		request.interventionID = unfinished.ID
		request.agentName = string(unfinished.Target)
		request.waitingReason = interventionAnsweredReason
		request.waitKind = execution.RunWaitKindPaused
		request.request.OutputContract = workerhttp.OutputContractIntervention
	}
	if isLead && !isIntervention {
		if _, implementing := implementationTurnNumber(session.ID, checkpoint.AttemptID); implementing {
			request.request.OutputContract = workerhttp.OutputContractImplementationLead
		}
		if _, correcting := implementationCorrectionTurnNumber(session.ID, checkpoint.AttemptID); correcting {
			request.request.OutputContract = workerhttp.OutputContractImplementationLead
		}
		if _, acknowledging := implementationReadinessTurnNumber(session.ID, checkpoint.AttemptID); acknowledging {
			request.request.OutputContract = workerhttp.OutputContractImplementationReadiness
		}
	}
	if isReviewer && !isIntervention {
		request.agentName = "reviewer"
		if _, reviewing := implementationReviewTurnNumber(session.ID, checkpoint.AttemptID); reviewing {
			request.request.OutputContract = workerhttp.OutputContractImplementationReview
		}
	}
	pending, err := starter.executions.PendingCommandsForSession(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("load pending lead commands: %w", err)
	}
	if len(pending) > 1 {
		return fmt.Errorf("%w: lead session has multiple pending replies", ErrInvalidRunRequest)
	}
	if isIntervention && len(pending) != 0 {
		return fmt.Errorf("%w: intervention session has an unexpected pending command", ErrInvalidRunRequest)
	}
	if isReviewer && len(pending) != 0 {
		return fmt.Errorf("%w: reviewer session has an unexpected pending command", ErrInvalidRunRequest)
	}
	if isLead && len(pending) == 1 {
		implementationTurn, implementing := implementationTurnNumber(session.ID, checkpoint.AttemptID)
		matchesDraftReply := storedFeature.State == feature.StateDraft &&
			replyAttemptID(session.ID, pending[0].ID) == checkpoint.AttemptID
		matchesImplementationContinuation := storedFeature.State == feature.StateImplementing &&
			implementing && implementationTurn > 1
		if pending[0].Type != worker.CommandMessage ||
			(!matchesDraftReply && !matchesImplementationContinuation) {
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

// RecoverBlocker lets a user explicitly retry reconciliation of the exact
// durable checkpoint that produced a blocker. Recover performs only worker
// lookups, event reattachment, and already-admitted result verification; it
// never calls PutAttempt for a replacement provider turn. The per-run claim
// folds concurrent retries onto the same in-flight reconciliation.
func (starter *RemoteLeadStarter) RecoverBlocker(
	ctx context.Context,
	runID string,
	actionID string,
) (execution.Run, bool, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(actionID) == "" {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Status != execution.RunStatusWaitingForUser || run.Paused ||
		run.WaitKind != execution.RunWaitKindBlocker {
		return execution.Run{}, false, ErrRecoveryNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}

	err = starter.Recover(context.WithoutCancel(ctx), run, storedFeature, project.RecoveryPolicyAutomatic)
	if err != nil && !errors.Is(err, ErrRunAlreadyActive) {
		// A failed re-check is itself a safe outcome: retain the existing blocker
		// and its user-facing reason so another explicit request can try again.
		starter.reportError(fmt.Errorf("re-check recovery blocker for run %q: %w", run.ID, err))
		current, getErr := starter.executions.GetRun(ctx, run.ID)
		if getErr != nil {
			return execution.Run{}, false, errors.Join(err, getErr)
		}
		return current, false, nil
	}
	current, err := starter.executions.GetRun(ctx, run.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	return current, true, nil
}

func (starter *RemoteLeadStarter) recoverIdleReadyToMergeRun(
	ctx context.Context,
	run execution.Run,
) error {
	// The lead's verified acknowledgement and feature transition are durable
	// before the run returns to its user gate. Once both exist, recovery must not
	// depend on a worker container that has no more work to perform.
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load approved lead session: %w", err)
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load approved reviewer session: %w", err)
	}
	if lead.Status != execution.SessionStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser {
		return fmt.Errorf("%w: ready-to-merge run has a non-waiting session", ErrInvalidRunRequest)
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return fmt.Errorf("load approved lead checkpoint: %w", err)
	}
	readinessVersion, _, acknowledging := workflowAttemptVersionAndTurn(
		lead.ID, "readiness", checkpoint.AttemptID,
	)
	if !acknowledging || readinessVersion != run.PlanVersion {
		return fmt.Errorf("%w: ready-to-merge run has no lead acknowledgement", ErrInvalidRunRequest)
	}
	recorded, err := starter.implementationReadinessVerificationRecorded(
		ctx, lead.ID, checkpoint.AttemptID,
	)
	if err != nil {
		return err
	}
	if !recorded {
		return fmt.Errorf("%w: ready-to-merge run has no verified lead acknowledgement", ErrInvalidRunRequest)
	}
	if starter.validation != nil {
		storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
		if err != nil {
			return err
		}
		prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
		if err != nil {
			return err
		}
		if _, _, err := starter.validation.EnsureJob(
			ctx, storedFeature.ProjectID, storedFeature.ID, run.ID, prepared.ID, prepared.ApprovedCommitID,
		); err != nil {
			if errors.Is(err, validation.ErrNotFound) {
				return starter.waitRun(ctx, run.ID,
					"Configure this project's isolated validation commands before merge.",
					execution.RunWaitKindMergeGate,
				)
			}
			return fmt.Errorf("recover isolated validation job: %w", err)
		}
		if err := starter.validation.RequirePassed(ctx, run.ID, prepared.ApprovedCommitID); err != nil {
			return starter.waitRun(ctx, run.ID,
				"The exact approved revision is waiting for isolated validation before merge.",
				execution.RunWaitKindMergeGate,
			)
		}
	}
	if run.MergePolicy == project.MergePolicyAutoAfterGates {
		if _, _, err := starter.Merge(ctx, run.ID, run.ID+":automatic-merge"); err != nil {
			starter.recordMergeBlocked(ctx, run.ID, err)
			return starter.waitRun(ctx, run.ID, implementationMergeBlockedReason, execution.RunWaitKindBlocker)
		}
		return nil
	}
	return starter.waitRun(ctx, run.ID, implementationApprovedReason, execution.RunWaitKindMergeGate)
}

// Merge performs the one final coordinator-owned external action. Both user
// approval and automatic policy call this same method, so neither path can
// bypass the exact approved-commit checks in the workspace service.
func (starter *RemoteLeadStarter) Merge(
	ctx context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	starter.mergeMu.Lock()
	defer starter.mergeMu.Unlock()

	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Status.IsTerminal() {
		if run.Status == execution.RunStatusSucceeded {
			return run, false, nil
		}
		return execution.Run{}, false, ErrImplementationNotAllowed
	}
	if run.Paused {
		return execution.Run{}, false, ErrRunControlNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if starter.validation != nil {
		prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
		if err != nil {
			return execution.Run{}, false, err
		}
		if err := starter.validation.RequirePassed(ctx, run.ID, prepared.ApprovedCommitID); err != nil {
			return execution.Run{}, false, err
		}
	}
	mergedWorkspace, mergedNow, err := starter.workspaces.MergeApproved(
		ctx, storedFeature.ProjectID, storedFeature.ID,
	)
	if err != nil {
		return execution.Run{}, false, err
	}
	if storedFeature.State == feature.StateReadyToMerge {
		if _, err := starter.planning.TransitionFeature(
			ctx, storedFeature.ID, feature.StateCompleted,
			workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
			run.ID+":merged",
		); err != nil {
			return execution.Run{}, false, fmt.Errorf("complete merged feature: %w", err)
		}
	}
	leadSessionID := remoteLeadSessionID(run.ID)
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, run.ID+":merge-completed", leadSessionID,
		worker.Event{Type: worker.EventActivity, Text: fmt.Sprintf(
			"Forgejo merged approved commit %s as %s in pull request #%d.",
			mergedWorkspace.ApprovedCommitID, mergedWorkspace.MergeCommitID,
			mergedWorkspace.PullRequestNumber,
		)},
	); err != nil {
		return execution.Run{}, false, fmt.Errorf("record completed merge activity: %w", err)
	}
	completed, err := starter.executions.TransitionRun(
		ctx, run.ID, run.Status, execution.RunStatusSucceeded, implementationMergedReason,
	)
	if errors.Is(err, execution.ErrStateConflict) {
		stored, getErr := starter.executions.GetRun(ctx, run.ID)
		if getErr == nil && stored.Status == execution.RunStatusSucceeded {
			return stored, false, nil
		}
	}
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("complete merged run: %w", err)
	}
	return completed, mergedNow, nil
}

// Pause arms a durable coordinator gate. It does not claim that a provider
// process can be frozen mid-command; an already admitted turn may finish, but
// every following admission observes Run.Paused and stops at the next boundary.
func (starter *RemoteLeadStarter) Pause(
	ctx context.Context,
	runID string,
	actionID string,
) (execution.Run, bool, error) {
	return starter.executions.ApplyRunPause(
		ctx, actionID, runID, execution.RunPauseActionPause,
	)
}

func (starter *RemoteLeadStarter) QueueIntervention(
	ctx context.Context,
	runID string,
	interventionID string,
	target worker.Role,
	message string,
) (execution.Intervention, bool, error) {
	intervention, created, err := starter.executions.QueueIntervention(
		ctx, interventionID, runID, target, message,
	)
	if err != nil {
		return execution.Intervention{}, false, err
	}
	if _, err := starter.advanceIntervention(context.WithoutCancel(ctx), runID); err != nil {
		return intervention, created, err
	}
	current, err := starter.executions.GetLatestIntervention(ctx, runID)
	if err != nil {
		return execution.Intervention{}, false, err
	}
	return current, created, nil
}

// advanceIntervention starts delivery only after the run is durably paused at
// a waiting boundary. Admission changes the selected session and intervention
// together, while deliberately leaving the run paused and waiting.
func (starter *RemoteLeadStarter) advanceIntervention(
	ctx context.Context,
	runID string,
) (bool, error) {
	intervention, err := starter.executions.GetLatestIntervention(ctx, runID)
	if errors.Is(err, execution.ErrNotFound) ||
		(err == nil && intervention.Status == execution.InterventionStatusAnswered) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if intervention.Status != execution.InterventionStatusQueued {
		return false, nil
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	if !run.Paused || run.Status != execution.RunStatusWaitingForUser ||
		run.WaitKind != execution.RunWaitKindPaused {
		return false, nil
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return false, err
	}
	session, err := starter.executions.GetSession(ctx, intervention.SessionID)
	if err != nil {
		return false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		return false, err
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return false, fmt.Errorf("load intervention workspace: %w", err)
	}
	if !prepared.CheckoutReady() {
		return false, fmt.Errorf("%w: intervention workspace is not ready", ErrInvalidRunRequest)
	}
	attemptID := interventionAttemptID(session.ID, intervention.ID)
	request, err := starter.interventionRequest(
		run, storedFeature, session, prepared, intervention, attemptID,
	)
	if err != nil {
		return false, err
	}
	if !starter.claim(run.ID) {
		// The goroutine that currently owns the run will call
		// advanceWaitingRun after it releases the active turn. The durable queue
		// is therefore enough; competing here must not turn a successful user
		// request into a transient HTTP failure.
		return true, nil
	}
	admitted, err := starter.executions.BeginInterventionTurn(
		ctx, intervention.ID, checkpoint, attemptID, interventionAnsweringReason,
	)
	if err != nil {
		starter.release(run.ID)
		return true, err
	}
	if !admitted {
		starter.release(run.ID)
		return true, nil
	}
	go starter.launch(request)
	return true, nil
}

func (starter *RemoteLeadStarter) interventionRequest(
	run execution.Run,
	storedFeature feature.Feature,
	session execution.Session,
	prepared workspace.Workspace,
	intervention execution.Intervention,
	attemptID string,
) (remoteLeadRequest, error) {
	provider := run.AgentProviders.Lead
	if intervention.Target == worker.RoleReviewer {
		provider = run.AgentProviders.Reviewer
	}
	request := remoteLeadRequest{
		runID: run.ID, interventionID: intervention.ID,
		agentName:     string(intervention.Target),
		waitingReason: interventionAnsweredReason,
		waitKind:      execution.RunWaitKindPaused,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{
				SessionID: session.ID, AttemptID: attemptID,
			},
			IdempotencyKey: attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(provider, intervention.Target),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.Role(intervention.Target), WorkspaceID: prepared.ID,
			},
			ProviderSessionID: session.ProviderSessionID,
			Instructions:      interventionInstructions(storedFeature.State, intervention.Message),
			OutputContract:    workerhttp.OutputContractIntervention,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func interventionInstructions(state feature.State, message string) string {
	return fmt.Sprintf(`The user paused the Commitarium workflow during the %s phase and sent this intervention:

%s

Answer the user directly using the restored conversation and current project context. This is an intervention-only turn: do not edit files, run implementation work, commit, push, publish or update a pull request, submit a formal review, or continue the workflow. Return "guidance_applied" when the message can guide later work without changing the accepted goal or agreed plan; return "clarification_required" when you need more information before classifying or applying it; return "replanning_required" when following it would change the accepted goal, scope, or agreed plan.`, state, strings.TrimSpace(message))
}

func (starter *RemoteLeadStarter) Resume(
	ctx context.Context,
	runID string,
	actionID string,
) (execution.Run, bool, error) {
	intervention, err := starter.executions.GetLatestIntervention(ctx, runID)
	if err == nil && intervention.Status != execution.InterventionStatusAnswered {
		return execution.Run{}, false, ErrInterventionPending
	}
	if err == nil && intervention.Status == execution.InterventionStatusAnswered && intervention.ResolvedAt == nil {
		switch intervention.Effect {
		case worker.InterventionEffectGuidanceApplied:
			run, applied, resolveErr := starter.executions.ResolveInterventionGuidance(
				ctx, actionID, runID, intervention.ID,
			)
			if resolveErr != nil {
				return execution.Run{}, false, resolveErr
			}
			if err := starter.advanceWaitingRun(context.WithoutCancel(ctx), run.ID); err != nil {
				return execution.Run{}, false, err
			}
			current, getErr := starter.executions.GetRun(ctx, run.ID)
			return current, applied, getErr
		case worker.InterventionEffectClarificationRequired:
			return execution.Run{}, false, ErrInterventionClarificationRequired
		case worker.InterventionEffectReplanningRequired:
			return starter.resumeIntoReplanning(ctx, runID, actionID, intervention)
		default:
			return execution.Run{}, false, execution.ErrInvalidIntervention
		}
	}
	if err != nil && !errors.Is(err, execution.ErrNotFound) {
		return execution.Run{}, false, err
	}
	run, applied, err := starter.executions.ApplyRunPause(
		ctx, actionID, runID, execution.RunPauseActionResume,
	)
	if err != nil {
		return execution.Run{}, false, err
	}
	if err := starter.advanceWaitingRun(context.WithoutCancel(ctx), run.ID); err != nil {
		return execution.Run{}, false, err
	}
	current, err := starter.executions.GetRun(ctx, run.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	return current, applied, nil
}

func (starter *RemoteLeadStarter) resumeIntoReplanning(
	ctx context.Context,
	runID string,
	actionID string,
	intervention execution.Intervention,
) (execution.Run, bool, error) {
	if starter.planning == nil || starter.workspaces == nil {
		return execution.Run{}, false, ErrInterventionReplanningRequired
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Status != execution.RunStatusWaitingForUser || !run.Paused ||
		run.WaitKind != execution.RunWaitKindPaused {
		return execution.Run{}, false, ErrRunControlNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	switch storedFeature.State {
	case feature.StatePlanning, feature.StateImplementing, feature.StateReviewing, feature.StateReadyToMerge:
	default:
		return execution.Run{}, false, ErrInterventionReplanningRequired
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return execution.Run{}, false, err
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != worker.RoleLead ||
		messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return execution.Run{}, false, ErrInterventionReplanningRequired
	}
	previousPlan := messages[len(messages)-1].Event
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return execution.Run{}, false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if lead.Status != execution.SessionStatusWaitingForUser || lead.ProviderSessionID == "" ||
		lead.Role != worker.RoleLead || lead.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) {
		return execution.Run{}, false, ErrInterventionReplanningRequired
	}
	if err := starter.confirmCompletedTurn(ctx, lead, checkpoint); err != nil {
		return execution.Run{}, false, ErrInterventionReplanningRequired
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, ErrRunControlNotAllowed
	}
	prepared, baselineCommitID, err := starter.workspaces.PrepareReplanningBaseline(
		ctx, storedFeature.ProjectID, storedFeature.ID, previousPlan.ID, previousPlan.Text,
	)
	if err != nil {
		starter.release(run.ID)
		return execution.Run{}, false, fmt.Errorf("prepare replanning baseline: %w", err)
	}
	effectiveGoal, err := starter.replanningEffectiveGoal(ctx, run, storedFeature, intervention.Message)
	if err != nil {
		starter.release(run.ID)
		return execution.Run{}, false, err
	}
	nextVersion := run.PlanVersion + 1
	attemptID := replanningAttemptID(lead.ID, nextVersion, 1)
	request, err := starter.replanningRequest(
		run, storedFeature, lead, prepared, nextVersion, baselineCommitID,
		effectiveGoal, previousPlan.Text, intervention.Message, attemptID,
	)
	if err != nil {
		starter.release(run.ID)
		return execution.Run{}, false, err
	}
	if storedFeature.State != feature.StatePlanning {
		if _, err := starter.planning.TransitionFeature(
			ctx, storedFeature.ID, feature.StatePlanning,
			workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
			actionID+":replanning",
		); err != nil {
			starter.release(run.ID)
			return execution.Run{}, false, err
		}
	}
	started, _, admitted, err := starter.executions.BeginReplanningTurn(
		ctx, actionID, run.ID, intervention.ID, nextVersion, previousPlan.ID,
		effectiveGoal, baselineCommitID, checkpoint, attemptID, replanningRunningReason,
	)
	if err != nil {
		starter.release(run.ID)
		if errors.Is(err, execution.ErrStateConflict) ||
			errors.Is(err, execution.ErrWorkerAttemptConflict) {
			return execution.Run{}, false, ErrInterventionReplanningRequired
		}
		return execution.Run{}, false, err
	}
	if !admitted {
		starter.release(run.ID)
		return started, false, nil
	}
	go starter.launch(request)
	return started, true, nil
}

func (starter *RemoteLeadStarter) replanningEffectiveGoal(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	amendment string,
) (string, error) {
	goal := strings.TrimSpace(storedFeature.AcceptedGoal)
	if run.PlanVersion > 1 {
		revision, err := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
		if err != nil {
			return "", err
		}
		goal = revision.EffectiveGoal
	}
	return goal + "\n\nUser-approved scope amendment for plan version " +
		strconv.Itoa(run.PlanVersion+1) + ":\n" + strings.TrimSpace(amendment), nil
}

// featureForCurrentPlan keeps the durable feature's original accepted goal
// intact while presenting the accumulated, user-approved scope to agents
// working on later plan versions.
func (starter *RemoteLeadStarter) featureForCurrentPlan(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
) (feature.Feature, error) {
	if run.PlanVersion == 1 {
		return storedFeature, nil
	}
	revision, err := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
	if err != nil {
		return feature.Feature{}, err
	}
	storedFeature.AcceptedGoal = revision.EffectiveGoal
	return storedFeature, nil
}

// workspaceForCurrentPlan returns a prompt-only copy whose planning baseline
// is the commit recorded for the current plan version. The persisted workspace
// keeps its original feature-branch base for ancestry and merge checks.
func (starter *RemoteLeadStarter) workspaceForCurrentPlan(
	ctx context.Context,
	run execution.Run,
	projectID string,
	featureID string,
) (workspace.Workspace, error) {
	prepared, err := starter.workspaces.Get(ctx, projectID, featureID)
	if err != nil || run.PlanVersion == 1 {
		return prepared, err
	}
	revision, err := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
	if err != nil {
		return workspace.Workspace{}, err
	}
	prepared.BaseCommitID = revision.BaselineCommitID
	return prepared, nil
}

func (starter *RemoteLeadStarter) currentPlanningMessages(
	ctx context.Context,
	run execution.Run,
) ([]execution.PlanningMessage, error) {
	messages, err := starter.executions.PlanningMessagesForRun(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	current := make([]execution.PlanningMessage, 0, len(messages))
	for _, message := range messages {
		if message.PlanVersion == run.PlanVersion {
			current = append(current, message)
		}
	}
	return current, nil
}

func (starter *RemoteLeadStarter) replanningRequest(
	run execution.Run,
	storedFeature feature.Feature,
	lead execution.Session,
	prepared workspace.Workspace,
	planVersion int,
	baselineCommitID string,
	effectiveGoal string,
	previousPlan string,
	userAmendment string,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent",
		waitingReason: replanningProposalReason,
		waitKind:      execution.RunWaitKindPhaseCheckpoint,
		planningStage: planningStageLeadProposal,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Lead, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: replanningInstructions(
				planVersion, effectiveGoal, previousPlan, userAmendment,
				prepared, baselineCommitID,
			),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func replanningInstructions(
	planVersion int,
	effectiveGoal string,
	previousPlan string,
	userAmendment string,
	prepared workspace.Workspace,
	baselineCommitID string,
) string {
	return "Continue as the same lead agent and begin plan version " + strconv.Itoa(planVersion) +
		" after the user's scope-changing intervention. Inspect the managed repository, current Git " +
		"HEAD, branch, status, and diff before proposing anything. Existing branch commits and " +
		"uncommitted user or agent edits are intentional external state: preserve them, do not reset, " +
		"clean, overwrite, commit, push, or modify the pull request during this planning turn. Compare " +
		"the prior plan and work already present against the amended goal, identify what remains useful, " +
		"and propose a complete revised implementation plan for the same reviewer to challenge. Durable " +
		"Git, Forgejo, and coordinator state are authoritative over conversational memory.\n\n" +
		"Effective amended goal:\n" + effectiveGoal +
		"\n\nUser's exact scope amendment:\n" + strings.TrimSpace(userAmendment) +
		"\n\nPrevious agreed plan:\n" + previousPlan +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch +
		"\nExisting feature branch: " + prepared.Branch +
		"\nReplanning baseline commit: " + baselineCommitID +
		fmt.Sprintf("\nExisting draft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL)
}

func (starter *RemoteLeadStarter) advanceWaitingRun(ctx context.Context, runID string) error {
	if handled, err := starter.advanceIntervention(ctx, runID); err != nil || handled {
		return err
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Paused || run.Status != execution.RunStatusWaitingForUser ||
		run.WaitKind != execution.RunWaitKindPhaseCheckpoint ||
		run.AutonomyPolicy != project.AutonomyPolicyRunToCompletion {
		return nil
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	switch storedFeature.State {
	case feature.StateDraft:
		if strings.TrimSpace(storedFeature.AcceptedGoal) == "" ||
			storedFeature.GoalAcceptedAt == nil {
			return fmt.Errorf("%w: draft checkpoint has no accepted goal", ErrPlanningNotAllowed)
		}
		_, _, err = starter.StartPlanning(ctx, run.ID, run.ID+":autonomy:planning")
		return err
	case feature.StatePlanning:
		messages, err := starter.currentPlanningMessages(ctx, run)
		if err != nil {
			return err
		}
		if len(messages) == 0 {
			return fmt.Errorf("%w: planning checkpoint has no durable message", ErrPlanningNotAllowed)
		}
		if messages[len(messages)-1].Event.Type == worker.EventPlanSubmitted {
			published, err := starter.planPublicationRecorded(ctx, messages[len(messages)-1].Event)
			if err != nil {
				return err
			}
			if published {
				_, _, err = starter.StartImplementation(ctx, run.ID, run.ID+":autonomy:implementation")
				return err
			}
		}
		if _, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID)); errors.Is(err, execution.ErrNotFound) {
			_, _, err = starter.StartPlanningReview(ctx, run.ID, run.ID+":autonomy:first-review")
			return err
		} else if err != nil {
			return err
		}
		if len(messages) == 2 && messages[len(messages)-1].Role == worker.RoleReviewer {
			_, _, err = starter.StartPlanningRound(ctx, run.ID, run.ID+":autonomy:planning-loop")
			return err
		}
		_, err = starter.recoverIdlePlanningRun(ctx, run, storedFeature)
		return err
	case feature.StateImplementing:
		return starter.recoverIdleImplementationRun(ctx, run)
	case feature.StateReviewing:
		return starter.recoverIdleReviewingRun(ctx, run)
	case feature.StateReadyToMerge:
		return starter.recoverIdleReadyToMergeRun(ctx, run)
	default:
		return nil
	}
}

// stopAtPauseBoundary turns an armed pause into a durable user-visible wait
// before the coordinator admits the next provider turn or performs an
// automatic merge. The intended next step is retained privately so Resume can
// dispatch exactly that checkpoint without inferring it from prose.
func (starter *RemoteLeadStarter) stopAtPauseBoundary(
	ctx context.Context,
	runID string,
	reason string,
) (bool, error) {
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	if !run.Paused {
		return false, nil
	}
	return true, starter.waitRun(
		ctx, runID, reason, execution.RunWaitKindPhaseCheckpoint,
	)
}

func (starter *RemoteLeadStarter) recordMergeBlocked(ctx context.Context, runID string, cause error) {
	digest := sha256.Sum256([]byte(cause.Error()))
	_, _ = starter.executions.RecordSessionEventWithID(
		ctx, runID+":merge-blocked:"+hex.EncodeToString(digest[:8]), remoteLeadSessionID(runID),
		worker.Event{Type: worker.EventActivity, Text: "Merge blocked: " + cause.Error()},
	)
}

func (starter *RemoteLeadStarter) recoverIdleImplementationRun(
	ctx context.Context,
	run execution.Run,
) error {
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load implementing lead session: %w", err)
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load implementing reviewer session: %w", err)
	}
	if lead.Status != execution.SessionStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser {
		return fmt.Errorf("%w: idle implementation has a non-waiting session", ErrInvalidRunRequest)
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return fmt.Errorf("load implementation attempt: %w", err)
	}
	attemptVersion, _, ok := workflowAttemptVersionAndTurn(
		lead.ID, "implementation", checkpoint.AttemptID,
	)
	if !ok || attemptVersion != run.PlanVersion {
		return fmt.Errorf("%w: idle implementation attempt is incomplete", ErrInvalidRunRequest)
	}
	if err := starter.confirmCompletedTurn(ctx, lead, checkpoint); err != nil {
		return fmt.Errorf("confirm completed implementation attempt: %w", err)
	}
	attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
	})
	if err != nil {
		return fmt.Errorf("load completed implementation result: %w", err)
	}
	if attempt.Result == nil {
		return errors.New("completed implementation attempt has no result")
	}
	if attempt.Result.Disposition == workerhttp.DispositionInputRequired {
		return starter.handleInputRequired(ctx, remoteLeadRequest{runID: run.ID, identity: workerhttp.MutationIdentity{AttemptReference: attempt.AttemptReference}}, *attempt.Result)
	}
	request := remoteLeadRequest{
		runID: run.ID,
		identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
		}},
	}
	if !starter.claim(run.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	go starter.launchImplementationPublicationVerification(request, *attempt.Result)
	return nil
}

func (starter *RemoteLeadStarter) recoverIdleReviewingRun(
	ctx context.Context,
	run execution.Run,
) error {
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load reviewing session: %w", err)
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load reviewed lead session: %w", err)
	}
	if reviewer.Status != execution.SessionStatusWaitingForUser ||
		lead.Status != execution.SessionStatusWaitingForUser {
		return fmt.Errorf("%w: idle review has a non-waiting session", ErrInvalidRunRequest)
	}
	reviewerCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return fmt.Errorf("load reviewer checkpoint: %w", err)
	}
	leadCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return fmt.Errorf("load reviewed implementation checkpoint: %w", err)
	}
	reviewVersion, reviewRound, reviewing := workflowAttemptVersionAndTurn(reviewer.ID, "review", reviewerCheckpoint.AttemptID)
	correctionVersion, correctionRound, correcting := workflowAttemptVersionAndTurn(lead.ID, "correction", leadCheckpoint.AttemptID)
	readinessVersion, readinessRound, acknowledging := workflowAttemptVersionAndTurn(lead.ID, "readiness", leadCheckpoint.AttemptID)
	reviewing = reviewing && reviewVersion == run.PlanVersion
	correcting = correcting && correctionVersion == run.PlanVersion
	acknowledging = acknowledging && readinessVersion == run.PlanVersion
	leadResponseRound := correctionRound
	if acknowledging {
		leadResponseRound = readinessRound
	}
	leadResponding := correcting || acknowledging

	// The later durable checkpoint wins. Review N follows lead response N-1,
	// while either correction N or readiness acknowledgement N follows review N.
	// Equal numbers therefore mean the lead response must finish first.
	if reviewing && (!leadResponding || reviewRound > leadResponseRound) {
		if err := starter.confirmCompletedTurn(ctx, reviewer, reviewerCheckpoint); err != nil {
			return fmt.Errorf("confirm completed review: %w", err)
		}
		attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
			SessionID: reviewer.ID, AttemptID: reviewerCheckpoint.AttemptID,
		})
		if err != nil {
			return fmt.Errorf("load completed review result: %w", err)
		}
		if attempt.Result == nil {
			return errors.New("completed review attempt has no result")
		}
		if attempt.Result.Disposition == workerhttp.DispositionInputRequired {
			return starter.handleInputRequired(ctx, remoteLeadRequest{runID: run.ID, identity: workerhttp.MutationIdentity{AttemptReference: attempt.AttemptReference}}, *attempt.Result)
		}
		request := remoteLeadRequest{runID: run.ID, agentName: "reviewer", identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: reviewerCheckpoint.AttemptID},
		}}
		if !starter.claim(run.ID) {
			return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
		}
		go starter.launchImplementationReviewVerification(request, *attempt.Result)
		return nil
	}
	if leadResponding {
		if err := starter.confirmCompletedTurn(ctx, lead, leadCheckpoint); err != nil {
			return fmt.Errorf("confirm completed lead review response: %w", err)
		}
		attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
			SessionID: lead.ID, AttemptID: leadCheckpoint.AttemptID,
		})
		if err != nil {
			return fmt.Errorf("load completed lead review response: %w", err)
		}
		if attempt.Result == nil {
			return errors.New("completed lead review response has no result")
		}
		if attempt.Result.Disposition == workerhttp.DispositionInputRequired {
			return starter.handleInputRequired(ctx, remoteLeadRequest{runID: run.ID, identity: workerhttp.MutationIdentity{AttemptReference: attempt.AttemptReference}}, *attempt.Result)
		}
		request := remoteLeadRequest{runID: run.ID, agentName: "lead agent", identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: leadCheckpoint.AttemptID},
		}}
		if !starter.claim(run.ID) {
			return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
		}
		if correcting {
			go starter.launchImplementationCorrectionVerification(request, *attempt.Result)
		} else {
			go starter.launchImplementationReadinessVerification(request, *attempt.Result)
		}
		return nil
	}
	implementationVersion, _, implementing := workflowAttemptVersionAndTurn(
		lead.ID, "implementation", leadCheckpoint.AttemptID,
	)
	if !implementing || implementationVersion != run.PlanVersion {
		return fmt.Errorf("%w: idle review has no current-plan implementation", ErrInvalidRunRequest)
	}
	if err := starter.confirmCompletedTurn(ctx, lead, leadCheckpoint); err != nil {
		return fmt.Errorf("confirm reviewed implementation: %w", err)
	}
	attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: lead.ID, AttemptID: leadCheckpoint.AttemptID,
	})
	if err != nil {
		return fmt.Errorf("load reviewed implementation result: %w", err)
	}
	if attempt.Result == nil || attempt.Result.Publication == nil {
		return errors.New("reviewed implementation attempt has no publication result")
	}
	request := remoteLeadRequest{runID: run.ID, identity: workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: leadCheckpoint.AttemptID},
	}}
	if !starter.claim(run.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	go starter.launchImplementationPublicationVerification(request, *attempt.Result)
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
	if lead.Status != execution.SessionStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser {
		return true, fmt.Errorf("%w: idle planning run has a non-waiting session", ErrInvalidRunRequest)
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return true, err
	}
	if len(messages) < 2 {
		if run.PlanVersion > 1 && len(messages) == 1 {
			if err := starter.waitRun(
				ctx, run.ID, replanningProposalReason, execution.RunWaitKindPhaseCheckpoint,
			); err != nil {
				return true, err
			}
			if run.AutonomyPolicy == project.AutonomyPolicyRunToCompletion && !run.Paused {
				_, _, err := starter.StartPlanningReview(
					ctx, run.ID, run.ID+":recovery:replanning-review",
				)
				return true, err
			}
			return true, nil
		}
		return true, fmt.Errorf("%w: idle planning run has incomplete shared history", ErrInvalidRunRequest)
	}
	last := messages[len(messages)-1]
	if last.Event.Type == worker.EventPlanSubmitted {
		if err := starter.ensureSubmittedPlanArtifact(ctx, run, last.Event); err != nil {
			return true, err
		}
		if !starter.claim(run.ID) {
			return true, fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
		}
		defer starter.release(run.ID)
		if err := starter.publishSubmittedPlan(ctx, run.ID, last.Event); err != nil {
			starter.requirePlanPublicationReview(ctx, run.ID, last.Event, err)
		}
		return true, nil
	}
	if planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) {
		return true, starter.waitRun(ctx, run.ID, planningLimitReason(run.PlanningRoundLimit), execution.RunWaitKindRoundCap)
	}
	if len(messages) == 2 {
		if err := starter.waitRun(
			ctx, run.ID, "The reviewer's first planning response is ready.",
			execution.RunWaitKindPhaseCheckpoint,
		); err != nil {
			return true, err
		}
		if run.AutonomyPolicy == project.AutonomyPolicyRunToCompletion && !run.Paused {
			_, _, err := starter.StartPlanningRound(
				ctx, run.ID, run.ID+":recovery:planning-loop",
			)
			return true, err
		}
		return true, nil
	}
	if !starter.claim(run.ID) {
		return true, fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	var request remoteLeadRequest
	var admitted bool
	if last.Role == worker.RoleLead {
		request, admitted, err = starter.startReviewerResponse(ctx, run, storedFeature, messages)
	} else if last.Role == worker.RoleReviewer {
		request, admitted, err = starter.startLeadResponse(ctx, run, storedFeature, messages, true)
	} else {
		err = ErrPlanningNotAllowed
	}
	if err != nil || !admitted {
		starter.release(run.ID)
		return true, err
	}
	go starter.launch(request)
	return true, nil
}

func (starter *RemoteLeadStarter) ensureSubmittedPlanArtifact(
	ctx context.Context,
	run execution.Run,
	planEvent execution.Event,
) error {
	if starter.artifacts == nil {
		return nil
	}
	current, err := starter.artifacts.GetFeatureArtifact(
		ctx, run.FeatureID, featureartifact.KindImplementationPlan,
	)
	if err == nil {
		plan := featureartifact.ImplementationPlan{}
		if decodeErr := json.Unmarshal([]byte(current.Document), &plan); decodeErr == nil &&
			plan.PlanVersion == run.PlanVersion {
			return nil
		}
	} else if !errors.Is(err, workflow.ErrArtifactNotFound) {
		return err
	}
	if strings.TrimSpace(planEvent.WorkerAttemptID) == "" || strings.TrimSpace(planEvent.SessionID) == "" {
		return errors.New("submitted plan has no durable worker attempt identity")
	}
	attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: planEvent.SessionID, AttemptID: planEvent.WorkerAttemptID,
	})
	if err != nil || attempt.Result == nil || attempt.Result.ImplementationPlan == nil {
		return errors.New("submitted plan has no recoverable structured implementation checklist")
	}
	normalized, err := attempt.Result.ImplementationPlan.NormalizeInitial(run.PlanVersion)
	if err != nil {
		return err
	}
	_, err = starter.artifacts.UpsertImplementationPlan(
		ctx, run.FeatureID, normalized,
		workflow.Actor{Kind: workflow.ActorKindAgent, ID: planEvent.SessionID},
		planEvent.WorkerAttemptID+":implementation-plan",
	)
	return err
}

type remoteLeadRequest struct {
	runID          string
	commandID      string
	interventionID string
	agentName      string
	waitingReason  string
	waitKind       execution.RunWaitKind
	planningStage  planningStage
	identity       workerhttp.MutationIdentity
	request        workerhttp.PutAttemptRequest
}

func interventionAttemptID(sessionID, interventionID string) string {
	digest := sha256.Sum256([]byte(interventionID))
	return sessionID + ":intervention:" + hex.EncodeToString(digest[:16])
}

func (starter *RemoteLeadStarter) startRequest(
	runID string,
	projectID string,
	featureID string,
	workspaceID string,
	goal string,
	agentProviders project.AgentProviders,
) (remoteLeadRequest, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(projectID) == "" ||
		strings.TrimSpace(featureID) == "" || strings.TrimSpace(workspaceID) == "" ||
		strings.TrimSpace(goal) == "" {
		return remoteLeadRequest{}, ErrInvalidRunRequest
	}
	sessionID := remoteLeadSessionID(runID)
	attemptID := sessionID + ":turn:1"
	request := remoteLeadRequest{
		runID:         runID,
		agentName:     "lead agent",
		waitingReason: "The lead agent is waiting for the user's response.",
		waitKind:      execution.RunWaitKindClarification,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":start",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeStart,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(agentProviders.Lead, worker.RoleLead),
				ProjectID:      projectID, FeatureID: featureID,
				Role: workerhttp.RoleLead, WorkspaceID: workspaceID,
			},
			Instructions:   remoteLeadInstructions(goal),
			OutputContract: workerhttp.OutputContractGoalClarification,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func (starter *RemoteLeadStarter) replyRequest(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	session execution.Session,
	command worker.Command,
) (remoteLeadRequest, error) {
	if starter.workspaces == nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: workspace service is required", ErrInvalidRunRequest)
	}
	prepared, _, err := starter.workspaces.PrepareForClarification(
		ctx, storedFeature.ProjectID, storedFeature.ID,
	)
	if err != nil {
		return remoteLeadRequest{}, fmt.Errorf("prepare goal-clarification reply workspace: %w", err)
	}
	if !prepared.CheckoutReady() {
		return remoteLeadRequest{}, fmt.Errorf("%w: goal-clarification checkout is not ready", ErrInvalidRunRequest)
	}
	attemptID := replyAttemptID(session.ID, command.ID)
	request := remoteLeadRequest{
		runID: run.ID, commandID: command.ID,
		agentName:     "lead agent",
		waitingReason: "The lead agent is waiting for the user's response.",
		waitKind:      execution.RunWaitKindClarification,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{
				SessionID: session.ID, AttemptID: attemptID,
			},
			IdempotencyKey: attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Lead, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			Instructions:      remoteLeadReplyInstructions(command.Message),
			ProviderSessionID: session.ProviderSessionID,
			OutputContract:    workerhttp.OutputContractGoalClarification,
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

func planningTurnAttemptID(sessionID string, turn int) string {
	return sessionID + ":planning:" + strconv.Itoa(turn)
}

func replanningAttemptID(sessionID string, version int, turn int) string {
	return sessionID + ":planning:v" + strconv.Itoa(version) + ":" + strconv.Itoa(turn)
}

func planningAttemptForVersion(sessionID string, version int, turn int) string {
	if version == 1 {
		return planningTurnAttemptID(sessionID, turn)
	}
	return replanningAttemptID(sessionID, version, turn)
}

func implementationAttemptID(sessionID string, turn int) string {
	return sessionID + ":implementation:" + strconv.Itoa(turn)
}

func implementationReviewAttemptID(sessionID string, turn int) string {
	return sessionID + ":review:" + strconv.Itoa(turn)
}

func implementationCorrectionAttemptID(sessionID string, turn int) string {
	return sessionID + ":correction:" + strconv.Itoa(turn)
}

func implementationReadinessAttemptID(sessionID string, turn int) string {
	return sessionID + ":readiness:" + strconv.Itoa(turn)
}

func workflowAttemptForVersion(sessionID string, kind string, version int, turn int) string {
	if version == 1 {
		return sessionID + ":" + kind + ":" + strconv.Itoa(turn)
	}
	return sessionID + ":" + kind + ":v" + strconv.Itoa(version) + ":" + strconv.Itoa(turn)
}

func implementationAttemptForVersion(sessionID string, version int, turn int) string {
	return workflowAttemptForVersion(sessionID, "implementation", version, turn)
}

func implementationReviewAttemptForVersion(sessionID string, version int, turn int) string {
	return workflowAttemptForVersion(sessionID, "review", version, turn)
}

func implementationCorrectionAttemptForVersion(sessionID string, version int, turn int) string {
	return workflowAttemptForVersion(sessionID, "correction", version, turn)
}

func implementationReadinessAttemptForVersion(sessionID string, version int, turn int) string {
	return workflowAttemptForVersion(sessionID, "readiness", version, turn)
}

func implementationCorrectionTurnNumber(sessionID, attemptID string) (int, bool) {
	_, turn, found := workflowAttemptVersionAndTurn(sessionID, "correction", attemptID)
	return turn, found
}

func implementationReviewTurnNumber(sessionID, attemptID string) (int, bool) {
	_, turn, found := workflowAttemptVersionAndTurn(sessionID, "review", attemptID)
	return turn, found
}

func implementationReadinessTurnNumber(sessionID, attemptID string) (int, bool) {
	_, turn, found := workflowAttemptVersionAndTurn(sessionID, "readiness", attemptID)
	return turn, found
}

func implementationTurnNumber(sessionID, attemptID string) (int, bool) {
	_, turn, found := workflowAttemptVersionAndTurn(sessionID, "implementation", attemptID)
	return turn, found
}

func workflowAttemptVersionAndTurn(sessionID string, kind string, attemptID string) (int, int, bool) {
	value, found := strings.CutPrefix(attemptID, sessionID+":"+kind+":")
	if !found {
		return 0, 0, false
	}
	if strings.HasPrefix(value, "v") {
		parts := strings.Split(value, ":")
		if len(parts) != 2 {
			return 0, 0, false
		}
		version, versionErr := strconv.Atoi(strings.TrimPrefix(parts[0], "v"))
		turn, turnErr := strconv.Atoi(parts[1])
		return version, turn, versionErr == nil && version > 1 && turnErr == nil && turn > 0
	}
	turn, err := strconv.Atoi(value)
	return 1, turn, err == nil && turn > 0
}

func planningTurnNumber(sessionID, attemptID string) (int, bool) {
	_, turn, found := planningAttemptVersionAndTurn(sessionID, attemptID)
	return turn, found
}

// planningAttemptVersionAndTurn understands both the original unversioned
// attempt IDs (which belong to plan version 1) and the explicit IDs used by
// replanning. Recovery uses both coordinates so an old attempt cannot be
// mistaken for work on the run's current plan.
func planningAttemptVersionAndTurn(sessionID, attemptID string) (int, int, bool) {
	value, found := strings.CutPrefix(attemptID, sessionID+":planning:")
	if !found {
		return 0, 0, false
	}
	if strings.HasPrefix(value, "v") {
		parts := strings.Split(value, ":")
		if len(parts) != 2 {
			return 0, 0, false
		}
		version, versionErr := strconv.Atoi(strings.TrimPrefix(parts[0], "v"))
		turn, turnErr := strconv.Atoi(parts[1])
		return version, turn, versionErr == nil && version > 1 && turnErr == nil && turn > 0
	}
	turn, err := strconv.Atoi(value)
	return 1, turn, err == nil && turn > 0
}

func planningStageForAttempt(session execution.Session, attemptID string) planningStage {
	turn, planned := planningTurnNumber(session.ID, attemptID)
	switch {
	case !planned:
		return planningStageNone
	case session.Role == worker.RoleLead && turn == 1:
		return planningStageLeadProposal
	case session.Role == worker.RoleReviewer && turn == 1:
		return planningStageFirstReview
	case session.Role == worker.RoleLead && turn >= 2:
		return planningStageLeadResponse
	case session.Role == worker.RoleReviewer && turn >= 2:
		return planningStageReviewerResponse
	default:
		return planningStageNone
	}
}

func waitKindForAttempt(session execution.Session, attemptID string) execution.RunWaitKind {
	if _, planned := planningTurnNumber(session.ID, attemptID); planned {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, implementing := implementationTurnNumber(session.ID, attemptID); implementing {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, correcting := implementationCorrectionTurnNumber(session.ID, attemptID); correcting {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, acknowledging := implementationReadinessTurnNumber(session.ID, attemptID); acknowledging {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, reviewing := implementationReviewTurnNumber(session.ID, attemptID); reviewing {
		return execution.RunWaitKindPhaseCheckpoint
	}
	return execution.RunWaitKindClarification
}

func waitingReasonForAttempt(session execution.Session, attemptID string) string {
	if _, implementing := implementationTurnNumber(session.ID, attemptID); session.Role == worker.RoleLead && implementing {
		return implementationReadyReason
	}
	if _, correcting := implementationCorrectionTurnNumber(session.ID, attemptID); session.Role == worker.RoleLead && correcting {
		return implementationCorrectionRunningReason
	}
	if _, acknowledging := implementationReadinessTurnNumber(session.ID, attemptID); session.Role == worker.RoleLead && acknowledging {
		return implementationReadinessRunningReason
	}
	if _, reviewing := implementationReviewTurnNumber(session.ID, attemptID); session.Role == worker.RoleReviewer && reviewing {
		return implementationReviewRunningReason
	}
	turn, planned := planningTurnNumber(session.ID, attemptID)
	if session.Role == worker.RoleReviewer && planned && turn == 1 {
		return "The reviewer's first planning response is ready."
	}
	if session.Role == worker.RoleLead && planned && turn == 1 {
		return "The lead planning proposal is ready for reviewer consultation."
	}
	if session.Role == worker.RoleReviewer && planned {
		return "The reviewer has responded to the lead's plan."
	}
	if session.Role == worker.RoleLead && planned {
		return "The lead has responded in the planning discussion."
	}
	return "The lead agent is waiting for the user's response."
}

func remoteLeadInstructions(goal string) string {
	return "You are the lead agent helping the user define a software-development goal. " +
		"This is goal clarification only: do not modify files, run destructive commands, " +
		"create commits, or begin implementation. Inspect the available project read-only " +
		"when useful. Restate your understanding, identify important ambiguity or risk, and " +
		"ask the user the smallest useful set of questions needed before planning. Return action 'ask' " +
		"while information is missing, put the user-facing question in message, put your current best " +
		"complete goal draft in goal when one is useful (or an empty string when it would be misleading), " +
		"and list the unresolved questions in open_questions. Once the goal is ready to accept, return " +
		"action 'propose', explain that in message, put the complete proposed goal in goal, and return an " +
		"empty open_questions array. The conversational message is not the proposed goal. " +
		"The user's current goal is:\n\n" + goal
}

func remoteLeadReplyInstructions(message string) string {
	return "Continue the same goal-clarification conversation. This is still clarification only: " +
		"do not modify files, run destructive commands, create commits, or begin implementation. " +
		"Use the existing conversation context, incorporate the user's reply, and ask only the " +
		"next questions genuinely needed before planning. Return action 'ask' with a user-facing message, " +
		"the current best complete goal draft in goal when useful (otherwise an empty string), and unresolved " +
		"questions in open_questions. When ready, return action 'propose' with the complete goal, an empty " +
		"open_questions array, and a separate user-facing message. The conversational message is never the " +
		"proposed goal. The user replied:\n\n" + message
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
	if run.Paused {
		return execution.Run{}, false, ErrRunControlNotAllowed
	}
	session, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return execution.Run{}, false, err
	}
	if session.RunID != run.ID || session.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) ||
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
		prepared, _, err = starter.workspaces.PrepareForClarification(
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
	if !prepared.CheckoutReady() {
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
		feature.StatePlanning,
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
		waitKind:      execution.RunWaitKindPhaseCheckpoint,
		planningStage: planningStageLeadProposal,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{
				SessionID: session.ID, AttemptID: attemptID,
			},
			IdempotencyKey: attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Lead, worker.RoleLead),
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
	if run.Paused {
		return execution.Run{}, false, ErrRunControlNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if storedFeature.State != feature.StatePlanning ||
		strings.TrimSpace(storedFeature.AcceptedGoal) == "" || storedFeature.GoalAcceptedAt == nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if run.PlanVersion > 1 {
		return starter.startReplanningReview(ctx, run, storedFeature)
	}

	reviewerID := remoteReviewerSessionID(run.ID)
	if existing, getErr := starter.executions.GetSession(ctx, reviewerID); getErr == nil {
		if existing.RunID != run.ID || existing.AgentID != agentID(run.AgentProviders.Reviewer, worker.RoleReviewer) ||
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
	if lead.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) || lead.Role != worker.RoleLead ||
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
	if err != nil || !prepared.CheckoutReady() {
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
		ctx, reviewerID, run.ID, agentID(run.AgentProviders.Reviewer, worker.RoleReviewer), worker.RoleReviewer,
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

func (starter *RemoteLeadStarter) startReplanningReview(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
) (execution.Run, bool, error) {
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return execution.Run{}, false, err
	}
	if len(messages) < 1 || messages[0].Role != worker.RoleLead ||
		messages[0].Event.Type != worker.EventMessage {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	revision, err := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
	if err != nil {
		return execution.Run{}, false, err
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
	wantLeadAttempt := planningAttemptForVersion(lead.ID, run.PlanVersion, 1)
	if lead.Status != execution.SessionStatusWaitingForUser ||
		leadCheckpoint.AttemptID != wantLeadAttempt ||
		lead.ProviderSessionID == "" ||
		lead.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) ||
		lead.Role != worker.RoleLead {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if err := starter.confirmCompletedTurn(ctx, lead, leadCheckpoint); err != nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	reviewerCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	nextAttemptID := planningAttemptForVersion(reviewer.ID, run.PlanVersion, 1)
	if reviewerCheckpoint.AttemptID == nextAttemptID {
		return run, false, nil
	}
	if len(messages) != 1 {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if run.Status != execution.RunStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser ||
		reviewer.ProviderSessionID == "" ||
		reviewer.AgentID != agentID(run.AgentProviders.Reviewer, worker.RoleReviewer) ||
		reviewer.Role != worker.RoleReviewer {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if err := starter.confirmCompletedTurn(ctx, reviewer, reviewerCheckpoint); err != nil {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	prepared, err := starter.workspaceForCurrentPlan(
		ctx, run, storedFeature.ProjectID, storedFeature.ID,
	)
	if err != nil || !prepared.CheckoutReady() || !prepared.PullRequestReady() {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	currentFeature, err := starter.featureForCurrentPlan(ctx, run, storedFeature)
	if err != nil {
		return execution.Run{}, false, err
	}
	request, err := starter.replanningReviewerRequest(
		run, currentFeature, reviewer, prepared, revision,
		messages[0].Event.Text, nextAttemptID,
	)
	if err != nil {
		return execution.Run{}, false, err
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	admitted, err := starter.executions.BeginAutonomousTurn(
		ctx, reviewer.ID, reviewerCheckpoint, nextAttemptID,
		feature.StatePlanning, "The reviewer is inspecting the revised planning proposal.",
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

func (starter *RemoteLeadStarter) replanningReviewerRequest(
	run execution.Run,
	storedFeature feature.Feature,
	reviewer execution.Session,
	prepared workspace.Workspace,
	revision execution.PlanRevision,
	proposal string,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID: run.ID, agentName: "reviewer",
		waitingReason: "The reviewer's first response to the revised plan is ready.",
		waitKind:      execution.RunWaitKindPhaseCheckpoint,
		planningStage: planningStageFirstReview,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Reviewer, worker.RoleReviewer),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: reviewer.ProviderSessionID,
			Instructions: "Continue the same reviewer conversation for plan version " +
				strconv.Itoa(run.PlanVersion) + ". The user's approved intervention changed the scope, " +
				"so the earlier agreed plan is historical and must not be treated as current. Inspect the " +
				"existing feature branch, Git HEAD, status, and diff before responding. Preserve all work; " +
				"do not modify files, commit, push, or update the pull request. Challenge the revised proposal " +
				"against the complete effective goal and the work already present. Clearly explain remaining " +
				"concerns or why you are satisfied. Durable Git, Forgejo, and coordinator state are authoritative.\n\n" +
				"Effective goal:\n" + storedFeature.AcceptedGoal +
				"\n\nReplanning baseline commit: " + revision.BaselineCommitID +
				"\n\nLead's exact revised proposal:\n" + proposal,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
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
		waitKind:      execution.RunWaitKindPhaseCheckpoint,
		planningStage: planningStageFirstReview,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":start",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeStart,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Reviewer, worker.RoleReviewer),
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
		"\n\n" + planningWorkspaceFacts(prepared) +
		"\n\nLead's exact proposal:\n" + proposal
}

// StartPlanningRound begins the autonomous part of the planning discussion.
// The existing lead and reviewer conversations then alternate until the lead
// submits an agreed plan or the durable shared history reaches its safety cap.
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
	if run.Paused {
		return execution.Run{}, false, ErrRunControlNotAllowed
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
	if run.Status == execution.RunStatusRunning {
		return run, false, nil
	}
	if run.Status != execution.RunStatusWaitingForUser ||
		lead.Status != execution.SessionStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser ||
		lead.ProviderSessionID == "" || reviewer.ProviderSessionID == "" ||
		lead.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) || lead.Role != worker.RoleLead ||
		reviewer.AgentID != agentID(run.AgentProviders.Reviewer, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return execution.Run{}, false, err
	}
	if len(messages) > 0 && (planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) ||
		messages[len(messages)-1].Event.Type == worker.EventPlanSubmitted) {
		if messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
			if err := starter.waitRun(
				ctx, run.ID, planningLimitReason(run.PlanningRoundLimit), execution.RunWaitKindRoundCap,
			); err != nil {
				return execution.Run{}, false, err
			}
			updated, err := starter.executions.GetRun(ctx, run.ID)
			return updated, false, err
		}
		submitted := messages[len(messages)-1].Event
		published, err := starter.planPublicationRecorded(ctx, submitted)
		if err != nil {
			return execution.Run{}, false, err
		}
		if published {
			return run, false, nil
		}
		if !starter.claim(run.ID) {
			return execution.Run{}, false, ErrPlanningNotAllowed
		}
		running, err := starter.executions.TransitionRun(
			ctx, run.ID, run.Status, execution.RunStatusRunning, planningPlanSubmittedReason,
		)
		if err != nil {
			starter.release(run.ID)
			return execution.Run{}, false, err
		}
		go starter.launchPlanPublication(run.ID, submitted)
		return running, true, nil
	}
	if len(messages) < 2 || messages[len(messages)-1].Role != worker.RoleReviewer {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	request, admitted, err := starter.startLeadResponse(ctx, run, storedFeature, messages, false)
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

// StartImplementation resumes the same lead conversation that submitted the
// agreed plan. Once that deterministic attempt exists, retries can only recheck
// its terminal publication result; they never launch a replacement agent.
func (starter *RemoteLeadStarter) StartImplementation(
	ctx context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(idempotencyKey) == "" ||
		starter.planning == nil || starter.workspaces == nil {
		return execution.Run{}, false, fmt.Errorf(
			"%w: run identity, idempotency key, or required services are missing",
			ErrImplementationNotAllowed,
		)
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Paused {
		return execution.Run{}, false, ErrRunControlNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if storedFeature.State != feature.StatePlanning &&
		storedFeature.State != feature.StateImplementing {
		return execution.Run{}, false, fmt.Errorf(
			"%w: feature state is %q", ErrImplementationNotAllowed, storedFeature.State,
		)
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return execution.Run{}, false, err
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return execution.Run{}, false, err
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return execution.Run{}, false, err
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != worker.RoleLead ||
		messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return execution.Run{}, false, fmt.Errorf(
			"%w: the last planning message is not a submitted lead plan",
			ErrImplementationNotAllowed,
		)
	}
	plan := messages[len(messages)-1].Event
	published, err := starter.planPublicationRecorded(ctx, plan)
	if err != nil {
		return execution.Run{}, false, err
	}
	if !published {
		return execution.Run{}, false, fmt.Errorf(
			"%w: the submitted plan has not been recorded as published",
			ErrImplementationNotAllowed,
		)
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return execution.Run{}, false, err
	}
	attemptID := implementationAttemptForVersion(lead.ID, run.PlanVersion, 1)
	if checkpoint.AttemptID == attemptID {
		if storedFeature.State != feature.StateImplementing {
			return execution.Run{}, false, fmt.Errorf(
				"%w: implementation attempt exists while feature state is %q",
				ErrImplementationNotAllowed, storedFeature.State,
			)
		}
		return starter.retryImplementationPublication(
			ctx, run, lead, reviewer, checkpoint,
		)
	}
	if run.Status != execution.RunStatusWaitingForUser ||
		lead.Status != execution.SessionStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser ||
		lead.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) || lead.Role != worker.RoleLead ||
		reviewer.AgentID != agentID(run.AgentProviders.Reviewer, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer ||
		lead.ProviderSessionID == "" || reviewer.ProviderSessionID == "" {
		return execution.Run{}, false, fmt.Errorf(
			"%w: run and planning sessions are not at the completed-plan checkpoint",
			ErrImplementationNotAllowed,
		)
	}
	if err := starter.confirmCompletedTurn(ctx, lead, checkpoint); err != nil {
		return execution.Run{}, false, fmt.Errorf(
			"%w: the lead's planning turn is not confirmed complete: %v",
			ErrImplementationNotAllowed, err,
		)
	}
	currentFeature, err := starter.featureForCurrentPlan(ctx, run, storedFeature)
	if err != nil {
		return execution.Run{}, false, err
	}
	var prepared workspace.Workspace
	if run.PlanVersion == 1 {
		prepared, err = starter.workspaces.VerifyPublishedPlan(
			ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
		)
	} else {
		revision, revisionErr := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
		if revisionErr != nil {
			return execution.Run{}, false, revisionErr
		}
		prepared, err = starter.workspaces.VerifyRevisedPublishedPlan(
			ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
			revision.BaselineCommitID,
		)
		prepared.BaseCommitID = revision.BaselineCommitID
	}
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("verify implementation workspace: %w", err)
	}
	request, err := starter.implementationRequest(
		run, currentFeature, lead, prepared, plan.Text, attemptID,
	)
	if err != nil {
		return execution.Run{}, false, err
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, fmt.Errorf(
			"%w: another coordinator operation still owns the run",
			ErrImplementationNotAllowed,
		)
	}
	if storedFeature.State == feature.StatePlanning {
		if _, err := starter.planning.TransitionFeature(
			ctx, storedFeature.ID, feature.StateImplementing,
			workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
			idempotencyKey,
		); err != nil {
			starter.release(run.ID)
			return execution.Run{}, false, err
		}
	}
	admitted, err := starter.executions.BeginAutonomousTurn(
		ctx, lead.ID, checkpoint, attemptID,
		feature.StateImplementing, implementationRunningReason,
	)
	if err != nil {
		starter.release(run.ID)
		if errors.Is(err, execution.ErrStateConflict) ||
			errors.Is(err, execution.ErrWorkerAttemptConflict) {
			return execution.Run{}, false, fmt.Errorf(
				"%w: implementation admission conflicted with durable execution state: %v",
				ErrImplementationNotAllowed, err,
			)
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

func (starter *RemoteLeadStarter) retryImplementationPublication(
	ctx context.Context,
	run execution.Run,
	lead execution.Session,
	reviewer execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
) (execution.Run, bool, error) {
	if run.Reason == implementationPublishedReason {
		return run, false, nil
	}
	if run.Status != execution.RunStatusWaitingForUser ||
		lead.Status != execution.SessionStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser {
		return execution.Run{}, false, fmt.Errorf(
			"%w: implementation publication cannot be rechecked while execution is active",
			ErrImplementationNotAllowed,
		)
	}
	prior, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
	})
	if err != nil {
		return execution.Run{}, false, fmt.Errorf(
			"%w: load completed implementation attempt: %v",
			ErrImplementationNotAllowed, err,
		)
	}
	if err := validateCompletedTurn(lead, checkpoint, prior); err != nil {
		return execution.Run{}, false, fmt.Errorf(
			"%w: implementation attempt is not safely complete: %v",
			ErrImplementationNotAllowed, err,
		)
	}
	if prior.Result == nil ||
		prior.Result.Disposition == workerhttp.DispositionInputRequired {
		return run, false, nil
	}
	if prior.Result.Publication == nil {
		return execution.Run{}, false, fmt.Errorf(
			"%w: completed implementation has no publication facts",
			ErrImplementationNotAllowed,
		)
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, fmt.Errorf(
			"%w: another coordinator operation still owns the run",
			ErrImplementationNotAllowed,
		)
	}
	running, err := starter.executions.TransitionRun(
		ctx, run.ID, run.Status, execution.RunStatusRunning,
		implementationVerificationReason,
	)
	if err != nil {
		starter.release(run.ID)
		return execution.Run{}, false, err
	}
	request := remoteLeadRequest{
		runID: run.ID,
		identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
		}},
	}
	go starter.launchImplementationPublicationVerification(request, *prior.Result)
	return running, true, nil
}

func (starter *RemoteLeadStarter) launchImplementationPublicationVerification(
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) {
	defer starter.release(request.runID)
	if err := starter.verifyImplementationPublication(starter.lifetime, request, result); err != nil {
		starter.requireReview(starter.lifetime, request, err)
	}
}

func (starter *RemoteLeadStarter) implementationRequest(
	run execution.Run,
	storedFeature feature.Feature,
	lead execution.Session,
	prepared workspace.Workspace,
	plan string,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent", waitingReason: implementationReadyReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{
				SessionID: lead.ID, AttemptID: attemptID,
			},
			IdempotencyKey: attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Lead, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: implementationInstructions(
				storedFeature, prepared, plan, attemptID,
			),
			OutputContract: workerhttp.OutputContractImplementationLead,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func implementationInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	plan string,
	attemptID string,
) string {
	marker := implementationPublicationMarker(attemptID)
	return "Continue the same provider conversation as the lead and begin implementation of the " +
		"agreed plan. Before modifying anything, inspect the current working directory, Git HEAD, " +
		"branch, status, and diff, and reconcile them with the durable facts below. Preserve any " +
		"unexpected user work: do not reset, clean, overwrite, or silently discard it. If the state " +
		"is missing, contradictory, or ambiguous, stop and explain the problem without making changes. " +
		"Otherwise implement the accepted goal and agreed plan and run the relevant available tests. " +
		implementationToolchainInstructions +
		implementationChecklistInstructions +
		"When you decide the implementation is ready for independent review, commit all intended work " +
		"on the assigned feature branch, push that exact HEAD to the 'commitarium' remote, and post one " +
		"Forgejo pull-request comment using the worker-provided Forgejo URL and token-file environment " +
		"variables. Never print, log, commit, or include the token in a URL. The comment must be exactly " +
		"the marker below, a blank line, the heading " +
		"'## Implementation summary', another blank line, and your concise summary. Check existing PR " +
		"comments for the marker before posting so a retry never duplicates the audit entry. Do not " +
		"change the PR body or merge the PR. Then return action 'published' with the same summary, exact " +
		"lowercase Git HEAD in commit_id, and the PR number. If any step is unsafe or cannot be confirmed, " +
		"return action 'blocked', explain why in summary, leave commit_id empty, and still return the known " +
		"PR number. Durable " +
		"repository and coordinator state are authoritative over conversational memory.\n\n" +
		"Current workflow phase: implementing\nAccepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\nAgreed implementation plan:\n" + plan +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nImplementation audit marker:\n" + marker
}

func implementationContinuationInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	plan string,
	userMessage string,
	attemptID string,
) string {
	marker := implementationPublicationMarker(attemptID)
	return "Continue the same provider conversation and implementation work after the user's " +
		"guidance below. Inspect before modifying anything: reconcile the current working directory, " +
		"Git HEAD, branch, status, and diff with the durable facts below and with the work already " +
		"completed in this conversation. Preserve all existing changes, including manual user edits; " +
		"do not reset, clean, overwrite, or repeat completed work. If partial work is ambiguous, facts " +
		"conflict, an external side effect may or may not have happened, or the guidance would change " +
		"the accepted goal or agreed plan, stop and explain the problem without making further changes. " +
		"Otherwise apply the user's guidance within the accepted plan, continue the implementation, and " +
		"run the relevant available tests. " + implementationToolchainInstructions +
		implementationChecklistInstructions +
		"When you decide the result is ready for independent review, " +
		"commit the intended work, push the exact HEAD to the 'commitarium' remote, and post one Forgejo " +
		"PR comment using the worker-provided URL and token-file environment variables. Never print, log, " +
		"commit, or include the token in a URL. The comment must contain exactly the audit " +
		"marker below, a blank line, '## Implementation summary', another blank " +
		"line, and your concise summary. Check for the marker before posting so retries do not duplicate " +
		"it. Do not change the PR body or merge. Return action 'published' with that same summary, exact " +
		"lowercase HEAD in commit_id, and the PR number. If publication is unsafe or cannot be confirmed, " +
		"return action 'blocked' with commit_id empty and explain the blocker. Durable repository, pull-request, and " +
		"coordinator state are authoritative over conversational memory.\n\n" +
		"User's continuation guidance:\n" + userMessage +
		"\n\nCurrent workflow phase: implementing\nAccepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\nAgreed implementation plan:\n" + plan +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nImplementation audit marker:\n" + marker
}

func implementationPublicationMarker(attemptID string) string {
	return workspace.ImplementationPublicationInitial.Marker(attemptID)
}

const implementationToolchainInstructions = "If an additional supported language runtime is required, " +
	"run 'commitarium-toolchain require <tool>@<exact-version>' with a generous timeout and wait for it " +
	"to finish; its shim becomes available on PATH immediately. If an operating-system package is missing, " +
	"do not run apt. Return action 'environment_required', leave commit_id empty and pull_request_number zero, " +
	"and provide only the Debian package names in system_packages plus a concise environment_reason. Commitarium " +
	"will ask the user and provision the same package set for lead, reviewer, and isolated validation. Do not use " +
	"mise use, floating versions such as latest, or repository mise configuration to provision tools. "

const implementationChecklistInstructions = "The coordinator owns a structured commit-sized checklist. " +
	"Run 'commitarium-artifact plan show' before changing files. Work through its steps in order without " +
	"waiting for an intermediate review. Immediately before beginning a pending step run " +
	"'commitarium-artifact plan start <step-id>'. Implement only that cohesive slice, run its listed " +
	"verification, and commit it with the planned commit subject. Then run " +
	"'commitarium-artifact plan complete <step-id> <lowercase-commit-id>' before continuing. Resume from " +
	"the statuses already recorded after an interruption. Do not amend, squash, or combine planned commits, " +
	"and do not create or commit a local checklist file. The final published HEAD must be the commit recorded " +
	"for the final checklist step. "

func (starter *RemoteLeadStarter) startLeadResponse(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	messages []execution.PlanningMessage,
	chained bool,
) (remoteLeadRequest, bool, error) {
	if len(messages) < 2 || planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) ||
		messages[len(messages)-1].Role != worker.RoleReviewer {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	storedFeature, err := starter.featureForCurrentPlan(ctx, run, storedFeature)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if lead.Status != execution.SessionStatusWaitingForUser ||
		lead.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) || lead.Role != worker.RoleLead ||
		lead.ProviderSessionID == "" {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	if err := starter.confirmCompletedTurn(ctx, lead, checkpoint); err != nil {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	turn := planningRoleMessageCount(messages, worker.RoleLead) + 1
	attemptID := planningAttemptForVersion(lead.ID, run.PlanVersion, turn)
	prepared, err := starter.workspaceForCurrentPlan(
		ctx, run, storedFeature.ProjectID, storedFeature.ID,
	)
	if err != nil || !prepared.CheckoutReady() {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent",
		waitingReason: "The lead has responded in the planning discussion.",
		planningStage: planningStageLeadResponse,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Lead, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions:      leadPlanningResponseInstructions(storedFeature, prepared, messages[len(messages)-1].Event.Text),
			OutputContract:    workerhttp.OutputContractPlanningLead,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	if chained {
		admitted, err := starter.executions.BeginChainedTurn(
			ctx, lead.ID, checkpoint, attemptID,
			"The lead is considering the reviewer's response.",
		)
		return request, admitted, err
	}
	admitted, err := starter.executions.BeginAutonomousTurn(
		ctx, lead.ID, checkpoint, attemptID,
		feature.StatePlanning,
		"The lead is considering the reviewer's response.",
	)
	return request, admitted, err
}

func leadPlanningResponseInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	reviewerResponse string,
) string {
	return "Continue the same planning conversation as the lead. The independent reviewer has " +
		"responded to your proposal. Inspect the managed repository and current Git state again before " +
		"answering. Do not modify files, install dependencies, commit, push, or begin implementation. " +
		"Address every material concern. If useful work or disagreement remains, respond to the reviewer " +
		"with action 'respond' and put your natural Markdown reply in content. If, and only if, you conclude " +
		"that both you and the reviewer genuinely agree and the plan completely satisfies the accepted " +
		"goal, use action 'submit_plan' and put the complete final implementation plan in content. Also " +
		"supply plan_title, plan_subtitle, and ordered steps. Each step must be one cohesive commit and " +
		"must have a stable lowercase ID, title, subtitle, detailed Markdown instructions, concrete " +
		"verification checks, and an imperative commit_subject. When action is 'respond', leave " +
		"plan_title and plan_subtitle empty and steps empty. " +
		"Do not submit merely to end the discussion. If you disagree, explain why with repository evidence. " +
		"Durable repository and workflow state are authoritative over conversational memory.\n\n" +
		"Accepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\n" + planningWorkspaceFacts(prepared) +
		"\n\nReviewer's exact response:\n" + reviewerResponse
}

func (starter *RemoteLeadStarter) startReviewerResponse(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	messages []execution.PlanningMessage,
) (remoteLeadRequest, bool, error) {
	if len(messages) < 3 || planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) ||
		messages[len(messages)-1].Role != worker.RoleLead ||
		messages[len(messages)-1].Event.Type == worker.EventPlanSubmitted {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	storedFeature, err := starter.featureForCurrentPlan(ctx, run, storedFeature)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if reviewer.Status != execution.SessionStatusWaitingForUser ||
		reviewer.AgentID != agentID(run.AgentProviders.Reviewer, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer ||
		reviewer.ProviderSessionID == "" {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	if err := starter.confirmCompletedTurn(ctx, reviewer, checkpoint); err != nil {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	prepared, err := starter.workspaceForCurrentPlan(
		ctx, run, storedFeature.ProjectID, storedFeature.ID,
	)
	if err != nil || !prepared.CheckoutReady() {
		return remoteLeadRequest{}, false, ErrPlanningNotAllowed
	}
	turn := planningRoleMessageCount(messages, worker.RoleReviewer) + 1
	nextAttemptID := planningAttemptForVersion(reviewer.ID, run.PlanVersion, turn)
	request, err := starter.reviewerResponseRequest(
		run, storedFeature, reviewer, prepared, messages[len(messages)-1].Event.Text, nextAttemptID,
	)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	admitted, err := starter.executions.BeginChainedTurn(
		ctx, reviewer.ID, checkpoint, nextAttemptID,
		"The reviewer is evaluating the lead's latest planning response.",
	)
	return request, admitted, err
}

func (starter *RemoteLeadStarter) reviewerResponseRequest(
	run execution.Run,
	storedFeature feature.Feature,
	reviewer execution.Session,
	prepared workspace.Workspace,
	leadResponse string,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID: run.ID, agentName: "reviewer",
		waitingReason: "The reviewer has responded to the lead's plan.",
		planningStage: planningStageReviewerResponse,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Reviewer, worker.RoleReviewer),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: reviewer.ProviderSessionID,
			Instructions:      reviewerResponseInstructions(storedFeature, prepared, leadResponse),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func reviewerResponseInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	leadResponse string,
) string {
	return "Continue the same planning conversation as the independent reviewer. Inspect the managed " +
		"repository and current Git state again. Do not modify files, install dependencies, commit, push, " +
		"or begin implementation. Evaluate whether the lead's response and current plan satisfy the accepted " +
		"goal and resolve every material concern. Respond naturally: clearly explain remaining concerns, or " +
		"clearly explain why you are satisfied. Do not use a special approval marker; the lead owns the later " +
		"structured plan-submission action after considering your response. " +
		"Durable repository and workflow state are authoritative over conversational memory.\n\n" +
		"Accepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\n" + planningWorkspaceFacts(prepared) +
		"\n\nLead's exact latest response:\n" + leadResponse
}

func planningRoleMessageCount(messages []execution.PlanningMessage, role worker.Role) int {
	count := 0
	for _, message := range messages {
		if message.Role == role {
			count++
		}
	}
	return count
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
		if event.WorkerAttemptID != attemptID ||
			(event.Type != worker.EventMessage && event.Type != worker.EventPlanSubmitted) {
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
		"implementation plan for a separate reviewer agent to challenge. Break it into small ordered " +
		"slices where each slice should produce one cohesive commit, with a short title, subtitle, " +
		"implementation details, verification, and intended commit subject. Call out assumptions, risks, " +
		"likely files or components, and how the result should be tested. Durable repository and " +
		"workflow state are authoritative over conversational memory.\n\nAccepted goal:\n" +
		storedFeature.AcceptedGoal + "\n\n" + planningWorkspaceFacts(prepared)
}

func planningWorkspaceFacts(prepared workspace.Workspace) string {
	return "Repository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch +
		"\nBase commit: " + prepared.BaseCommitID +
		"\nReserved feature branch name: " + prepared.Branch +
		"\nThe coordinator creates the feature branch and draft pull request only after " +
		"the final agreed plan is submitted. Do not require either resource during planning."
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
	run, err := starter.executions.GetRun(ctx, session.RunID)
	if err != nil {
		return workflow.Event{}, err
	}
	if session.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) || session.Role != worker.RoleLead {
		return workflow.Event{}, workflow.ErrGoalAcceptanceNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return workflow.Event{}, err
	}
	event, err := starter.goals.AcceptGoal(
		ctx, storedFeature.ID, session.ID, goal, actor, idempotencyKey,
	)
	if err != nil {
		return workflow.Event{}, err
	}

	// Idempotent retries can encounter an acceptance written by an older
	// coordinator, before acceptance also stored the planning checkpoint.
	// Normalize that durable state before considering automatic dispatch.
	run, err = starter.executions.GetRun(ctx, run.ID)
	if err != nil {
		return workflow.Event{}, err
	}
	if run.Status == execution.RunStatusWaitingForUser && !run.Paused &&
		run.WaitKind == execution.RunWaitKindClarification {
		if err := starter.waitRun(
			ctx, run.ID, workflow.GoalAcceptedPlanningReason,
			execution.RunWaitKindPhaseCheckpoint,
		); err != nil {
			return workflow.Event{}, err
		}
		run, err = starter.executions.GetRun(ctx, run.ID)
		if err != nil {
			return workflow.Event{}, err
		}
	}
	if run.AutonomyPolicy == project.AutonomyPolicyRunToCompletion && !run.Paused {
		if err := starter.advanceWaitingRun(context.WithoutCancel(ctx), run.ID); err != nil {
			starter.requireReview(context.WithoutCancel(ctx), remoteLeadRequest{
				runID: run.ID,
				identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
					SessionID: session.ID,
				}},
			}, err)
		}
	}
	return event, nil
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
	run, err := starter.executions.GetRun(ctx, session.RunID)
	if err != nil {
		return execution.Command{}, err
	}
	if session.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) || session.Role != worker.RoleLead ||
		session.Status != execution.SessionStatusWaitingForUser ||
		session.ProviderSessionID == "" {
		return execution.Command{}, commandStateError(command.Type, session.Status)
	}
	if run.Status != execution.RunStatusWaitingForUser {
		return execution.Command{}, ErrCommandNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Command{}, fmt.Errorf("load lead feature: %w", err)
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

	var request remoteLeadRequest
	var expectedState feature.State
	var runReason string
	switch storedFeature.State {
	case feature.StateDraft:
		if storedFeature.AcceptedGoal != "" || storedFeature.GoalAcceptedAt != nil {
			return execution.Command{}, ErrCommandNotAllowed
		}
		request, err = starter.replyRequest(ctx, run, storedFeature, session, command)
		expectedState = feature.StateDraft
		runReason = "The lead agent is responding to the user's message."
	case feature.StateImplementing:
		request, err = starter.implementationContinuationRequest(
			ctx, run, storedFeature, session, checkpoint, command,
		)
		expectedState = feature.StateImplementing
		runReason = implementationContinuationReason
	default:
		return execution.Command{}, ErrCommandNotAllowed
	}
	if err != nil {
		return execution.Command{}, err
	}
	if !starter.claim(run.ID) {
		return execution.Command{}, ErrCommandNotAllowed
	}
	result, admitted, err := starter.executions.BeginWorkerTurn(
		ctx, command, session.ID, checkpoint, request.identity.AttemptID,
		expectedState, runReason,
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

func (starter *RemoteLeadStarter) implementationContinuationRequest(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	lead execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	command worker.Command,
) (remoteLeadRequest, error) {
	if strings.TrimSpace(storedFeature.AcceptedGoal) == "" ||
		storedFeature.GoalAcceptedAt == nil || starter.workspaces == nil {
		return remoteLeadRequest{}, ErrCommandNotAllowed
	}
	attemptVersion, turn, implementing := workflowAttemptVersionAndTurn(
		lead.ID, "implementation", checkpoint.AttemptID,
	)
	if !implementing || attemptVersion != run.PlanVersion {
		return remoteLeadRequest{}, ErrCommandNotAllowed
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return remoteLeadRequest{}, err
	}
	if reviewer.Status != execution.SessionStatusWaitingForUser ||
		reviewer.AgentID != agentID(run.AgentProviders.Reviewer, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer ||
		reviewer.ProviderSessionID == "" {
		return remoteLeadRequest{}, ErrCommandNotAllowed
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return remoteLeadRequest{}, err
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != worker.RoleLead ||
		messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return remoteLeadRequest{}, ErrCommandNotAllowed
	}
	plan := messages[len(messages)-1].Event
	published, err := starter.planPublicationRecorded(ctx, plan)
	if err != nil {
		return remoteLeadRequest{}, err
	}
	if !published {
		return remoteLeadRequest{}, ErrCommandNotAllowed
	}
	currentFeature, err := starter.featureForCurrentPlan(ctx, run, storedFeature)
	if err != nil {
		return remoteLeadRequest{}, err
	}
	var prepared workspace.Workspace
	if run.PlanVersion == 1 {
		prepared, err = starter.workspaces.VerifyImplementationContinuation(
			ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
		)
	} else {
		revision, revisionErr := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
		if revisionErr != nil {
			return remoteLeadRequest{}, revisionErr
		}
		prepared, err = starter.workspaces.VerifyRevisedPublishedPlan(
			ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
			revision.BaselineCommitID,
		)
		prepared.BaseCommitID = revision.BaselineCommitID
	}
	if err != nil {
		return remoteLeadRequest{}, fmt.Errorf("verify implementation continuation workspace: %w", err)
	}
	attemptID := implementationAttemptForVersion(lead.ID, run.PlanVersion, turn+1)
	request := remoteLeadRequest{
		runID: run.ID, commandID: command.ID, agentName: "lead agent",
		waitingReason: implementationReadyReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{
				SessionID: lead.ID, AttemptID: attemptID,
			},
			IdempotencyKey: attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Lead, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: implementationContinuationInstructions(
				currentFeature, prepared, plan.Text, command.Message, attemptID,
			),
			OutputContract: workerhttp.OutputContractImplementationLead,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
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
	starter.launchAdmitted(request)
	starter.release(request.runID)
	if err := starter.advanceWaitingRun(starter.lifetime, request.runID); err != nil {
		starter.requireReview(starter.lifetime, request, err)
	}
}

// launchAdmitted runs a turn whose coordinator admission is already durable.
// It does not acquire or release the run claim, allowing one goroutine to keep
// ownership while handing a completed lead revision directly to the reviewer.
func (starter *RemoteLeadStarter) launchAdmitted(request remoteLeadRequest) {
	ctx := starter.lifetime
	if request.planningStage != planningStageNone ||
		request.request.OutputContract == workerhttp.OutputContractGoalClarification {
		request.request.WorkspaceAccess = workerhttp.WorkspaceAccessReadOnly
	} else {
		request.request.WorkspaceAccess = workerhttp.WorkspaceAccessReadWrite
	}
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
	defer func() {
		starter.release(request.runID)
		if err := starter.advanceWaitingRun(starter.lifetime, request.runID); err != nil {
			starter.requireReview(starter.lifetime, request, err)
		}
	}()
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
		if attempt.Result.GoalDraft != nil && starter.artifacts != nil {
			run, loadErr := starter.executions.GetRun(ctx, request.runID)
			if loadErr != nil {
				starter.requireReview(ctx, request, loadErr)
				return
			}
			if _, artifactErr := starter.artifacts.UpsertGoalDraft(
				ctx, run.FeatureID, *attempt.Result.GoalDraft,
				workflow.Actor{Kind: workflow.ActorKindAgent, ID: session.ID},
				request.identity.AttemptID+":goal-draft",
			); artifactErr != nil {
				starter.requireReview(ctx, request, artifactErr)
				return
			}
		}
		if request.interventionID != "" {
			if !worker.InterventionEffect(attempt.Result.InterventionEffect).IsValid() {
				starter.requireReview(ctx, request, errors.New("completed intervention omitted its structured effect"))
				return
			}
			_, _, err = starter.executions.CompleteIntervention(
				ctx, request.interventionID, request.identity.AttemptID,
				attempt.ProviderSessionID,
				worker.InterventionEffect(attempt.Result.InterventionEffect),
				interventionAnsweredReason,
			)
			if err != nil {
				starter.requireReview(ctx, request, err)
			}
			return
		}
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
		if request.planningStage == planningStageLeadResponse {
			if planningMessage.Type == worker.EventPlanSubmitted {
				if starter.artifacts != nil {
					if attempt.Result.ImplementationPlan == nil {
						starter.requireReview(ctx, request, errors.New("submitted plan omitted its structured implementation checklist"))
						return
					}
					run, loadErr := starter.executions.GetRun(ctx, request.runID)
					if loadErr != nil {
						starter.requireReview(ctx, request, loadErr)
						return
					}
					normalized, normalizeErr := attempt.Result.ImplementationPlan.NormalizeInitial(run.PlanVersion)
					if normalizeErr != nil {
						starter.requireReview(ctx, request, normalizeErr)
						return
					}
					if _, artifactErr := starter.artifacts.UpsertImplementationPlan(
						ctx, run.FeatureID, normalized,
						workflow.Actor{Kind: workflow.ActorKindAgent, ID: session.ID},
						request.identity.AttemptID+":implementation-plan",
					); artifactErr != nil {
						starter.requireReview(ctx, request, artifactErr)
						return
					}
				}
				if publishErr := starter.publishSubmittedPlan(
					ctx, request.runID, planningMessage,
				); publishErr != nil {
					starter.requirePlanPublicationReview(
						ctx, request.runID, planningMessage, publishErr,
					)
				}
				return
			}
			run, loadErr := starter.executions.GetRun(ctx, request.runID)
			if loadErr != nil {
				err = loadErr
				break
			}
			if run.Paused {
				err = starter.waitRun(ctx, request.runID, request.waitingReason, execution.RunWaitKindPhaseCheckpoint)
				break
			}
			storedFeature, loadErr := starter.features.GetByID(ctx, run.FeatureID)
			if loadErr != nil {
				err = loadErr
				break
			}
			messages, listErr := starter.currentPlanningMessages(ctx, run)
			if listErr != nil {
				err = listErr
				break
			}
			if planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) {
				err = starter.waitRun(ctx, request.runID, planningLimitReason(run.PlanningRoundLimit), execution.RunWaitKindRoundCap)
				break
			}
			next, admitted, startErr := starter.startReviewerResponse(ctx, run, storedFeature, messages)
			if startErr != nil {
				err = startErr
				break
			}
			if admitted {
				starter.launchAdmitted(next)
			}
			return
		}
		if request.planningStage == planningStageReviewerResponse {
			run, loadErr := starter.executions.GetRun(ctx, request.runID)
			if loadErr != nil {
				err = loadErr
				break
			}
			if run.Paused {
				err = starter.waitRun(ctx, request.runID, request.waitingReason, execution.RunWaitKindPhaseCheckpoint)
				break
			}
			messages, listErr := starter.currentPlanningMessages(ctx, run)
			if listErr != nil {
				err = listErr
				break
			}
			if planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) {
				err = starter.waitRun(ctx, request.runID, planningLimitReason(run.PlanningRoundLimit), execution.RunWaitKindRoundCap)
				break
			}
			storedFeature, loadErr := starter.features.GetByID(ctx, run.FeatureID)
			if loadErr != nil {
				err = loadErr
				break
			}
			next, admitted, startErr := starter.startLeadResponse(ctx, run, storedFeature, messages, true)
			if startErr != nil {
				err = startErr
				break
			}
			if admitted {
				starter.launchAdmitted(next)
			}
			return
		}
		if request.request.OutputContract == workerhttp.OutputContractImplementationLead {
			if attempt.Result.Disposition == workerhttp.DispositionInputRequired {
				err = starter.handleInputRequired(ctx, request, *attempt.Result)
				break
			}
			if _, correcting := implementationCorrectionTurnNumber(
				request.identity.SessionID, request.identity.AttemptID,
			); correcting {
				err = starter.verifyImplementationCorrection(ctx, request, *attempt.Result)
			} else {
				err = starter.verifyImplementationPublication(ctx, request, *attempt.Result)
			}
			break
		}
		if request.request.OutputContract == workerhttp.OutputContractImplementationReview {
			if attempt.Result.Disposition == workerhttp.DispositionInputRequired {
				err = starter.handleInputRequired(ctx, request, *attempt.Result)
				break
			}
			err = starter.verifyImplementationReview(ctx, request, *attempt.Result)
			break
		}
		if request.request.OutputContract == workerhttp.OutputContractImplementationReadiness {
			if attempt.Result.Disposition == workerhttp.DispositionInputRequired {
				err = starter.handleInputRequired(ctx, request, *attempt.Result)
				break
			}
			err = starter.verifyImplementationReadiness(ctx, request, *attempt.Result)
			break
		}
		err = starter.waitRun(ctx, request.runID, request.waitingReason, request.waitKind)
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

func (starter *RemoteLeadStarter) verifyImplementationPublication(
	ctx context.Context,
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) error {
	if result.Publication == nil {
		return errors.New("lead completed implementation without structured publication facts")
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return err
	}
	if len(messages) == 0 || messages[len(messages)-1].Role != worker.RoleLead ||
		messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return errors.New("implementation publication has no durable agreed plan")
	}
	if starter.artifacts != nil {
		if _, correcting := implementationCorrectionTurnNumber(request.identity.SessionID, request.identity.AttemptID); !correcting {
			artifact, artifactErr := starter.artifacts.GetFeatureArtifact(
				ctx, run.FeatureID, featureartifact.KindImplementationPlan,
			)
			if artifactErr != nil {
				return fmt.Errorf("load implementation checklist: %w", artifactErr)
			}
			checklist := featureartifact.ImplementationPlan{}
			if decodeErr := json.Unmarshal([]byte(artifact.Document), &checklist); decodeErr != nil {
				return fmt.Errorf("decode implementation checklist: %w", decodeErr)
			}
			if checklist.PlanVersion != run.PlanVersion || !checklist.Complete() ||
				checklist.Steps[len(checklist.Steps)-1].CommitID != result.Publication.CommitID {
				return errors.New("implementation checklist is incomplete or does not end at the published commit")
			}
		}
	}
	plan := messages[len(messages)-1].Event
	var verifyErr error
	if run.PlanVersion == 1 {
		_, verifyErr = starter.workspaces.VerifyImplementationPublication(
			ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
			request.identity.AttemptID, result.Summary, result.Publication.CommitID,
			result.Publication.PullRequestNumber, starter.forgejoAuthorFor(run, worker.RoleLead),
		)
	} else {
		revision, revisionErr := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
		if revisionErr != nil {
			return revisionErr
		}
		_, verifyErr = starter.workspaces.VerifyRevisedImplementationPublication(
			ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
			request.identity.AttemptID, result.Summary, result.Publication.CommitID,
			result.Publication.PullRequestNumber, starter.forgejoAuthorFor(run, worker.RoleLead),
			revision.BaselineCommitID,
		)
	}
	if verifyErr != nil {
		return fmt.Errorf("verify lead publication before review: %w", verifyErr)
	}
	_, err = starter.executions.RecordSessionEventWithID(
		ctx,
		request.identity.AttemptID+":publication-verified",
		request.identity.SessionID,
		worker.Event{
			Type: worker.EventActivity,
			Text: fmt.Sprintf(
				"Verified lead publication: commit %s is the head of Forgejo pull request #%d and its audit comment is attributed to %s.",
				result.Publication.CommitID,
				result.Publication.PullRequestNumber,
				starter.forgejoAuthorFor(run, worker.RoleLead),
			),
		},
	)
	if err != nil {
		return fmt.Errorf("record verified implementation publication: %w", err)
	}
	if paused, err := starter.stopAtPauseBoundary(
		ctx, run.ID, "Implementation is published and ready for independent review.",
	); err != nil || paused {
		return err
	}
	next, admitted, err := starter.startImplementationReview(
		ctx, run, storedFeature, plan, result.Summary, *result.Publication, 1,
	)
	if err != nil {
		return err
	}
	if admitted {
		starter.launchAdmitted(next)
	}
	return nil
}

func (starter *RemoteLeadStarter) startImplementationReview(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	plan execution.Event,
	leadSummary string,
	publication workerhttp.ImplementationPublication,
	round int,
) (remoteLeadRequest, bool, error) {
	if strings.TrimSpace(leadSummary) == "" || round < 1 || publication.Validate() != nil {
		return remoteLeadRequest{}, false, errors.New("implementation review subject is missing")
	}
	storedFeature, err := starter.featureForCurrentPlan(ctx, run, storedFeature)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if reviewer.Status != execution.SessionStatusWaitingForUser ||
		reviewer.AgentID != agentID(run.AgentProviders.Reviewer, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer ||
		reviewer.ProviderSessionID == "" {
		return remoteLeadRequest{}, false, errors.New("reviewer planning conversation is not safely resumable")
	}
	priorVersion, priorRound, alreadyReviewing := workflowAttemptVersionAndTurn(
		reviewer.ID, "review", checkpoint.AttemptID,
	)
	alreadyReviewing = alreadyReviewing && priorVersion == run.PlanVersion
	if alreadyReviewing {
		if priorRound >= round {
			return remoteLeadRequest{}, false, nil
		}
		if priorRound != round-1 {
			return remoteLeadRequest{}, false, errors.New("reviewer checkpoint does not precede the requested review round")
		}
	} else if round != 1 {
		return remoteLeadRequest{}, false, errors.New("reviewer has no preceding implementation review")
	}
	if err := starter.confirmCompletedTurn(ctx, reviewer, checkpoint); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("confirm reviewer planning turn: %w", err)
	}
	prepared, err := starter.workspaceForCurrentPlan(
		ctx, run, storedFeature.ProjectID, storedFeature.ID,
	)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	attemptID := implementationReviewAttemptForVersion(reviewer.ID, run.PlanVersion, round)
	request := remoteLeadRequest{
		runID: run.ID, agentName: "reviewer", waitingReason: implementationReviewRunningReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Reviewer, worker.RoleReviewer),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: reviewer.ProviderSessionID,
			Instructions: implementationReviewInstructions(
				storedFeature, prepared, plan.Text, leadSummary,
				publication.CommitID, attemptID,
			),
			OutputContract: workerhttp.OutputContractImplementationReview,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	if storedFeature.State == feature.StateImplementing {
		if _, err := starter.planning.TransitionFeature(
			ctx, storedFeature.ID, feature.StateReviewing,
			workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
			attemptID+":enter-review",
		); err != nil {
			return remoteLeadRequest{}, false, err
		}
	}
	admitted, err := starter.executions.BeginChainedTurnInState(
		ctx, reviewer.ID, checkpoint, attemptID, feature.StateReviewing,
		implementationReviewRunningReason,
	)
	return request, admitted, err
}

func implementationReviewInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	plan string,
	implementationSummary string,
	commitID string,
	attemptID string,
) string {
	marker := implementationReviewMarker(attemptID)
	return "Continue the same provider conversation as the independent reviewer. The lead has now " +
		"published an implementation for review. Inspect before judging: confirm the current branch, " +
		"Git HEAD, status, diff from the planning baseline, and the exact pull-request head. Review only " +
		"the exact commit below against the accepted goal and agreed plan, and run relevant read-only tests " +
		"when practical. Do not modify tracked files, commit, push, change the pull-request body, or merge. " +
		"If you find material problems, submit one formal Forgejo review with event REQUEST_CHANGES. If the " +
		"implementation is correct and sufficiently tested, tell the lead that you think the exact revision is " +
		"ready to merge and submit one review with event APPROVED. Use the worker-provided " +
		"Forgejo URL and token-file environment variables; never print, log, commit, or put the token in a URL. " +
		"Set commit_id on the review to the exact commit below. The review body must begin with exactly the marker below, " +
		"a blank line, '## Review', and another blank line, followed by your structured findings or approval. " +
		"Check existing reviews for the marker before posting so recovery never duplicates it. Return action " +
		"'approved' or 'changes_requested' with a concise session summary, exact commit ID, PR number, and returned review ID. " +
		"If state is contradictory, the exact revision is unavailable, or you cannot safely establish whether a " +
		"review was posted, return action 'blocked', leave commit_id empty and review_id zero, and explain why. " +
		"Durable Git, Forgejo, and coordinator state are authoritative over conversational memory.\n\n" +
		"Current workflow phase: reviewing\nAccepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\nAgreed implementation plan:\n" + plan +
		"\n\nLead's latest implementation or readiness summary:\n" + implementationSummary +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID + "\nExact implementation commit: " + commitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nReview audit marker:\n" + marker
}

func implementationReviewMarker(attemptID string) string {
	digest := sha256.Sum256([]byte(attemptID))
	return "<!-- commitarium-review: " + hex.EncodeToString(digest[:]) + " -->"
}

func (starter *RemoteLeadStarter) startImplementationCorrection(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	plan execution.Event,
	review workerhttp.TerminalResult,
	round int,
) (remoteLeadRequest, bool, error) {
	if review.Review == nil || round < 1 {
		return remoteLeadRequest{}, false, errors.New("review facts or correction round are missing")
	}
	storedFeature, err := starter.featureForCurrentPlan(ctx, run, storedFeature)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if lead.Status != execution.SessionStatusWaitingForUser ||
		lead.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) || lead.Role != worker.RoleLead ||
		lead.ProviderSessionID == "" {
		return remoteLeadRequest{}, false, errors.New("lead implementation conversation is not safely resumable")
	}
	priorVersion, priorRound, alreadyCorrecting := workflowAttemptVersionAndTurn(
		lead.ID, "correction", checkpoint.AttemptID,
	)
	alreadyCorrecting = alreadyCorrecting && priorVersion == run.PlanVersion
	if alreadyCorrecting {
		if priorRound >= round {
			return remoteLeadRequest{}, false, nil
		}
		if priorRound != round-1 {
			return remoteLeadRequest{}, false, errors.New("lead correction checkpoint does not precede the requested round")
		}
	} else if readinessVersion, readinessRound, acknowledged := workflowAttemptVersionAndTurn(
		lead.ID, "readiness", checkpoint.AttemptID,
	); acknowledged && readinessVersion == run.PlanVersion {
		if readinessRound != round-1 {
			return remoteLeadRequest{}, false, errors.New("lead readiness checkpoint does not precede the requested correction round")
		}
	} else if round != 1 {
		return remoteLeadRequest{}, false, errors.New("lead has no preceding correction round")
	} else if implementationVersion, _, implementing := workflowAttemptVersionAndTurn(
		lead.ID, "implementation", checkpoint.AttemptID,
	); !implementing || implementationVersion != run.PlanVersion {
		return remoteLeadRequest{}, false, errors.New("lead has no completed implementation to correct")
	}
	if err := starter.confirmCompletedTurn(ctx, lead, checkpoint); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("confirm lead implementation turn: %w", err)
	}
	prepared, err := starter.workspaceForCurrentPlan(
		ctx, run, storedFeature.ProjectID, storedFeature.ID,
	)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	attemptID := implementationCorrectionAttemptForVersion(lead.ID, run.PlanVersion, round)
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent", waitingReason: implementationCorrectionRunningReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Lead, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: implementationCorrectionInstructions(
				storedFeature, prepared, plan.Text, review.Summary,
				review.Review.CommitID, review.Review.ReviewID, attemptID,
			),
			OutputContract: workerhttp.OutputContractImplementationLead,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	admitted, err := starter.executions.BeginChainedTurnInState(
		ctx, lead.ID, checkpoint, attemptID, feature.StateReviewing,
		implementationCorrectionRunningReason,
	)
	return request, admitted, err
}

func implementationCorrectionInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	plan string,
	reviewSummary string,
	reviewedCommitID string,
	reviewID int64,
	attemptID string,
) string {
	marker := implementationReviewResponseMarker(attemptID)
	return "Continue the same provider conversation as the lead. The independent reviewer requested " +
		"changes to the exact commit below. Inspect before modifying anything: reconcile the working " +
		"directory, branch, Git HEAD, status, diff, pull-request head, and existing review with these durable " +
		"facts. Do not repeat completed work or discard unexpected user changes. Address every material review " +
		"finding while preserving the accepted goal and agreed plan, then run the relevant tests. " +
		implementationToolchainInstructions + "When the " +
		"correction is ready, create a new commit descended from the reviewed commit, push that exact HEAD to " +
		"the 'commitarium' remote, and post one pull-request comment using the worker-provided Forgejo URL and " +
		"token-file environment variables. Never print, log, commit, or include the token in a URL. The comment " +
		"must be exactly the marker below, a blank line, '## Review response', another blank line, and a concise " +
		"structured account of how the findings were addressed and tested. Check existing comments for the marker " +
		"before posting so recovery never duplicates it. Do not change the PR body or merge. Return action " +
		"'published' with that same summary, the new lowercase Git HEAD in commit_id, and the PR number. If state " +
		"is contradictory, the prior commit or review is unavailable, work is ambiguous, or publication cannot " +
		"be confirmed, return action 'blocked', leave commit_id empty, and explain why. Durable Git, Forgejo, and " +
		"coordinator state are authoritative over conversational memory.\n\n" +
		"Current workflow phase: reviewing (corrective implementation)\nAccepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\nAgreed implementation plan:\n" + plan +
		"\n\nVerified reviewer findings:\n" + reviewSummary +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID +
		"\nExact reviewed commit: " + reviewedCommitID +
		fmt.Sprintf("\nFormal review: #%d\nDraft pull request: #%d (%s)", reviewID, prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nReview-response audit marker:\n" + marker
}

func implementationReviewResponseMarker(attemptID string) string {
	return workspace.ImplementationPublicationReviewResponse.Marker(attemptID)
}

func (starter *RemoteLeadStarter) launchImplementationCorrectionVerification(
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) {
	defer starter.release(request.runID)
	if err := starter.verifyImplementationCorrection(starter.lifetime, request, result); err != nil {
		starter.requireReview(starter.lifetime, request, err)
	}
}

func (starter *RemoteLeadStarter) verifyImplementationCorrection(
	ctx context.Context,
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) error {
	if result.Publication == nil {
		return errors.New("lead completed correction without structured publication facts")
	}
	round, correcting := implementationCorrectionTurnNumber(
		request.identity.SessionID, request.identity.AttemptID,
	)
	if !correcting {
		return errors.New("lead correction has an invalid attempt identity")
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return err
	}
	if len(messages) == 0 || messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return errors.New("implementation correction has no durable agreed plan")
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return err
	}
	reviewCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return err
	}
	reviewRound, reviewing := implementationReviewTurnNumber(reviewer.ID, reviewCheckpoint.AttemptID)
	if !reviewing || reviewRound != round {
		return errors.New("implementation correction has no matching completed review")
	}
	if err := starter.confirmCompletedTurn(ctx, reviewer, reviewCheckpoint); err != nil {
		return fmt.Errorf("confirm review before correction verification: %w", err)
	}
	reviewAttempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: reviewer.ID, AttemptID: reviewCheckpoint.AttemptID,
	})
	if err != nil {
		return fmt.Errorf("load review before correction verification: %w", err)
	}
	if reviewAttempt.Result == nil || reviewAttempt.Result.Review == nil ||
		reviewAttempt.Result.Disposition != workerhttp.DispositionChangesRequested {
		return errors.New("implementation correction does not follow a changes-requested review")
	}
	plan := messages[len(messages)-1].Event
	if _, err := starter.workspaces.VerifyImplementationReviewResponse(
		ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
		request.identity.AttemptID, result.Summary, reviewAttempt.Result.Review.CommitID,
		result.Publication.CommitID, result.Publication.PullRequestNumber, starter.forgejoAuthorFor(run, worker.RoleLead),
	); err != nil {
		return fmt.Errorf("verify lead review response: %w", err)
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, request.identity.AttemptID+":response-verified", request.identity.SessionID,
		worker.Event{Type: worker.EventActivity, Text: fmt.Sprintf(
			"Verified lead response to review #%d: corrected commit %s is the head of Forgejo pull request #%d and its audit comment is attributed to %s.",
			reviewAttempt.Result.Review.ReviewID, result.Publication.CommitID,
			result.Publication.PullRequestNumber, starter.forgejoAuthorFor(run, worker.RoleLead),
		)},
	); err != nil {
		return fmt.Errorf("record verified implementation review response: %w", err)
	}
	if implementationReviewRoundLimitReached(run.ImplementationReviewRoundLimit, round) {
		return starter.waitRun(ctx, request.runID, implementationReviewLimitReason(run.ImplementationReviewRoundLimit), execution.RunWaitKindRoundCap)
	}
	if paused, err := starter.stopAtPauseBoundary(
		ctx, run.ID, "The correction is published and ready for another independent review.",
	); err != nil || paused {
		return err
	}
	next, admitted, err := starter.startImplementationReview(
		ctx, run, storedFeature, plan, result.Summary, *result.Publication, round+1,
	)
	if err != nil {
		return err
	}
	if admitted {
		starter.launchAdmitted(next)
	}
	return nil
}

func (starter *RemoteLeadStarter) launchImplementationReviewVerification(
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) {
	defer starter.release(request.runID)
	if err := starter.verifyImplementationReview(starter.lifetime, request, result); err != nil {
		starter.requireReview(starter.lifetime, request, err)
	}
}

func (starter *RemoteLeadStarter) verifyImplementationReview(
	ctx context.Context,
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) error {
	if result.Review == nil {
		return errors.New("reviewer completed without structured review facts")
	}
	round, reviewing := implementationReviewTurnNumber(
		request.identity.SessionID, request.identity.AttemptID,
	)
	if !reviewing {
		return errors.New("implementation review has an invalid attempt identity")
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return err
	}
	if len(messages) == 0 || messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return errors.New("implementation review has no durable agreed plan")
	}
	expectedState := "APPROVED"
	if result.Disposition == workerhttp.DispositionChangesRequested {
		expectedState = "REQUEST_CHANGES"
	}
	plan := messages[len(messages)-1].Event
	if _, err := starter.workspaces.VerifyImplementationReview(
		ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
		request.identity.AttemptID, result.Summary, result.Review.CommitID,
		result.Review.PullRequestNumber, result.Review.ReviewID,
		starter.forgejoAuthorFor(run, worker.RoleReviewer), expectedState,
	); err != nil {
		return fmt.Errorf("verify reviewer publication: %w", err)
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, implementationReviewVerificationEventID(request.identity.AttemptID), request.identity.SessionID,
		worker.Event{Type: worker.EventActivity, Text: fmt.Sprintf(
			"Verified %s review #%d by %s for commit %s in Forgejo pull request #%d.",
			strings.ToLower(expectedState), result.Review.ReviewID, starter.forgejoAuthorFor(run, worker.RoleReviewer),
			result.Review.CommitID, result.Review.PullRequestNumber,
		)},
	); err != nil {
		return fmt.Errorf("record verified implementation review: %w", err)
	}
	if paused, err := starter.stopAtPauseBoundary(
		ctx, run.ID, "The independent review is complete and the lead's response is pending.",
	); err != nil || paused {
		return err
	}
	switch nextImplementationReviewAction(result.Disposition) {
	case implementationReviewActionCorrect:
		next, admitted, err := starter.startImplementationCorrection(
			ctx, run, storedFeature, plan, result, round,
		)
		if err != nil {
			return err
		}
		if admitted {
			starter.launchAdmitted(next)
		}
		return nil
	case implementationReviewActionAcknowledge:
		next, admitted, err := starter.startImplementationReadiness(
			ctx, run, storedFeature, plan, result, round,
		)
		if err != nil {
			return err
		}
		if admitted {
			starter.launchAdmitted(next)
		}
		return nil
	default:
		return errors.New("implementation review produced an unsupported next action")
	}
}

func (starter *RemoteLeadStarter) startImplementationReadiness(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	plan execution.Event,
	review workerhttp.TerminalResult,
	round int,
) (remoteLeadRequest, bool, error) {
	if review.Review == nil || review.Disposition != workerhttp.DispositionSucceeded || round < 1 {
		return remoteLeadRequest{}, false, errors.New("approved review facts are missing")
	}
	storedFeature, err := starter.featureForCurrentPlan(ctx, run, storedFeature)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if lead.Status != execution.SessionStatusWaitingForUser ||
		lead.AgentID != agentID(run.AgentProviders.Lead, worker.RoleLead) || lead.Role != worker.RoleLead ||
		lead.ProviderSessionID == "" {
		return remoteLeadRequest{}, false, errors.New("lead conversation is not safely resumable for merge readiness")
	}
	priorRound := 0
	if correctionVersion, correctionRound, correcting := workflowAttemptVersionAndTurn(
		lead.ID, "correction", checkpoint.AttemptID,
	); correcting && correctionVersion == run.PlanVersion {
		priorRound = correctionRound
	} else if readinessVersion, readinessRound, acknowledging := workflowAttemptVersionAndTurn(
		lead.ID, "readiness", checkpoint.AttemptID,
	); acknowledging && readinessVersion == run.PlanVersion {
		priorRound = readinessRound
	} else if implementationVersion, _, implementing := workflowAttemptVersionAndTurn(
		lead.ID, "implementation", checkpoint.AttemptID,
	); !implementing || implementationVersion != run.PlanVersion {
		return remoteLeadRequest{}, false, errors.New("lead has no implementation review response checkpoint")
	}
	if priorRound >= round {
		return remoteLeadRequest{}, false, nil
	}
	if priorRound != round-1 {
		return remoteLeadRequest{}, false, errors.New("lead response checkpoint does not precede readiness round")
	}
	if err := starter.confirmCompletedTurn(ctx, lead, checkpoint); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("confirm lead before merge-readiness acknowledgement: %w", err)
	}
	prepared, err := starter.workspaceForCurrentPlan(
		ctx, run, storedFeature.ProjectID, storedFeature.ID,
	)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	attemptID := implementationReadinessAttemptForVersion(lead.ID, run.PlanVersion, round)
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent", waitingReason: implementationReadinessRunningReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.Lead, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: implementationReadinessInstructions(
				storedFeature, prepared, plan.Text, review.Summary,
				review.Review.CommitID, review.Review.ReviewID, attemptID,
			),
			OutputContract: workerhttp.OutputContractImplementationReadiness,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	admitted, err := starter.executions.BeginChainedTurnInState(
		ctx, lead.ID, checkpoint, attemptID, feature.StateReviewing,
		implementationReadinessRunningReason,
	)
	return request, admitted, err
}

func implementationReadinessInstructions(
	storedFeature feature.Feature,
	prepared workspace.Workspace,
	plan string,
	reviewSummary string,
	commitID string,
	reviewID int64,
	attemptID string,
) string {
	marker := workspace.ImplementationPublicationMergeReadiness.Marker(attemptID)
	return "Continue the same provider conversation as the lead. The independent reviewer has said " +
		"that the exact commit below is ready to merge. Inspect before answering: reconcile the branch, " +
		"Git HEAD, status, diff, pull-request head, accepted goal, agreed plan, and exact approved review. " +
		"Do not modify files, commit, push, change the pull-request body, or merge. If you agree that no " +
		"material blocker remains, give the merge green light. If you do not agree, state the concrete " +
		"remaining concern. For either decision, post one pull-request comment using the worker-provided " +
		"Forgejo URL and token-file environment variables. Never print, log, commit, or include the token " +
		"in a URL. The comment must be exactly the marker below, a blank line, '## Merge readiness', " +
		"another blank line, and your concise decision summary. Check existing comments for the marker " +
		"before posting so recovery never duplicates it. Return action 'ready_to_merge' with that same " +
		"summary when you agree, or action 'concern' with the same summary when you do not. If state is " +
		"contradictory, the exact revision or approval is unavailable, or you cannot safely establish " +
		"whether the comment was posted, return action 'blocked' and explain why. Durable Git, Forgejo, " +
		"and coordinator state are authoritative over conversational memory.\n\n" +
		"Current workflow phase: reviewing (merge-readiness acknowledgement)\nAccepted goal:\n" + storedFeature.AcceptedGoal +
		"\n\nAgreed implementation plan:\n" + plan +
		"\n\nReviewer's exact approval summary:\n" + reviewSummary +
		"\n\nRepository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nFeature branch: " + prepared.Branch +
		"\nPlanning baseline commit: " + prepared.BaseCommitID +
		"\nExact approved commit: " + commitID +
		fmt.Sprintf("\nFormal review: #%d\nDraft pull request: #%d (%s)", reviewID, prepared.PullRequestNumber, prepared.PullRequestURL) +
		"\nMerge-readiness audit marker:\n" + marker
}

func (starter *RemoteLeadStarter) launchImplementationReadinessVerification(
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) {
	defer starter.release(request.runID)
	if err := starter.verifyImplementationReadiness(starter.lifetime, request, result); err != nil {
		starter.requireReview(starter.lifetime, request, err)
	}
}

func (starter *RemoteLeadStarter) verifyImplementationReadiness(
	ctx context.Context,
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) error {
	round, acknowledging := implementationReadinessTurnNumber(
		request.identity.SessionID, request.identity.AttemptID,
	)
	if !acknowledging {
		return errors.New("lead merge-readiness acknowledgement has an invalid attempt identity")
	}
	if result.Disposition != workerhttp.DispositionSucceeded &&
		result.Disposition != workerhttp.DispositionChangesRequested {
		return errors.New("lead merge-readiness acknowledgement has an invalid decision")
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return err
	}
	if len(messages) == 0 || messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return errors.New("merge-readiness acknowledgement has no durable agreed plan")
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return err
	}
	reviewCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return err
	}
	reviewRound, reviewing := implementationReviewTurnNumber(reviewer.ID, reviewCheckpoint.AttemptID)
	if !reviewing || reviewRound != round {
		return errors.New("merge-readiness acknowledgement has no matching completed review")
	}
	if err := starter.confirmCompletedTurn(ctx, reviewer, reviewCheckpoint); err != nil {
		return fmt.Errorf("confirm review before merge-readiness verification: %w", err)
	}
	reviewAttempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: reviewer.ID, AttemptID: reviewCheckpoint.AttemptID,
	})
	if err != nil {
		return fmt.Errorf("load review before merge-readiness verification: %w", err)
	}
	if reviewAttempt.Result == nil || reviewAttempt.Result.Review == nil ||
		reviewAttempt.Result.Disposition != workerhttp.DispositionSucceeded {
		return errors.New("merge-readiness acknowledgement does not follow an approved review")
	}
	reviewVerified, err := starter.sessionActivityRecorded(
		ctx, reviewer.ID, implementationReviewVerificationEventID(reviewCheckpoint.AttemptID),
	)
	if err != nil {
		return fmt.Errorf("load verified review activity: %w", err)
	}
	if !reviewVerified {
		return errors.New("merge-readiness acknowledgement does not follow a verified review")
	}
	plan := messages[len(messages)-1].Event
	approved := reviewAttempt.Result.Review
	if _, err := starter.workspaces.VerifyImplementationMergeReadiness(
		ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
		request.identity.AttemptID, result.Summary, approved.CommitID,
		approved.PullRequestNumber, starter.forgejoAuthorFor(run, worker.RoleLead),
	); err != nil {
		return fmt.Errorf("verify lead merge-readiness decision: %w", err)
	}
	decision := "remaining concern"
	if result.Disposition == workerhttp.DispositionSucceeded {
		decision = "merge green light"
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, implementationReadinessVerificationEventID(request.identity.AttemptID), request.identity.SessionID,
		worker.Event{Type: worker.EventActivity, Text: fmt.Sprintf(
			"Verified lead %s for approved commit %s in Forgejo pull request #%d.",
			decision, approved.CommitID, approved.PullRequestNumber,
		)},
	); err != nil {
		return fmt.Errorf("record verified merge-readiness decision: %w", err)
	}
	if result.Disposition == workerhttp.DispositionSucceeded {
		mergeReady, err := starter.workspaces.RecordMergeReady(
			ctx, storedFeature.ProjectID, storedFeature.ID,
			approved.CommitID, approved.PullRequestNumber,
		)
		if err != nil {
			return fmt.Errorf("record exact merge target: %w", err)
		}
		if _, err := starter.planning.TransitionFeature(
			ctx, storedFeature.ID, feature.StateReadyToMerge,
			workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
			request.identity.AttemptID+":ready-to-merge",
		); err != nil {
			return fmt.Errorf("advance mutually approved implementation to ready to merge: %w", err)
		}
		if paused, err := starter.stopAtPauseBoundary(
			ctx, run.ID, implementationApprovedReason,
		); err != nil || paused {
			return err
		}
		if starter.validation != nil {
			job, _, err := starter.validation.EnsureJob(
				ctx, storedFeature.ProjectID, storedFeature.ID, run.ID, mergeReady.ID, approved.CommitID,
			)
			if err != nil {
				if errors.Is(err, validation.ErrNotFound) {
					return starter.waitRun(ctx, request.runID,
						"Configure this project's isolated validation commands before merge.",
						execution.RunWaitKindMergeGate,
					)
				}
				return fmt.Errorf("create isolated validation job: %w", err)
			}
			if _, err := starter.executions.RecordSessionEventWithID(
				ctx, request.identity.AttemptID+":validation-created", request.identity.SessionID,
				worker.Event{Type: worker.EventActivity, Text: "Isolated validation job " + job.ID + " is pending for approved commit " + approved.CommitID + "."},
			); err != nil {
				return fmt.Errorf("record validation activity: %w", err)
			}
			return starter.waitRun(ctx, request.runID,
				"The exact approved revision is waiting for isolated validation before merge.",
				execution.RunWaitKindMergeGate,
			)
		}
		if run.MergePolicy == project.MergePolicyAutoAfterGates {
			if _, _, err := starter.Merge(ctx, run.ID, run.ID+":automatic-merge"); err != nil {
				starter.recordMergeBlocked(ctx, run.ID, err)
				return starter.waitRun(ctx, request.runID, implementationMergeBlockedReason, execution.RunWaitKindBlocker)
			}
			return nil
		}
		return starter.waitRun(ctx, request.runID, implementationApprovedReason, execution.RunWaitKindMergeGate)
	}
	if implementationReviewRoundLimitReached(run.ImplementationReviewRoundLimit, round) {
		return starter.waitRun(ctx, request.runID, implementationReviewLimitReason(run.ImplementationReviewRoundLimit), execution.RunWaitKindRoundCap)
	}
	if paused, err := starter.stopAtPauseBoundary(
		ctx, run.ID, "The lead still has a concern and another independent review is pending.",
	); err != nil || paused {
		return err
	}
	next, admitted, err := starter.startImplementationReview(
		ctx, run, storedFeature, plan, result.Summary,
		workerhttp.ImplementationPublication{
			CommitID: approved.CommitID, PullRequestNumber: approved.PullRequestNumber,
		},
		round+1,
	)
	if err != nil {
		return err
	}
	if admitted {
		starter.launchAdmitted(next)
	}
	return nil
}

func implementationReviewVerificationEventID(attemptID string) string {
	return attemptID + ":review-verified"
}

func implementationReadinessVerificationEventID(attemptID string) string {
	return attemptID + ":readiness-verified"
}

func planningRoundLimitReached(limit int, messageCount int) bool {
	return dialogueRoundLimitReached(limit, messageCount/2)
}

func implementationReviewRoundLimitReached(limit int, completedRound int) bool {
	return dialogueRoundLimitReached(limit, completedRound)
}

// A zero limit deliberately means unlimited, matching the public project
// setting and the immutable copy stored on each run.
func dialogueRoundLimitReached(limit int, completedRounds int) bool {
	return limit > 0 && completedRounds >= limit
}

func planningLimitReason(limit int) string {
	return fmt.Sprintf(
		"The planning discussion completed %d dialogue rounds without a submitted plan. User input is required.",
		limit,
	)
}

func implementationReviewLimitReason(limit int) string {
	return fmt.Sprintf(
		"The implementation completed %d review rounds without mutual agreement. User input is required before another round.",
		limit,
	)
}

func (starter *RemoteLeadStarter) implementationReadinessVerificationRecorded(
	ctx context.Context,
	sessionID string,
	attemptID string,
) (bool, error) {
	recorded, err := starter.sessionActivityRecorded(
		ctx, sessionID, implementationReadinessVerificationEventID(attemptID),
	)
	if err != nil {
		return false, fmt.Errorf("load lead readiness activity: %w", err)
	}
	return recorded, nil
}

func (starter *RemoteLeadStarter) sessionActivityRecorded(
	ctx context.Context,
	sessionID string,
	eventID string,
) (bool, error) {
	events, err := starter.executions.EventsForSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.ID != eventID {
			continue
		}
		if event.Type != worker.EventActivity {
			return false, execution.ErrEventConflict
		}
		return true, nil
	}
	return false, nil
}

func nextImplementationReviewAction(disposition workerhttp.Disposition) implementationReviewAction {
	if disposition == workerhttp.DispositionChangesRequested {
		return implementationReviewActionCorrect
	}
	return implementationReviewActionAcknowledge
}

func (starter *RemoteLeadStarter) launchPlanPublication(
	runID string,
	plan execution.Event,
) {
	ctx := starter.lifetime
	if err := starter.publishSubmittedPlan(ctx, runID, plan); err != nil {
		starter.requirePlanPublicationReview(ctx, runID, plan, err)
	}
	starter.release(runID)
	request := remoteLeadRequest{
		runID: runID,
		identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: plan.SessionID,
		}},
	}
	if err := starter.advanceWaitingRun(ctx, runID); err != nil {
		starter.requireReview(ctx, request, err)
	}
}

func (starter *RemoteLeadStarter) publishSubmittedPlan(
	ctx context.Context,
	runID string,
	plan execution.Event,
) error {
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	var prepared workspace.Workspace
	if run.PlanVersion == 1 {
		prepared, _, err = starter.workspaces.PublishPlan(
			ctx, storedFeature.ProjectID, run.FeatureID, plan.ID, plan.Text,
		)
	} else {
		revision, revisionErr := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
		if revisionErr != nil {
			return revisionErr
		}
		prepared, _, err = starter.workspaces.PublishRevisedPlan(
			ctx, storedFeature.ProjectID, run.FeatureID, plan.ID, plan.Text,
			revision.BaselineCommitID,
		)
	}
	if err != nil {
		return err
	}
	_, err = starter.executions.RecordSessionEventWithID(
		ctx, planPublicationEventID(plan), plan.SessionID,
		worker.Event{
			Type: worker.EventActivity,
			Text: fmt.Sprintf(
				"The agreed implementation plan was published to Forgejo pull request #%d.",
				prepared.PullRequestNumber,
			),
		},
	)
	if err != nil {
		return err
	}
	return starter.waitRun(ctx, runID, planningPlanPublishedReason, execution.RunWaitKindPhaseCheckpoint)
}

func planPublicationEventID(plan execution.Event) string {
	return plan.ID + ":forgejo-plan-published"
}

func (starter *RemoteLeadStarter) planPublicationRecorded(
	ctx context.Context,
	plan execution.Event,
) (bool, error) {
	events, err := starter.executions.EventsForSession(ctx, plan.SessionID)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.ID != planPublicationEventID(plan) {
			continue
		}
		if event.Type != worker.EventActivity {
			return false, execution.ErrEventConflict
		}
		return true, nil
	}
	return false, nil
}

func (starter *RemoteLeadStarter) requirePlanPublicationReview(
	ctx context.Context,
	runID string,
	plan execution.Event,
	cause error,
) {
	starter.reportError(fmt.Errorf("publish agreed plan for run %q: %w", runID, cause))
	_, _ = starter.executions.RecordSessionEventWithID(
		ctx, plan.ID+":forgejo-plan-review-required", plan.SessionID,
		worker.Event{
			Type: worker.EventRecoveryAssessment, Text: planningPublicationReviewReason,
			RecoveryAssessment: &worker.RecoveryAssessment{
				Consistent: false, RequiresUserReview: true,
			},
		},
	)
	_ = starter.waitRun(ctx, runID, planningPublicationReviewReason, execution.RunWaitKindBlocker)
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
	_ = starter.waitRun(ctx, request.runID, text, execution.RunWaitKindBlocker)
}

func (starter *RemoteLeadStarter) handleInputRequired(
	ctx context.Context,
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) error {
	if result.EnvironmentRequest == nil || starter.environmentRequests == nil {
		return starter.waitRun(ctx, request.runID, result.Summary, execution.RunWaitKindBlocker)
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	_, _, err = starter.environmentRequests.Request(ctx, projectenvironment.Request{
		ID: request.identity.AttemptID + ":environment", ProjectID: storedFeature.ProjectID,
		FeatureID: storedFeature.ID, RunID: run.ID, SessionID: request.identity.SessionID,
		AttemptID:      request.identity.AttemptID,
		SystemPackages: append([]string(nil), result.EnvironmentRequest.SystemPackages...),
		Reason:         result.EnvironmentRequest.Reason,
	})
	if err != nil {
		return fmt.Errorf("record agent environment request: %w", err)
	}
	return starter.waitRun(ctx, request.runID,
		"The lead requested approved additions to the managed project environment: "+
			strings.Join(result.EnvironmentRequest.SystemPackages, ", ")+". Review the request before provisioning.",
		execution.RunWaitKindBlocker,
	)
}

func (starter *RemoteLeadStarter) waitRun(
	ctx context.Context,
	runID string,
	reason string,
	waitKinds ...execution.RunWaitKind,
) error {
	if len(waitKinds) > 1 {
		return execution.ErrInvalidStatusTransition
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil || run.Status.IsTerminal() {
		return err
	}
	var waitKind execution.RunWaitKind
	if len(waitKinds) > 0 {
		waitKind = waitKinds[0]
	}
	if waitKind == "" {
		waitKind = execution.RunWaitKindBlocker
	}
	if run.Status == execution.RunStatusWaitingForUser {
		storedKind := run.WaitKind
		if run.Paused {
			storedKind = run.PausedFromWaitKind
		}
		if run.Reason == reason && storedKind == waitKind {
			return nil
		}
	}
	_, err = starter.executions.TransitionRun(
		ctx, run.ID, run.Status, execution.RunStatusWaitingForUser, reason, waitKind,
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
