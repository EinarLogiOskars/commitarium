package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
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
	acceptanceTestsRunningReason          = "The reviewer is privately authoring independent acceptance tests from the agreed plan."
	implementationContinuationReason      = "The lead is continuing implementation after the user's guidance."
	implementationVerificationReason      = "The coordinator is rechecking the implementation revision already published by the lead."
	recoveryRecheckReason                 = "The coordinator is rechecking the blocked durable workflow state."
	implementationReadyReason             = "The lead needs user input before it can publish the implementation for review."
	implementationPublishedReason         = "The lead published its implementation commit and Forgejo audit entry. The verified revision is ready for automated review."
	implementationReviewRunningReason     = "The reviewer is inspecting the lead's exact implementation commit and preparing a formal Forgejo review."
	implementationCorrectionRunningReason = "The lead is addressing the verified review findings and publishing a corrected commit."
	implementationReadinessRunningReason  = "The lead is deciding whether it agrees with the reviewer's approval of the exact commit."
	implementationApprovedReason          = "The reviewer and lead agreed that the exact verified revision is ready for the merge gate."
	implementationMergedReason            = "Forgejo merged the exact commit approved by the reviewer and lead."
	implementationLeadOnlyReadyReason     = "The lead published and verified its implementation. This work order has no code review; the exact revision is ready for the merge gate."
	implementationLeadOnlyMergedReason    = "Forgejo merged the lead's exact verified commit. This work order had no code review."
	implementationMergeBlockedReason      = "The exact approved revision could not be merged safely. Inspect the merge activity and Forgejo pull request before retrying."
	implementationBaseUpdateRunningReason = "Main moved after this revision was approved. The lead is merging main into the feature branch, resolving conflicts, and publishing an updated commit for review."
	implementationMergeBehindMainReason   = "Main moved and the approved revision no longer merges cleanly. Continue (optionally with guidance) to have the lead bring main into the branch; the updated commit is checked again before merging."
	interventionAnsweringReason           = "The selected agent is answering the user's intervention while the workflow remains paused."
	interventionAnsweredReason            = "The selected agent answered the intervention. The workflow remains paused until the user explicitly continues it."
	replanningRunningReason               = "The lead is reconciling the preserved feature branch and proposing a revised plan."
	replanningProposalReason              = "The lead's revised planning proposal is ready for reviewer consultation."
)

type planningStage string

type implementationReviewAction string

const (
	planningStageNone             planningStage = ""
	planningStageLeadProposal     planningStage = "lead_proposal"
	planningStageFirstReview      planningStage = "first_review"
	planningStageLeadResponse     planningStage = "lead_response"
	planningStageReviewerResponse planningStage = "reviewer_response"
	planningStagePlanApproval     planningStage = "plan_approval"
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

type RecoveryAction string

const (
	RecoveryActionRecheck     RecoveryAction = "recheck"
	RecoveryActionContinue    RecoveryAction = "continue"
	RecoveryActionReplace     RecoveryAction = "replace"
	RecoveryActionLeavePaused RecoveryAction = "leave_paused"
)

type RecoveryDirective struct {
	Action  RecoveryAction
	Message string
	// FromAssistant says the assistant wrote Message (a continue_work_order
	// action), not the user; the agent's transcript shows who it was.
	FromAssistant bool
	// blockedReason is the reason of the blocker the user is answering.
	blockedReason string
	// interruption says what cut off the turn an automatic retry continues
	// (interrupted_turn.go).
	interruption string
	// previewCheck is what a failed preview check saw, which Commitarium
	// hands to the lead to fix (preview_check.go).
	previewCheck string
}

// answersBlocker reports whether the user chose to move the work on, which
// also answers an agent that stopped to ask for help. An automatic retry
// does not: only the user can answer a blocker.
func (directive RecoveryDirective) answersBlocker() bool {
	return directive.interruption == "" &&
		(directive.Action == RecoveryActionContinue || directive.Action == RecoveryActionReplace)
}

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
	ExtendPlanningRoundLimit(context.Context, string, string, int) (execution.Run, bool, error)
	QueueIntervention(context.Context, string, string, worker.Role, string) (execution.Intervention, bool, error)
	GetLatestIntervention(context.Context, string) (execution.Intervention, error)
	BeginInterventionTurn(context.Context, string, execution.WorkerAttemptCheckpoint, string, string) (bool, error)
	CompleteIntervention(context.Context, string, string, string, worker.InterventionEffect, string) (execution.Intervention, bool, error)
	ResolveInterventionGuidance(context.Context, string, string, string) (execution.Run, bool, error)
	BeginReplanningTurn(context.Context, string, string, string, int, string, string, string, execution.WorkerAttemptCheckpoint, string, string) (execution.Run, execution.PlanRevision, bool, error)
	GetPlanRevision(context.Context, string, int) (execution.PlanRevision, error)
	TransitionSession(context.Context, string, execution.SessionStatus, execution.SessionStatus, string) (execution.Session, error)
	RotateProviderSession(context.Context, string, string, string) (execution.Session, error)
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

type remoteLeadStepsExecution interface {
	CreateRunWithSteps(context.Context, string, string, int, int, project.AgentProviders, project.AgentModels, project.MergePolicy, bool, project.WorkSteps, ...project.AutonomyPolicy) (execution.Run, bool, error)
}

// ErrStepsUnsupported rejects optional-step combinations the orchestrator
// cannot run yet (ADR-018).
var ErrStepsUnsupported = errors.New("these work-order steps are not supported yet")

// supportedSteps reports whether the orchestrator can run a work order with
// these optional steps.
func supportedSteps(steps project.WorkSteps) bool {
	return steps.Validate() == nil
}

type remoteLeadAcceptanceExecution interface {
	CreateRunWithModelsAndAcceptance(context.Context, string, string, int, int, project.AgentProviders, project.AgentModels, project.MergePolicy, bool, ...project.AutonomyPolicy) (execution.Run, bool, error)
}

type remoteLeadParallelImplementationExecution interface {
	BeginParallelImplementationTurns(context.Context, string, string, execution.WorkerAttemptCheckpoint, string, string, execution.WorkerAttemptCheckpoint, string, string) (bool, error)
}

type RemoteLeadFeatureFinder interface {
	GetByID(context.Context, string) (feature.Feature, error)
}

type RemoteLeadGoalService interface {
	AcceptGoalAtStart(context.Context, string, string, string, workflow.Actor, string) (workflow.Event, error)
}

type RemoteLeadPlanningWorkflow interface {
	TransitionFeature(context.Context, string, feature.State, workflow.Actor, string) (workflow.Event, error)
}

type RemoteLeadArtifactService interface {
	GetFeatureArtifact(context.Context, string, featureartifact.Kind) (workflow.FeatureArtifact, error)
	UpsertImplementationPlan(context.Context, string, featureartifact.ImplementationPlan, workflow.Actor, string) (workflow.FeatureArtifact, error)
}

type remoteLeadAcceptanceArtifactService interface {
	UpsertAcceptanceTests(context.Context, string, featureartifact.AcceptanceTests, workflow.Actor, string) (workflow.FeatureArtifact, error)
}

type RemoteLeadWorkspaceService interface {
	Get(context.Context, string, string) (workspace.Workspace, error)
	PrepareForClarification(context.Context, string, string) (workspace.Workspace, bool, error)
	PrepareForStart(context.Context, string, string) (workspace.Workspace, string, error)
	PublishPlan(context.Context, string, string, string, string) (workspace.Workspace, bool, error)
	PublishRevisedPlan(context.Context, string, string, string, string, string) (workspace.Workspace, bool, error)
	VerifyPublishedPlan(context.Context, string, string, string, string) (workspace.Workspace, error)
	VerifyRevisedPublishedPlan(context.Context, string, string, string, string, string) (workspace.Workspace, error)
	VerifyImplementationContinuation(context.Context, string, string, string, string, string) (workspace.Workspace, error)
	PrepareReplanningBaseline(context.Context, string, string, string, string) (workspace.Workspace, string, error)
	VerifyImplementationPublication(context.Context, string, string, string, string, string, string, string, int64, string) (workspace.Workspace, error)
	VerifyRevisedImplementationPublication(context.Context, string, string, string, string, string, string, string, int64, string, string) (workspace.Workspace, error)
	VerifyImplementationReviewResponse(context.Context, string, string, string, string, string, string, string, string, int64, string, bool) (workspace.Workspace, error)
	VerifyImplementationMergeReadiness(context.Context, string, string, string, string, string, string, string, int64, string) (workspace.Workspace, error)
	VerifyImplementationReview(context.Context, string, string, string, string, string, string, string, int64, int64, string, string) (workspace.Workspace, error)
	RecordMergeReady(context.Context, string, string, string, int64) (workspace.Workspace, error)
	MergeApproved(context.Context, string, string, ...string) (workspace.Workspace, bool, error)
}

type RemoteLeadWorker interface {
	PutAttempt(context.Context, workerhttp.MutationIdentity, workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error)
	GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error)
	Supersede(context.Context, workerhttp.MutationIdentity, workerhttp.SupersedeRequest) (workerhttp.Attempt, error)
}

type RemoteLeadPump interface {
	Run(context.Context, string) (workeringest.PumpResult, error)
}

type RemoteLeadEnvironmentRequests interface {
	Request(context.Context, projectenvironment.Request) (projectenvironment.Request, bool, error)
}

type RemoteLeadValidationGate interface {
	EnsureJob(context.Context, string, string, string, string, string) (validation.Job, bool, error)
	RequirePassed(context.Context, string, string, string) error
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
	Executions          RemoteLeadExecution
	Features            RemoteLeadFeatureFinder
	Goals               RemoteLeadGoalService
	Planning            RemoteLeadPlanningWorkflow
	Artifacts           RemoteLeadArtifactService
	Briefs              RemoteLeadBriefReader
	Workspaces          RemoteLeadWorkspaceService
	Worker              RemoteLeadWorker
	Pump                RemoteLeadPump
	EnvironmentRequests RemoteLeadEnvironmentRequests
	Validation          RemoteLeadValidationGate
	// PreviewChecks, when set, checks that the app runs before a
	// preview-setup work order merges (ADR-028).
	PreviewChecks RemoteLeadPreviewChecks
	Toolchains    RemoteLeadToolchainReader
	Usage         RemoteLeadUsageRecorder
	Lifetime      context.Context
	ReportError   func(error)
}

// RemoteLeadLoginWaits records runs that wait for an agent to log in again
// (ADR-017).
type RemoteLeadLoginWaits interface {
	RecordLoginWait(ctx context.Context, runID, agentID string, role worker.Role) error
	ClearLoginWait(ctx context.Context, runID string) error
	RunsAwaitingLogin(ctx context.Context, agentID string) ([]string, error)
}

// RemoteLeadBriefReader reads a work order's handoff brief at Start.
type RemoteLeadBriefReader interface {
	GetFeatureArtifact(context.Context, string, featureartifact.Kind) (workflow.FeatureArtifact, error)
}

type RemoteLeadToolchainReader interface {
	Get(context.Context, string) (toolchain.Manifest, error)
}

// RemoteLeadUsageRecorder stores provider-reported token usage per attempt.
type RemoteLeadUsageRecorder interface {
	RecordAttemptUsage(context.Context, execution.AttemptUsage) error
}

// RemoteLeadStarter owns the deliberately small real-provider path used while
// a user and the lead agent clarify a draft goal. A deterministic session and
// attempt identity make replaying PutAttempt after a restart safe: the worker
// returns the existing attempt instead of launching a duplicate process.
type RemoteLeadStarter struct {
	executions          RemoteLeadExecution
	features            RemoteLeadFeatureFinder
	goals               RemoteLeadGoalService
	planning            RemoteLeadPlanningWorkflow
	artifacts           RemoteLeadArtifactService
	briefs              RemoteLeadBriefReader
	workspaces          RemoteLeadWorkspaceService
	worker              RemoteLeadWorker
	pump                RemoteLeadPump
	environmentRequests RemoteLeadEnvironmentRequests
	validation          RemoteLeadValidationGate
	previewChecks       RemoteLeadPreviewChecks
	toolchains          RemoteLeadToolchainReader
	usage               RemoteLeadUsageRecorder
	lifetime            context.Context
	// signInCleared and signInTry let others waiting on an agent's sign-in
	// (the assistant's conversations) follow it: the login works again, or
	// it is the moment to try their waiting work too.
	signInCleared func(agentID string)
	signInTry     func(agentID string)
	reportError   func(error)

	activeMu sync.Mutex
	active   map[string]struct{}
	mergeMu  sync.Mutex
	// acceptanceJoinMu closes the small window where both parallel workers can
	// observe the other as incomplete and then both leave without starting the
	// review. The durable turn admission still provides cross-process safety.
	acceptanceJoinMu sync.Mutex
}

func NewRemoteLeadStarter(config RemoteLeadConfig) (*RemoteLeadStarter, error) {
	if config.Executions == nil || config.Features == nil || config.Goals == nil ||
		config.Worker == nil || config.Pump == nil {
		return nil, fmt.Errorf("%w: execution service, feature store, goal service, worker client, and event pump are required", ErrInvalidRunRequest)
	}
	if config.Lifetime == nil {
		return nil, fmt.Errorf("%w: lifetime context is required", ErrInvalidRunRequest)
	}
	reportError := config.ReportError
	if reportError == nil {
		reportError = func(error) {}
	}
	return &RemoteLeadStarter{
		executions: config.Executions, features: config.Features, goals: config.Goals,
		planning: config.Planning, artifacts: config.Artifacts, briefs: config.Briefs, workspaces: config.Workspaces,
		worker: config.Worker, pump: config.Pump,
		environmentRequests: config.EnvironmentRequests, validation: config.Validation, toolchains: config.Toolchains,
		previewChecks: config.PreviewChecks,
		usage:         config.Usage, lifetime: config.Lifetime, reportError: reportError,
		active: make(map[string]struct{}),
	}, nil
}

// agentID names a session's participant: the agent and the role it plays.
func agentID(agent string, role worker.Role) string {
	return agent + "-" + string(role)
}

// profileID is the worker profile for an agent; an agent's worker serves both
// roles under one profile (ADR-016).
func (*RemoteLeadStarter) profileID(agent string, _ worker.Role) string {
	return agent
}

// forgejoAuthorFor is the Forgejo identity the run's agent uses in a role:
// "<agent>-lead" or "<agent>-reviewer" (ADR-016).
func (*RemoteLeadStarter) forgejoAuthorFor(run execution.Run, role worker.Role) string {
	agent := run.AgentProviders.LeadAgent
	if role == worker.RoleReviewer {
		agent = run.AgentProviders.ReviewerAgent
	}
	if agent == "" {
		return ""
	}
	return agentID(agent, role)
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
	if starter.workspaces == nil || starter.briefs == nil || starter.planning == nil {
		return execution.Run{}, false, fmt.Errorf("%w: workspace, brief, and planning services are required", ErrInvalidRunRequest)
	}
	if existing, err := starter.executions.GetRun(ctx, runID); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, execution.ErrNotFound) {
		return execution.Run{}, false, err
	}
	storedFeature, err := starter.features.GetByID(ctx, featureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if storedFeature.State != feature.StateReady {
		return execution.Run{}, false, ErrFeatureNotReady
	}
	if err := storedFeature.Steps.Validate(); err != nil {
		return execution.Run{}, false, err
	}
	if !supportedSteps(storedFeature.Steps) {
		return execution.Run{}, false, ErrStepsUnsupported
	}
	brief, err := starter.handoffBrief(ctx, featureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	agentProviders, err = agentProviders.Normalize()
	if err != nil {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	agentModels, err = agentModels.Normalize()
	if err != nil {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	prepared, previousBase, err := starter.workspaces.PrepareForStart(ctx, projectID, featureID)
	if err != nil {
		return execution.Run{}, false, fmt.Errorf("prepare workspace for start: %w", err)
	}
	if !prepared.CheckoutReady() {
		return execution.Run{}, false, fmt.Errorf("%w: start checkout is not ready", ErrInvalidRunRequest)
	}
	sessionID := remoteLeadSessionID(runID)
	userActor := workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}
	if _, err := starter.goals.AcceptGoalAtStart(
		ctx, featureID, sessionID, briefGoal(brief), userActor, runID+":start-goal",
	); err != nil {
		return execution.Run{}, false, err
	}
	var run execution.Run
	var created bool
	if stepsExecutions, ok := starter.executions.(remoteLeadStepsExecution); ok {
		run, created, err = stepsExecutions.CreateRunWithSteps(
			ctx, runID, featureID, dialogueLimits.PlanningRounds,
			dialogueLimits.ImplementationReviewRounds, agentProviders, agentModels,
			mergePolicy, newRunsWriteAcceptanceTests, storedFeature.Steps, autonomyPolicy,
		)
	} else if acceptanceExecutions, ok := starter.executions.(remoteLeadAcceptanceExecution); ok {
		run, created, err = acceptanceExecutions.CreateRunWithModelsAndAcceptance(
			ctx, runID, featureID, dialogueLimits.PlanningRounds,
			dialogueLimits.ImplementationReviewRounds, agentProviders, agentModels,
			mergePolicy, newRunsWriteAcceptanceTests, autonomyPolicy,
		)
	} else {
		run, created, err = starter.executions.CreateRunWithModels(
			ctx, runID, featureID, dialogueLimits.PlanningRounds,
			dialogueLimits.ImplementationReviewRounds, agentProviders, agentModels,
			mergePolicy, autonomyPolicy,
		)
	}
	if err != nil || !created {
		return run, created, err
	}
	if _, err := starter.planning.TransitionFeature(
		ctx, featureID, feature.StatePlanning,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID}, runID+":start-planning",
	); err != nil {
		starter.failAdmission(ctx, run, sessionID, fmt.Errorf("start planning: %w", err))
		return execution.Run{}, false, err
	}
	if _, _, err := starter.executions.CreateSession(
		ctx, sessionID, runID, agentID(run.AgentProviders.LeadAgent, worker.RoleLead), worker.RoleLead,
	); err != nil {
		starter.failAdmission(ctx, run, sessionID, fmt.Errorf("create lead session: %w", err))
		return execution.Run{}, false, err
	}
	storedFeature, err = starter.features.GetByID(ctx, featureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Steps.SkipPlan {
		return starter.startWithoutPlan(ctx, run, storedFeature, sessionID)
	}
	attemptID := planningAttemptID(sessionID)
	request, err := starter.startPlanningRequest(run, storedFeature, sessionID, prepared, previousBase, attemptID)
	if err != nil {
		starter.failAdmission(ctx, run, sessionID, err)
		return execution.Run{}, false, err
	}
	if _, _, err := starter.executions.CreateWorkerAttempt(ctx, sessionID, attemptID); err != nil {
		starter.failAdmission(ctx, run, sessionID, fmt.Errorf("create worker attempt: %w", err))
		return execution.Run{}, false, err
	}
	if !starter.claim(runID) {
		return execution.Run{}, false, fmt.Errorf("%w: %q", ErrRunAlreadyActive, runID)
	}
	go starter.launch(request)
	startedRun, err := starter.executions.GetRun(ctx, runID)
	return startedRun, true, err
}

// noDocumentedPlan is the plan record of a work order without a documented
// plan (ADR-018). The coordinator publishes it in place of a plan, so the pull
// request says plainly how the work was planned.
const noDocumentedPlan = "No documented plan. This work order is a quick change: the lead " +
	"implements the accepted goal directly, without a commit-by-commit plan."

// startWithoutPlan starts a work order without a documented plan. The
// coordinator records that there is no plan as the run's plan, which creates
// the feature branch and draft pull request, and the lead's first turn
// implements the accepted goal.
func (starter *RemoteLeadStarter) startWithoutPlan(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	sessionID string,
) (execution.Run, bool, error) {
	fail := func(err error) (execution.Run, bool, error) {
		starter.failAdmission(ctx, run, sessionID, err)
		return execution.Run{}, false, err
	}
	planText := noDocumentedPlan
	if storedFeature.Kind == feature.KindSession {
		planText = sessionPlanRecord
	}
	plan, err := starter.executions.RecordSessionEventWithID(
		ctx, run.ID+":no-documented-plan", sessionID,
		worker.Event{Type: worker.EventPlanSubmitted, Text: planText},
	)
	if err != nil {
		return fail(fmt.Errorf("record the work order's plan: %w", err))
	}
	if _, _, err := starter.executions.LinkPlanningMessage(ctx, run.ID, plan.ID); err != nil {
		return fail(fmt.Errorf("record the work order's plan: %w", err))
	}
	prepared, err := starter.recordPlanPublication(ctx, run, storedFeature, plan)
	if err != nil {
		return fail(fmt.Errorf("open the draft pull request: %w", err))
	}
	if _, err := starter.planning.TransitionFeature(
		ctx, storedFeature.ID, feature.StateImplementing,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID}, run.ID+":start-implementing",
	); err != nil {
		return fail(fmt.Errorf("start implementing: %w", err))
	}
	if storedFeature.Kind == feature.KindSession {
		opened, created, err := starter.openSession(ctx, run, sessionID)
		if err != nil {
			return fail(fmt.Errorf("open the session: %w", err))
		}
		return opened, created, nil
	}
	lead, err := starter.executions.GetSession(ctx, sessionID)
	if err != nil {
		return fail(err)
	}
	attemptID := implementationAttemptForVersion(sessionID, run.PlanVersion, 1)
	request, err := starter.implementationRequest(ctx, run, storedFeature, lead, prepared, plan.Text, attemptID)
	if err != nil {
		return fail(err)
	}
	if _, _, err := starter.executions.CreateWorkerAttempt(ctx, sessionID, attemptID); err != nil {
		return fail(fmt.Errorf("create worker attempt: %w", err))
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, false, fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	go starter.launch(request)
	startedRun, err := starter.executions.GetRun(ctx, run.ID)
	return startedRun, true, err
}

// ErrFeatureNotReady means a run can only start from a Ready work order.
var ErrFeatureNotReady = errors.New("only a ready work order can be started")

func (starter *RemoteLeadStarter) handoffBrief(ctx context.Context, featureID string) (featureartifact.HandoffBrief, error) {
	artifact, err := starter.briefs.GetFeatureArtifact(ctx, featureID, featureartifact.KindHandoffBrief)
	if err != nil {
		if errors.Is(err, workflow.ErrArtifactNotFound) {
			return featureartifact.HandoffBrief{}, ErrFeatureNotReady
		}
		return featureartifact.HandoffBrief{}, err
	}
	brief := featureartifact.HandoffBrief{}
	if err := json.Unmarshal([]byte(artifact.Document), &brief); err != nil {
		return featureartifact.HandoffBrief{}, fmt.Errorf("decode handoff brief: %w", err)
	}
	if err := brief.Validate(); err != nil {
		return featureartifact.HandoffBrief{}, err
	}
	return brief, nil
}

// startPlanningRequest is the lead's first turn: it checks the brief against
// the current checkout, then proposes the plan.
func (starter *RemoteLeadStarter) startPlanningRequest(
	run execution.Run,
	storedFeature feature.Feature,
	sessionID string,
	prepared workspace.Workspace,
	previousBase string,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID:         run.ID,
		agentName:     "lead agent",
		waitingReason: "The lead planning proposal is ready for reviewer consultation.",
		waitKind:      execution.RunWaitKindPhaseCheckpoint,
		planningStage: planningStageLeadProposal,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":start",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeStart,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				Model:          run.AgentModels.Lead, ProjectID: storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			Instructions: planningInstructions(storedFeature, prepared, reviewerParticipant(run.AgentProviders)) +
				briefFreshnessInstructions(previousBase, prepared.BaseCommitID),
		},
	}
	if run.Steps.SkipReviewer {
		request.request.Instructions = soloPlanningInstructions(storedFeature, prepared) +
			briefFreshnessInstructions(previousBase, prepared.BaseCommitID)
		submitPlanAlone(&request)
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func (starter *RemoteLeadStarter) Recover(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	recoveryPolicy project.RecoveryPolicy,
) error {
	return starter.recoverWithDirective(ctx, run, storedFeature, recoveryPolicy, RecoveryDirective{})
}

func (starter *RemoteLeadStarter) recoverWithDirective(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	recoveryPolicy project.RecoveryPolicy,
	directive RecoveryDirective,
) error {
	if storedFeature.State == feature.StateCompleted {
		_, _, err := starter.merge(ctx, run.ID, run.ID+":recovery-merge")
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
	if len(activeSessions) == 2 && run.IndependentAcceptanceTests && storedFeature.State == feature.StateImplementing {
		if !starter.claim(run.ID) {
			return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
		}
		requests := make([]remoteLeadRequest, 0, 2)
		for _, active := range activeSessions {
			checkpoint, checkpointErr := starter.executions.GetWorkerAttempt(ctx, active.ID)
			if checkpointErr != nil {
				starter.release(run.ID)
				return checkpointErr
			}
			request := remoteLeadRequest{runID: run.ID, allowRecoveryContinuation: recoveryPolicy == project.RecoveryPolicyAutomatic, forceFreshRecovery: directive.Action == RecoveryActionReplace, recoveryMessage: directive.Message, guidanceFromAssistant: directive.FromAssistant, answerBlocker: directive.answersBlocker(), interruption: directive.interruption, identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{SessionID: active.ID, AttemptID: checkpoint.AttemptID}}}
			switch {
			case active.Role == worker.RoleLead:
				version, _, ok := workflowAttemptVersionAndTurn(active.ID, "implementation", checkpoint.AttemptID)
				if !ok || version != run.PlanVersion {
					starter.release(run.ID)
					return fmt.Errorf("%w: parallel lead attempt is invalid", ErrInvalidRunRequest)
				}
				request.agentName = "lead agent"
				request.request.OutputContract = workerhttp.OutputContractImplementationLead
			case active.Role == worker.RoleReviewer:
				version, _, ok := workflowAttemptVersionAndTurn(active.ID, "acceptance", checkpoint.AttemptID)
				if !ok || version != run.PlanVersion {
					starter.release(run.ID)
					return fmt.Errorf("%w: parallel reviewer attempt is invalid", ErrInvalidRunRequest)
				}
				request.agentName = "reviewer"
				request.request.OutputContract = workerhttp.OutputContractAcceptanceTests
			default:
				starter.release(run.ID)
				return fmt.Errorf("%w: parallel implementation has an unexpected role", ErrInvalidRunRequest)
			}
			requests = append(requests, request)
		}
		starter.release(run.ID)
		for _, request := range requests {
			go starter.reattachAdmitted(request)
		}
		return nil
	}
	var session execution.Session
	if len(activeSessions) == 1 {
		session = activeSessions[0]
	} else if len(activeSessions) == 0 {
		if storedFeature.State == feature.StateReadyToMerge {
			return starter.recoverIdleReadyToMergeRun(ctx, run, directive)
		}
		if storedFeature.State == feature.StateImplementing {
			return starter.recoverIdleImplementationRun(ctx, run, directive)
		}
		if storedFeature.State == feature.StateReviewing {
			return starter.recoverIdleReviewingRun(ctx, run, directive)
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
	isLead := session.AgentID == agentID(run.AgentProviders.LeadAgent, worker.RoleLead) && session.Role == worker.RoleLead
	isReviewer := session.AgentID == agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) && session.Role == worker.RoleReviewer
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
		acceptanceVersion, _, authoring := workflowAttemptVersionAndTurn(
			session.ID, "acceptance", checkpoint.AttemptID,
		)
		updateVersion, _, updating := workflowAttemptVersionAndTurn(session.ID, "update", checkpoint.AttemptID)
		_, sessionTurn := sessionTurnNumber(session.ID, checkpoint.AttemptID)
		validLead := isLead && ((implementing && attemptVersion == run.PlanVersion) ||
			(updating && updateVersion == run.PlanVersion) || sessionTurn)
		validReviewer := run.IndependentAcceptanceTests && isReviewer && authoring && acceptanceVersion == run.PlanVersion
		if !validLead && !validReviewer {
			return fmt.Errorf("%w: implementing feature has an unexpected active attempt", ErrInvalidRunRequest)
		}
	}
	if !isIntervention && storedFeature.State == feature.StateReviewing {
		reviewVersion, _, reviewing := workflowAttemptVersionAndTurn(session.ID, "review", checkpoint.AttemptID)
		correctionVersion, _, correcting := workflowAttemptVersionAndTurn(session.ID, "correction", checkpoint.AttemptID)
		readinessVersion, _, acknowledging := workflowAttemptVersionAndTurn(session.ID, "readiness", checkpoint.AttemptID)
		updateVersion, _, updating := workflowAttemptVersionAndTurn(session.ID, "update", checkpoint.AttemptID)
		reviewing = reviewing && reviewVersion == run.PlanVersion
		correcting = correcting && correctionVersion == run.PlanVersion
		acknowledging = acknowledging && readinessVersion == run.PlanVersion
		updating = updating && updateVersion == run.PlanVersion
		if (!isReviewer || !reviewing) && (!isLead || (!correcting && !acknowledging && !updating)) {
			return fmt.Errorf("%w: reviewing feature has an unexpected active attempt", ErrInvalidRunRequest)
		}
	}
	if !isIntervention && storedFeature.State == feature.StatePlanning {
		attemptVersion, _, planning := planningAttemptVersionAndTurn(session.ID, checkpoint.AttemptID)
		if !planning {
			attemptVersion, _, planning = planApprovalVersionAndTurn(session.ID, checkpoint.AttemptID)
		}
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
		runID:                     run.ID,
		allowRecoveryContinuation: recoveryPolicy == project.RecoveryPolicyAutomatic,
		forceFreshRecovery:        directive.Action == RecoveryActionReplace,
		recoveryMessage:           directive.Message,
		guidanceFromAssistant:     directive.FromAssistant,
		answerBlocker:             directive.answersBlocker(),
		interruption:              directive.interruption,
		agentName:                 "lead agent",
		waitingReason:             waitingReasonForAttempt(session, checkpoint.AttemptID),
		waitKind:                  waitKindForAttempt(session, checkpoint.AttemptID),
		planningStage:             planningStageForAttempt(session, checkpoint.AttemptID),
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
	if run.Steps.SkipReviewer && isLead && !isIntervention &&
		(request.planningStage == planningStageLeadProposal || request.planningStage == planningStageLeadResponse) {
		submitPlanAlone(&request)
	}
	if isLead && !isIntervention {
		if turn, ok := sessionTurnNumber(session.ID, checkpoint.AttemptID); ok && turn == 1 {
			request.request.OutputContract = workerhttp.OutputContractSessionOpening
		}
		if _, implementing := implementationTurnNumber(session.ID, checkpoint.AttemptID); implementing {
			request.request.OutputContract = workerhttp.OutputContractImplementationLead
		}
		if _, correcting := implementationCorrectionTurnNumber(session.ID, checkpoint.AttemptID); correcting {
			request.request.OutputContract = workerhttp.OutputContractImplementationLead
		}
		if _, updating := baseUpdateTurnNumber(session.ID, checkpoint.AttemptID); updating {
			request.request.OutputContract = workerhttp.OutputContractImplementationLead
		}
		if _, acknowledging := implementationReadinessTurnNumber(session.ID, checkpoint.AttemptID); acknowledging {
			request.request.OutputContract = workerhttp.OutputContractImplementationReadiness
		}
	}
	if isReviewer && !isIntervention {
		request.agentName = "reviewer"
		if _, authoring := acceptanceTestsTurnNumber(session.ID, checkpoint.AttemptID); authoring {
			request.request.OutputContract = workerhttp.OutputContractAcceptanceTests
		}
		if _, reviewing := implementationReviewTurnNumber(session.ID, checkpoint.AttemptID); reviewing {
			request.request.OutputContract = workerhttp.OutputContractImplementationReview
		}
		if _, _, approving := planApprovalVersionAndTurn(session.ID, checkpoint.AttemptID); approving {
			request.request.OutputContract = workerhttp.OutputContractPlanApproval
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
		matchesImplementationContinuation := storedFeature.State == feature.StateImplementing &&
			implementing && implementationTurn > 1
		_, sessionTurn := sessionTurnNumber(session.ID, checkpoint.AttemptID)
		matchesSessionTurn := storedFeature.State == feature.StateImplementing && sessionTurn
		if pending[0].Type != worker.CommandMessage || (!matchesImplementationContinuation && !matchesSessionTurn) {
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

// RecoverBlocker preserves the original approve-and-continue behavior for
// existing clients. New clients can choose a narrower directive below.
func (starter *RemoteLeadStarter) RecoverBlocker(
	ctx context.Context,
	runID string,
	actionID string,
) (execution.Run, bool, error) {
	return starter.RecoverBlockerWithDirective(ctx, runID, actionID, RecoveryDirective{Action: RecoveryActionContinue})
}

func (starter *RemoteLeadStarter) RecoverBlockerWithDirective(
	ctx context.Context,
	runID string,
	actionID string,
	directive RecoveryDirective,
) (execution.Run, bool, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(actionID) == "" {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	if directive.Action == "" {
		directive.Action = RecoveryActionContinue
	}
	directive.Message = strings.TrimSpace(directive.Message)
	if directive.Action != RecoveryActionRecheck && directive.Action != RecoveryActionContinue &&
		directive.Action != RecoveryActionReplace && directive.Action != RecoveryActionLeavePaused {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	if len(directive.Message) > 16*1024 {
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
	if directive.Action == RecoveryActionLeavePaused {
		return run, false, nil
	}
	starter.noteRecovery(ctx, run.ID, actionID, directive)
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	rechecking, err := starter.executions.TransitionRun(
		ctx, run.ID, execution.RunStatusWaitingForUser, execution.RunStatusRunning,
		recoveryRecheckReason,
	)
	if err != nil {
		if errors.Is(err, execution.ErrStateConflict) {
			current, getErr := starter.executions.GetRun(ctx, run.ID)
			if getErr == nil && current.Status == execution.RunStatusRunning && !current.Paused {
				return current, false, nil
			}
			return execution.Run{}, false, errors.Join(err, getErr)
		}
		return execution.Run{}, false, err
	}

	policy := project.RecoveryPolicyAutomatic
	if directive.Action == RecoveryActionRecheck {
		policy = project.RecoveryPolicyApprovalRequired
	} else if waits, ok := starter.executions.(RemoteLeadLoginWaits); ok {
		// Continuing retries the turn; a login failure records a new wait.
		if err := waits.ClearLoginWait(ctx, run.ID); err != nil {
			starter.reportError(err)
		}
	}
	if directive.Action != RecoveryActionRecheck {
		// Likewise a limit that is still reached records a new wait.
		if waits, ok := starter.executions.(remoteLeadLimitWaits); ok {
			if err := waits.ClearLimitWait(ctx, run.ID); err != nil {
				starter.reportError(err)
			}
		}
	}
	if directive.interruption == "" {
		// The user's own continue starts automatic retries afresh.
		starter.clearRetryWait(ctx, run.ID)
	}
	directive.blockedReason = run.Reason
	err = starter.recoverWithDirective(
		context.WithoutCancel(ctx), rechecking, storedFeature, policy, directive,
	)
	if err != nil && !errors.Is(err, ErrRunAlreadyActive) {
		// A failed re-check is itself a safe outcome: retain the existing blocker
		// and its user-facing reason so another explicit request can try again.
		starter.reportError(fmt.Errorf("re-check recovery blocker for run %q: %w", run.ID, err))
		_ = starter.waitRun(
			context.WithoutCancel(ctx), run.ID, run.Reason, execution.RunWaitKindBlocker,
		)
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

// ResumeAfterLogin continues every run that paused because the agent's
// provider login expired, now that the agent is connected again. It returns
// how many runs it continued.
func (starter *RemoteLeadStarter) ResumeAfterLogin(ctx context.Context, agentID string) (int, error) {
	waits, ok := starter.executions.(RemoteLeadLoginWaits)
	if !ok {
		return 0, nil
	}
	if signIns, ok := starter.executions.(remoteLeadSignIns); ok {
		if _, err := signIns.ClearAgentSignIn(ctx, agentID); err != nil {
			return 0, err
		}
	}
	// The assistant's conversations waiting on the agent go on too.
	if starter.signInCleared != nil {
		go starter.signInCleared(agentID)
	}
	runIDs, err := waits.RunsAwaitingLogin(ctx, agentID)
	if err != nil {
		return 0, err
	}
	resumed := 0
	var failures []error
	for _, runID := range runIDs {
		admitted, err := starter.resumeLoginWait(ctx, waits, runID, "signed-in")
		if err != nil {
			failures = append(failures, err)
		}
		if admitted {
			resumed++
		}
	}
	return resumed, errors.Join(failures...)
}

// confirmReviewedMergeReady checks that a reviewed run reached the merge gate
// through the lead's verified readiness acknowledgement.
func (starter *RemoteLeadStarter) confirmReviewedMergeReady(ctx context.Context, run execution.Run) error {
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
	if checkpoint, err = starter.workflowCheckpoint(ctx, lead, checkpoint); err != nil {
		return err
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
	return nil
}

// confirmLeadOnlyMergeReady checks that a run without a reviewer (ADR-018)
// reached the merge gate through the lead's verified implementation
// publication.
func (starter *RemoteLeadStarter) confirmLeadOnlyMergeReady(ctx context.Context, run execution.Run) error {
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load lead session at the merge gate: %w", err)
	}
	if lead.Status != execution.SessionStatusWaitingForUser {
		return fmt.Errorf("%w: lead is not waiting at the merge gate", ErrInvalidRunRequest)
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return fmt.Errorf("load lead implementation checkpoint: %w", err)
	}
	if checkpoint, err = starter.workflowCheckpoint(ctx, lead, checkpoint); err != nil {
		return err
	}
	verified, err := starter.sessionActivityRecorded(ctx, lead.ID, checkpoint.AttemptID+":publication-verified")
	if err != nil {
		return err
	}
	if !verified {
		return fmt.Errorf("%w: the merge gate has no verified lead publication", ErrInvalidRunRequest)
	}
	return nil
}

func (starter *RemoteLeadStarter) recoverIdleReadyToMergeRun(
	ctx context.Context,
	run execution.Run,
	directive RecoveryDirective,
) error {
	// The lead's verified acknowledgement (or, without a reviewer, its verified
	// publication) and the feature transition are durable before the run
	// returns to its user gate. Once both exist, recovery must not depend on a
	// worker container that has no more work to perform.
	confirm := starter.confirmReviewedMergeReady
	if !run.Steps.CodeReview() {
		confirm = starter.confirmLeadOnlyMergeReady
	}
	if err := confirm(ctx, run); err != nil {
		return err
	}
	// An update onto main that was requested before a restart continues.
	if resumed, err := starter.resumeBaseUpdate(ctx, run); err != nil || resumed {
		return err
	}
	// The user's Continue on a work order blocked because main moved past it
	// has the lead bring main in, with the user's guidance. It is the user's
	// choice, so the limit on automatic updates does not apply (ADR-025).
	if directive.answersBlocker() && directive.blockedReason == implementationMergeBehindMainReason {
		storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
		if err != nil {
			return err
		}
		if _, behind := starter.behindMain(ctx, storedFeature); behind && storedFeature.Kind != feature.KindSession {
			return starter.beginBaseUpdate(ctx, run.ID, directive.Message, directive.FromAssistant, false)
		}
	}
	// A preview-setup work order merges only once the app's preview check
	// of the approved commit passed (ADR-028).
	if storedFeature, err := starter.features.GetByID(ctx, run.FeatureID); err != nil {
		return err
	} else if starter.previewCheckApplies(storedFeature) {
		prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
		if err != nil {
			return err
		}
		if handled, err := starter.previewCheckGate(
			ctx, run, storedFeature, prepared.ApprovedCommitID, false, directive,
		); err != nil || handled {
			return err
		}
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
		_, _, validationErr := starter.validation.EnsureJob(
			ctx, storedFeature.ProjectID, storedFeature.ID, run.ID, prepared.ID, prepared.ApprovedCommitID,
		)
		if errors.Is(validationErr, validation.ErrNotFound) {
			validationErr = nil
		} else if validationErr != nil {
			return fmt.Errorf("recover isolated validation job: %w", validationErr)
		} else {
			validationErr = starter.validation.RequirePassed(
				ctx, storedFeature.ProjectID, run.ID, prepared.ApprovedCommitID,
			)
		}
		if validationErr != nil {
			return starter.waitRun(ctx, run.ID,
				validationPendingReason,
				execution.RunWaitKindMergeGate,
			)
		}
	}
	if run.MergePolicy == project.MergePolicyAutoAfterGates {
		return starter.mergeAfterGates(ctx, run, false)
	}
	return starter.waitRun(ctx, run.ID, implementationApprovedReason, execution.RunWaitKindMergeGate)
}

// maxAutomaticBaseUpdates bounds how often a work order brings main in on its
// own within one plan version before it waits for the user (ADR-025).
const maxAutomaticBaseUpdates = 3

// mergeAfterGates merges a work order whose policy merges once every gate
// passed. When Forgejo refuses because main moved past the approved revision,
// the lead brings main in and the updated commit goes through the gates
// again, at most maxAutomaticBaseUpdates times per plan version; any other
// refusal blocks the run. owned says the caller already holds the run, so an
// update runs in the caller's goroutine.
func (starter *RemoteLeadStarter) mergeAfterGates(ctx context.Context, run execution.Run, owned bool) error {
	_, _, err := starter.merge(ctx, run.ID, run.ID+":automatic-merge")
	if err == nil {
		return nil
	}
	starter.recordMergeBlocked(ctx, run.ID, err)
	if !starter.mergeBlockedByMovedBase(ctx, run, err) {
		starter.noteBlocker(ctx, run.ID, execution.BlockerCheck, "merge", implementationMergeBlockedReason, implementationMergeBlockedReason)
		return starter.waitRun(ctx, run.ID, implementationMergeBlockedReason, execution.RunWaitKindBlocker)
	}
	requested, countErr := starter.baseUpdatesRequested(ctx, run)
	if countErr != nil {
		starter.reportError(fmt.Errorf("count updates onto main for run %q: %w", run.ID, countErr))
	}
	if countErr != nil || requested >= maxAutomaticBaseUpdates {
		starter.noteBlocker(ctx, run.ID, execution.BlockerCheck, "merge", implementationMergeBehindMainReason, implementationMergeBehindMainReason)
		return starter.waitRun(ctx, run.ID, implementationMergeBehindMainReason, execution.RunWaitKindBlocker)
	}
	if paused, err := starter.stopAtPauseBoundary(ctx, run.ID, implementationMergeBehindMainReason); err != nil || paused {
		return err
	}
	if err := starter.beginBaseUpdate(ctx, run.ID, "", false, owned); err != nil {
		if errors.Is(err, ErrRunAlreadyActive) {
			// Another coordinator operation owns the run and decides its state.
			return err
		}
		starter.reportError(fmt.Errorf("start update onto main for run %q: %w", run.ID, err))
		starter.noteBlocker(ctx, run.ID, execution.BlockerCheck, "merge", implementationMergeBehindMainReason, implementationMergeBehindMainReason)
		return starter.waitRun(ctx, run.ID, implementationMergeBehindMainReason, execution.RunWaitKindBlocker)
	}
	return nil
}

// MergeAfterValidation merges a work order whose isolated validation just
// passed, when its policy merges automatically. It takes the same path as
// every other automatic merge, so a work order that main moved past is
// brought up to date rather than left at the gate (ADR-025).
func (starter *RemoteLeadStarter) MergeAfterValidation(ctx context.Context, runID string) error {
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.MergePolicy != project.MergePolicyAutoAfterGates || run.Paused ||
		run.Status != execution.RunStatusWaitingForUser || run.WaitKind != execution.RunWaitKindMergeGate {
		return nil
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	if storedFeature.State != feature.StateReadyToMerge {
		return nil
	}
	return starter.mergeAfterGates(ctx, run, false)
}

// Merge performs the one final coordinator-owned external action for the
// user. When Forgejo refuses because main moved past the approved revision,
// the lead brings main in instead and the run is returned, running, without
// an error; the updated commit goes through every gate again (ADR-025).
func (starter *RemoteLeadStarter) Merge(
	ctx context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, bool, error) {
	merged, mergedNow, err := starter.merge(ctx, runID, idempotencyKey)
	if err == nil {
		return merged, mergedNow, nil
	}
	run, getErr := starter.executions.GetRun(ctx, runID)
	if getErr != nil || !starter.mergeBlockedByMovedBase(ctx, run, err) {
		return execution.Run{}, false, err
	}
	starter.recordMergeBlocked(ctx, run.ID, err)
	if updateErr := starter.beginBaseUpdate(ctx, run.ID, "", false, false); updateErr != nil {
		if errors.Is(updateErr, ErrRunAlreadyActive) {
			return execution.Run{}, false, err
		}
		// The update may already have left the merge gate, so the run waits
		// where Continue can start it again.
		starter.reportError(fmt.Errorf("start update onto main for run %q: %w", run.ID, updateErr))
		if waitErr := starter.waitRun(
			ctx, run.ID, implementationMergeBehindMainReason, execution.RunWaitKindBlocker,
		); waitErr != nil {
			return execution.Run{}, false, errors.Join(err, waitErr)
		}
	}
	current, err := starter.executions.GetRun(ctx, run.ID)
	return current, false, err
}

// merge performs the merge itself. The user's approval and the automatic
// policy both call it, so neither path can bypass the exact approved-commit
// checks in the workspace service.
func (starter *RemoteLeadStarter) merge(
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
	if starter.previewCheckApplies(storedFeature) {
		prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
		if err != nil {
			return execution.Run{}, false, err
		}
		check, found, err := starter.previewChecks.Latest(ctx, run.ID, prepared.ApprovedCommitID)
		if err != nil {
			return execution.Run{}, false, err
		}
		if !found || check.Status != previewcheck.StatusPassed {
			return execution.Run{}, false, ErrPreviewCheckRequired
		}
	}
	if starter.validation != nil {
		prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
		if err != nil {
			return execution.Run{}, false, err
		}
		if err := starter.validation.RequirePassed(
			ctx, storedFeature.ProjectID, run.ID, prepared.ApprovedCommitID,
		); err != nil {
			return execution.Run{}, false, err
		}
	}
	var approvalNote []string
	if !run.Steps.CodeReview() {
		// Branch protection still requires an approval; without code review
		// the coordinator gives it here and says why (ADR-018, ADR-031).
		approver := "the user"
		if idempotencyKey == run.ID+":automatic-merge" || idempotencyKey == run.ID+":automatic-validation-merge" {
			approver = "the work order's auto-merge policy"
		}
		approvalNote = append(approvalNote, "This work order had no code review. Commitarium approved the lead's verified commit at the merge gate, as decided by "+approver+".")
	}
	mergedWorkspace, mergedNow, err := starter.workspaces.MergeApproved(
		ctx, storedFeature.ProjectID, storedFeature.ID, approvalNote...,
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
	mergedReason := implementationMergedReason
	if !run.Steps.CodeReview() {
		mergedReason = implementationLeadOnlyMergedReason
	}
	completed, err := starter.executions.TransitionRun(
		ctx, run.ID, run.Status, execution.RunStatusSucceeded, mergedReason,
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
				AgentProfileID: starter.profileID(agentForSession(run, intervention.Target), intervention.Target),
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
	if run.Steps.SkipPlan {
		// A work order without a documented plan has no plan to revise; it
		// goes Back to Ready and starts again with a plan (ADR-018).
		return execution.Run{}, false, ErrInterventionReplanningRequired
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
		lead.Role != worker.RoleLead || lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) {
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
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
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
	if run.Steps.SkipReviewer {
		request.request.Instructions += "\n\n" + soloPlanSubmissionInstructions
		submitPlanAlone(&request)
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
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
		if run.Steps.SkipReviewer {
			_, err = starter.recoverIdlePlanningRun(ctx, run, storedFeature)
			return err
		}
		if _, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID)); errors.Is(err, execution.ErrNotFound) {
			_, _, err = starter.StartPlanningReview(ctx, run.ID, run.ID+":autonomy:first-review")
			return err
		} else if err != nil {
			return err
		}
		// The discussion continues with whoever did not speak last; a pause
		// can stop it after either agent's response.
		if len(messages) >= 2 && messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
			_, _, err = starter.StartPlanningRound(ctx, run.ID, run.ID+":autonomy:planning-loop")
			return err
		}
		_, err = starter.recoverIdlePlanningRun(ctx, run, storedFeature)
		return err
	case feature.StateImplementing:
		// Advancing on its own never answers an agent's question.
		return starter.recoverIdleImplementationRun(ctx, run, RecoveryDirective{})
	case feature.StateReviewing:
		return starter.recoverIdleReviewingRun(ctx, run, RecoveryDirective{})
	case feature.StateReadyToMerge:
		return starter.recoverIdleReadyToMergeRun(ctx, run, RecoveryDirective{})
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
	directive RecoveryDirective,
) error {
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load implementing lead session: %w", err)
	}
	if lead.Status != execution.SessionStatusWaitingForUser {
		return fmt.Errorf("%w: idle implementation has a non-waiting session", ErrInvalidRunRequest)
	}
	if !run.Steps.SkipReviewer {
		reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
		if err != nil {
			return fmt.Errorf("load implementing reviewer session: %w", err)
		}
		if reviewer.Status != execution.SessionStatusWaitingForUser {
			return fmt.Errorf("%w: idle implementation has a non-waiting session", ErrInvalidRunRequest)
		}
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return fmt.Errorf("load implementation attempt: %w", err)
	}
	if checkpoint, err = starter.workflowCheckpoint(ctx, lead, checkpoint); err != nil {
		return err
	}
	// An update onto main requested before a restart continues, rather than
	// the approved commit being checked again (ADR-025).
	if resumed, err := starter.resumeBaseUpdate(ctx, run); err != nil || resumed {
		return err
	}
	attemptVersion, _, ok := workflowAttemptVersionAndTurn(
		lead.ID, "implementation", checkpoint.AttemptID,
	)
	updateVersion, _, updating := workflowAttemptVersionAndTurn(lead.ID, "update", checkpoint.AttemptID)
	if (!ok || attemptVersion != run.PlanVersion) && (!updating || updateVersion != run.PlanVersion) {
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
	// Guidance with Continue goes to a lead whose finished turn could not be
	// accepted; Continue alone checks that turn again.
	return starter.continueOrVerifyLeadTurn(ctx, run, lead, checkpoint, attempt, directive)
}

func (starter *RemoteLeadStarter) recoverIdleReviewingRun(
	ctx context.Context,
	run execution.Run,
	directive RecoveryDirective,
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
	if reviewerCheckpoint, err = starter.workflowCheckpoint(ctx, reviewer, reviewerCheckpoint); err != nil {
		return err
	}
	if leadCheckpoint, err = starter.workflowCheckpoint(ctx, lead, leadCheckpoint); err != nil {
		return err
	}
	// An update onto main requested before a restart continues, rather than
	// the approved commit being checked again (ADR-025).
	if resumed, err := starter.resumeBaseUpdate(ctx, run); err != nil || resumed {
		return err
	}
	if updateVersion, _, updating := workflowAttemptVersionAndTurn(
		lead.ID, "update", leadCheckpoint.AttemptID,
	); updating && updateVersion == run.PlanVersion {
		handled, err := starter.recoverIdleBaseUpdate(
			ctx, run, lead, leadCheckpoint, reviewer, reviewerCheckpoint, directive,
		)
		if err != nil || handled {
			return err
		}
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
		// Guidance with Continue goes to a reviewer whose finished review
		// could not be accepted; Re-check checks that review again.
		return starter.continueOrVerifyReviewTurn(ctx, run, reviewer, reviewerCheckpoint, attempt, directive)
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
			if !directive.answersBlocker() {
				return starter.handleInputRequired(ctx, remoteLeadRequest{runID: run.ID, identity: workerhttp.MutationIdentity{AttemptReference: attempt.AttemptReference}}, *attempt.Result)
			}
			// The user's Continue answers a lead that asked for help while
			// answering a review: it tries the same round again.
			contract := workerhttp.OutputContractImplementationLead
			if acknowledging {
				contract = workerhttp.OutputContractImplementationReadiness
			}
			return starter.answerLeadBlocker(ctx, run, lead, leadCheckpoint, attempt, directive, false, contract)
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

// continueOrVerifyReviewTurn handles the reviewer's finished review, as
// continueOrVerifyLeadTurn does the lead's turn. The user's Continue answers a
// reviewer that asked for help. A review the coordinator could not accept is
// tried again in the same round and conversation when the user gave guidance
// or chose a fresh conversation, or when its acceptance results stop it:
// checking that review again can never pass, so plain Continue tells the
// reviewer what to put right. Otherwise, and always for Re-check, the review
// is checked again.
func (starter *RemoteLeadStarter) continueOrVerifyReviewTurn(
	ctx context.Context,
	run execution.Run,
	reviewer execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	attempt workerhttp.Attempt,
	directive RecoveryDirective,
) error {
	inputRequired := attempt.Result.Disposition == workerhttp.DispositionInputRequired
	if inputRequired && !directive.answersBlocker() {
		return starter.handleInputRequired(ctx, remoteLeadRequest{runID: run.ID, identity: workerhttp.MutationIdentity{AttemptReference: attempt.AttemptReference}}, *attempt.Result)
	}
	guided, refusal := false, ""
	if !inputRequired && directive.answersBlocker() {
		// A verified review was accepted: the blocker came later, and
		// checking again continues from it.
		verified, err := starter.sessionActivityRecorded(
			ctx, reviewer.ID, implementationReviewVerificationEventID(checkpoint.AttemptID),
		)
		if err != nil {
			return fmt.Errorf("load review verification: %w", err)
		}
		if !verified {
			refused, err := starter.acceptanceReviewRefusal(ctx, run, *attempt.Result)
			if err != nil {
				return err
			}
			var incomplete acceptanceResultsIncomplete
			switch {
			case errors.As(refused, &incomplete):
				refusal = strings.TrimSuffix(refused.Error(), ".") + ". " + acceptanceResultsGuidance
			case refused != nil:
				refusal = refused.Error()
			default:
				// The blocker recorded what failed (see requireReview).
				_, refusal, _ = strings.Cut(directive.blockedReason, "What failed: ")
			}
			guided = directive.Message != "" || directive.Action == RecoveryActionReplace ||
				errors.As(refused, &incomplete)
		}
	}
	if inputRequired || guided {
		if !starter.claim(run.ID) {
			return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
		}
		answer := remoteLeadRequest{
			runID: run.ID, agentName: "reviewer", allowRecoveryContinuation: true,
			answerBlocker: true, guided: guided, forceFreshRecovery: directive.Action == RecoveryActionReplace,
			recoveryMessage: directive.Message, guidanceFromAssistant: directive.FromAssistant, refusal: refusal,
			identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
				SessionID: reviewer.ID, AttemptID: checkpoint.AttemptID,
			}},
			request: workerhttp.PutAttemptRequest{OutputContract: workerhttp.OutputContractImplementationReview},
		}
		go starter.launchBlockerAnswer(answer, attempt)
		return nil
	}
	request := remoteLeadRequest{runID: run.ID, agentName: "reviewer", identity: workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: checkpoint.AttemptID},
	}}
	if !starter.claim(run.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	go starter.launchImplementationReviewVerification(request, *attempt.Result)
	return nil
}

// acceptanceResultsGuidance tells a reviewer whose review was refused for its
// acceptance results how to put them right.
const acceptanceResultsGuidance = "Run the affected acceptance tests again against the same exact commit and " +
	"record each new result: `commitarium-artifact acceptance start <test-id>`, then `acceptance pass <test-id>` " +
	"or `acceptance fail <test-id> <note>`; a new result replaces a failure. Then post a new review of that " +
	"commit with the new marker: approve only when no acceptance test is recorded as failed, and request " +
	"changes if the implementation is wrong."

// recoverIdlePlanningRun handles the safe points between provider turns. The
// run intentionally remains active across the lead-to-reviewer handoff, so a
// restart can distinguish unfinished automatic routing from a normal user gate.
func (starter *RemoteLeadStarter) recoverIdlePlanningRun(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
) (bool, error) {
	if run.Steps.SkipReviewer {
		return true, starter.recoverIdleSoloPlanningRun(ctx, run, storedFeature)
	}
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
		// The reviewer may still need to approve the plan, which is a whole
		// provider turn, so continue outside recovery.
		go starter.launchPlanPublication(run.ID, last.Event)
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
		request, admitted, err = starter.startReviewerResponse(ctx, run, storedFeature, messages, true)
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

const (
	soloPlanningRunningReason = "The lead is submitting its plan."
	soloPlanSubmittedReason   = "The lead has submitted its plan."
)

// submitPlanAlone makes a lead planning turn submit the plan, for work orders
// without a reviewer (ADR-018): there is no one to discuss it with.
func submitPlanAlone(request *remoteLeadRequest) {
	request.planningStage = planningStageLeadResponse
	request.request.OutputContract = workerhttp.OutputContractPlanningLead
	request.waitingReason = soloPlanSubmittedReason
	request.waitKind = execution.RunWaitKindPhaseCheckpoint
}

// continueSoloPlanning is the planning control for a work order without a
// reviewer: there is no review to start, so it continues the lead's
// submission instead.
func (starter *RemoteLeadStarter) continueSoloPlanning(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
) (execution.Run, bool, error) {
	if run.Status == execution.RunStatusRunning {
		return run, false, nil
	}
	if run.Status != execution.RunStatusWaitingForUser {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if err := starter.recoverIdleSoloPlanningRun(ctx, run, storedFeature); err != nil {
		return execution.Run{}, false, err
	}
	updated, err := starter.executions.GetRun(ctx, run.ID)
	return updated, true, err
}

// recoverIdleSoloPlanningRun continues planning without a reviewer: a
// submitted plan is published, and a lead that replied without submitting is
// asked again within the planning round limit.
func (starter *RemoteLeadStarter) recoverIdleSoloPlanningRun(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
) error {
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return fmt.Errorf("load lead session: %w", err)
	}
	if lead.Status != execution.SessionStatusWaitingForUser {
		return fmt.Errorf("%w: idle planning run has a non-waiting session", ErrInvalidRunRequest)
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return fmt.Errorf("%w: idle planning run has no planning message", ErrInvalidRunRequest)
	}
	last := messages[len(messages)-1]
	if last.Event.Type == worker.EventPlanSubmitted {
		if err := starter.ensureSubmittedPlanArtifact(ctx, run, last.Event); err != nil {
			return err
		}
		if !starter.claim(run.ID) {
			return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
		}
		go starter.launchPlanPublication(run.ID, last.Event)
		return nil
	}
	if planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) {
		return starter.waitRun(ctx, run.ID, planningLimitReason(run.PlanningRoundLimit), execution.RunWaitKindRoundCap)
	}
	if !starter.claim(run.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	request, admitted, err := starter.startLeadResponse(ctx, run, storedFeature, messages, true)
	if err != nil || !admitted {
		starter.release(run.ID)
		return err
	}
	go starter.launch(request)
	return nil
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
	runID                     string
	commandID                 string
	interventionID            string
	agentName                 string
	waitingReason             string
	waitKind                  execution.RunWaitKind
	planningStage             planningStage
	identity                  workerhttp.MutationIdentity
	request                   workerhttp.PutAttemptRequest
	allowRecoveryContinuation bool
	forceFreshRecovery        bool
	recoveryMessage           string
	// guidanceFromAssistant says the assistant, not the user, wrote the
	// guidance in recoveryMessage.
	guidanceFromAssistant bool
	// modelSwitch resumes a turn the user stopped to switch models.
	modelSwitch bool
	// answerBlocker lets the user's Continue start the next turn of an agent
	// that stopped to ask for help, in the same conversation. Automatic
	// recovery never does this: only the user can answer a blocker.
	answerBlocker bool
	// guided lets the user's Continue with guidance start the next turn of
	// an agent whose finished turn the coordinator could not accept, such
	// as a publication that failed verification. Without guidance, Continue
	// only checks that turn again.
	guided bool
	// refusal says why the coordinator could not accept a guided agent's
	// finished turn, so its next turn knows what to fix.
	refusal string
	// interruption says what cut off the turn an automatic retry continues.
	interruption string
	// previewCheck is the evidence of a failed preview check the lead's next
	// turn fixes.
	previewCheck string
}

func interventionAttemptID(sessionID, interventionID string) string {
	digest := sha256.Sum256([]byte(interventionID))
	return sessionID + ":intervention:" + hex.EncodeToString(digest[:16])
}

func isInterventionAttempt(sessionID, attemptID string) bool {
	return strings.HasPrefix(attemptID, sessionID+":intervention:")
}

// workflowCheckpoint looks past an answered intervention. The user's message
// becomes the conversation's latest attempt, but the workflow still rests on
// the turn before it, so recovery checks and continues that turn. Any other
// checkpoint is returned as it is.
func (starter *RemoteLeadStarter) workflowCheckpoint(
	ctx context.Context,
	session execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
) (execution.WorkerAttemptCheckpoint, error) {
	if !isInterventionAttempt(session.ID, checkpoint.AttemptID) {
		return checkpoint, nil
	}
	events, err := starter.executions.EventsForSession(ctx, session.ID)
	if err != nil {
		return execution.WorkerAttemptCheckpoint{}, fmt.Errorf("load the turn before an intervention: %w", err)
	}
	prior := execution.WorkerAttemptCheckpoint{SessionID: session.ID}
	for _, event := range events {
		if event.WorkerAttemptID != "" && !isInterventionAttempt(session.ID, event.WorkerAttemptID) {
			prior.AttemptID = event.WorkerAttemptID
		}
	}
	if prior.AttemptID == "" {
		return checkpoint, nil
	}
	for _, event := range events {
		if event.WorkerAttemptID == prior.AttemptID {
			prior.LastEventSequence = max(prior.LastEventSequence, event.WorkerEventSequence)
		}
	}
	return prior, nil
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

func acceptanceTestsAttemptForVersion(sessionID string, version int) string {
	return workflowAttemptForVersion(sessionID, "acceptance", version, 1)
}

func acceptanceTestsTurnNumber(sessionID, attemptID string) (int, bool) {
	_, turn, found := workflowAttemptVersionAndTurn(sessionID, "acceptance", attemptID)
	return turn, found
}

func implementationCorrectionAttemptForVersion(sessionID string, version int, turn int) string {
	return workflowAttemptForVersion(sessionID, "correction", version, turn)
}

func implementationReadinessAttemptForVersion(sessionID string, version int, turn int) string {
	return workflowAttemptForVersion(sessionID, "readiness", version, turn)
}

// baseUpdateAttemptForVersion names the lead's turn that brings the moved
// default branch into an approved feature branch (ADR-025).
func baseUpdateAttemptForVersion(sessionID string, version int, turn int) string {
	return workflowAttemptForVersion(sessionID, "update", version, turn)
}

func baseUpdateTurnNumber(sessionID, attemptID string) (int, bool) {
	_, turn, found := workflowAttemptVersionAndTurn(sessionID, "update", attemptID)
	return turn, found
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
	version, turn, _, ok := workflowAttemptCoordinates(sessionID, kind, attemptID)
	return version, turn, ok
}

// workflowAttemptCoordinates also reports which try of its turn an attempt
// is. Only the round kinds are tried again within their round (see
// roundRetryAttemptID); every other attempt is its turn's first try.
func workflowAttemptCoordinates(sessionID string, kind string, attemptID string) (int, int, int, bool) {
	value, found := strings.CutPrefix(attemptID, sessionID+":"+kind+":")
	if !found {
		return 0, 0, 0, false
	}
	parts := strings.Split(value, ":")
	version := 1
	if strings.HasPrefix(parts[0], "v") {
		parsed, err := strconv.Atoi(strings.TrimPrefix(parts[0], "v"))
		if err != nil || parsed < 2 {
			return 0, 0, 0, false
		}
		version, parts = parsed, parts[1:]
	}
	try := 1
	if slices.Contains(roundKinds, kind) && len(parts) == 2 {
		parsed, err := strconv.Atoi(parts[1])
		if err != nil || parsed < 2 {
			return 0, 0, 0, false
		}
		try, parts = parsed, parts[:1]
	}
	if len(parts) != 1 {
		return 0, 0, 0, false
	}
	turn, err := strconv.Atoi(parts[0])
	if err != nil || turn < 1 {
		return 0, 0, 0, false
	}
	return version, turn, try, true
}

// roundKinds are the turns numbered by review round. Review N answers lead
// response N-1 and is answered by correction or readiness N.
var roundKinds = []string{"review", "correction", "readiness"}

// roundRetryAttemptID names another try at the same round, so a turn that is
// tried again keeps its round rather than taking the next.
func roundRetryAttemptID(sessionID, kind string, version, round, try int) string {
	return workflowAttemptForVersion(sessionID, kind, version, round) + ":" + strconv.Itoa(try)
}

func reviewRetryAttemptID(sessionID string, version, round, try int) string {
	return roundRetryAttemptID(sessionID, "review", version, round, try)
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
	if version, turn, _, ok := planningRecoveryVersionAndTurn(sessionID, attemptID); ok {
		return version, turn, true
	}
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

// isPlanningTurn reports whether attemptID is the given planning turn of a
// plan version, either as first started or as one of its recovery successors
// (ADR-032), which keep the turn they replace.
func isPlanningTurn(sessionID, attemptID string, version, turn int) bool {
	attemptVersion, attemptTurn, ok := planningAttemptVersionAndTurn(sessionID, attemptID)
	return ok && attemptVersion == version && attemptTurn == turn
}

func planningStageForAttempt(session execution.Session, attemptID string) planningStage {
	if _, _, approving := planApprovalVersionAndTurn(session.ID, attemptID); approving && session.Role == worker.RoleReviewer {
		return planningStagePlanApproval
	}
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
	if _, _, approving := planApprovalVersionAndTurn(session.ID, attemptID); approving {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, planned := planningTurnNumber(session.ID, attemptID); planned {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, implementing := implementationTurnNumber(session.ID, attemptID); implementing {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, correcting := implementationCorrectionTurnNumber(session.ID, attemptID); correcting {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, updating := baseUpdateTurnNumber(session.ID, attemptID); updating {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, acknowledging := implementationReadinessTurnNumber(session.ID, attemptID); acknowledging {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, reviewing := implementationReviewTurnNumber(session.ID, attemptID); reviewing {
		return execution.RunWaitKindPhaseCheckpoint
	}
	if _, authoring := acceptanceTestsTurnNumber(session.ID, attemptID); authoring {
		return execution.RunWaitKindPhaseCheckpoint
	}
	return execution.RunWaitKindClarification
}

func waitingReasonForAttempt(session execution.Session, attemptID string) string {
	if _, ok := sessionTurnNumber(session.ID, attemptID); ok {
		return sessionYourTurnReason
	}
	if _, _, approving := planApprovalVersionAndTurn(session.ID, attemptID); approving && session.Role == worker.RoleReviewer {
		return planApprovalReason
	}
	if _, implementing := implementationTurnNumber(session.ID, attemptID); session.Role == worker.RoleLead && implementing {
		return implementationReadyReason
	}
	if _, correcting := implementationCorrectionTurnNumber(session.ID, attemptID); session.Role == worker.RoleLead && correcting {
		return implementationCorrectionRunningReason
	}
	if _, updating := baseUpdateTurnNumber(session.ID, attemptID); session.Role == worker.RoleLead && updating {
		return implementationBaseUpdateRunningReason
	}
	if _, acknowledging := implementationReadinessTurnNumber(session.ID, attemptID); session.Role == worker.RoleLead && acknowledging {
		return implementationReadinessRunningReason
	}
	if _, reviewing := implementationReviewTurnNumber(session.ID, attemptID); session.Role == worker.RoleReviewer && reviewing {
		return implementationReviewRunningReason
	}
	if _, authoring := acceptanceTestsTurnNumber(session.ID, attemptID); session.Role == worker.RoleReviewer && authoring {
		return acceptanceTestsRunningReason
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
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: session.ProviderSessionID,
			Instructions:      planningInstructions(storedFeature, prepared, reviewerParticipant(run.AgentProviders)),
		},
	}
	if run.Steps.SkipReviewer {
		request.request.Instructions = soloPlanningInstructions(storedFeature, prepared)
		submitPlanAlone(&request)
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
	if run.Steps.SkipReviewer {
		return starter.continueSoloPlanning(ctx, run, storedFeature)
	}
	if run.PlanVersion > 1 {
		return starter.startReplanningReview(ctx, run, storedFeature)
	}

	reviewerID := remoteReviewerSessionID(run.ID)
	if existing, getErr := starter.executions.GetSession(ctx, reviewerID); getErr == nil {
		if existing.RunID != run.ID || existing.AgentID != agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) ||
			existing.Role != worker.RoleReviewer {
			return execution.Run{}, false, execution.ErrRecordConflict
		}
		checkpoint, checkpointErr := starter.executions.GetWorkerAttempt(ctx, existing.ID)
		if checkpointErr != nil || !isPlanningTurn(existing.ID, checkpoint.AttemptID, 1, 1) {
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
	if lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) || lead.Role != worker.RoleLead ||
		lead.Status != execution.SessionStatusWaitingForUser ||
		!isPlanningTurn(lead.ID, leadCheckpoint.AttemptID, 1, 1) {
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
		ctx, reviewerID, run.ID, agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer), worker.RoleReviewer,
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
	if lead.Status != execution.SessionStatusWaitingForUser ||
		!isPlanningTurn(lead.ID, leadCheckpoint.AttemptID, run.PlanVersion, 1) ||
		lead.ProviderSessionID == "" ||
		lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) ||
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
	if isPlanningTurn(reviewer.ID, reviewerCheckpoint.AttemptID, run.PlanVersion, 1) {
		return run, false, nil
	}
	if len(messages) != 1 {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if run.Status != execution.RunStatusWaitingForUser ||
		reviewer.Status != execution.SessionStatusWaitingForUser ||
		reviewer.ProviderSessionID == "" ||
		reviewer.AgentID != agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) ||
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
				AgentProfileID: starter.profileID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer),
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
				AgentProfileID: starter.profileID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			Instructions: reviewerPlanningInstructions(storedFeature, prepared, proposal, leadParticipant(run.AgentProviders)),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
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
	if run.Steps.SkipReviewer {
		return starter.continueSoloPlanning(ctx, run, storedFeature)
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
		lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) || lead.Role != worker.RoleLead ||
		reviewer.AgentID != agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return execution.Run{}, false, err
	}
	claimed := false
	if len(messages) > 0 && planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) &&
		messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		if run.WaitKind != execution.RunWaitKindRoundCap {
			if err := starter.waitRun(
				ctx, run.ID, planningLimitReason(run.PlanningRoundLimit), execution.RunWaitKindRoundCap,
			); err != nil {
				return execution.Run{}, false, err
			}
			updated, err := starter.executions.GetRun(ctx, run.ID)
			return updated, false, err
		}
		if !starter.claim(run.ID) {
			return execution.Run{}, false, ErrPlanningNotAllowed
		}
		claimed = true
		extended, applied, err := starter.executions.ExtendPlanningRoundLimit(
			ctx,
			planningRoundExtensionID(run.ID, idempotencyKey),
			run.ID,
			run.PlanningRoundLimit,
		)
		if err != nil {
			starter.release(run.ID)
			return execution.Run{}, false, err
		}
		if !applied {
			starter.release(run.ID)
			return extended, false, nil
		}
		run = extended
	}
	if len(messages) > 0 && messages[len(messages)-1].Event.Type == worker.EventPlanSubmitted {
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
	// The planning discussion continues with whoever did not speak last: the
	// lead answers the reviewer, and the reviewer answers the lead's response
	// (a pause between the two leaves the run waiting on the reviewer's turn).
	if len(messages) < 2 || (messages[len(messages)-1].Role != worker.RoleReviewer &&
		messages[len(messages)-1].Role != worker.RoleLead) {
		if claimed {
			starter.release(run.ID)
		}
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	if !claimed && !starter.claim(run.ID) {
		return execution.Run{}, false, ErrPlanningNotAllowed
	}
	var request remoteLeadRequest
	var admitted bool
	if messages[len(messages)-1].Role == worker.RoleLead {
		request, admitted, err = starter.startReviewerResponse(ctx, run, storedFeature, messages, false)
	} else {
		request, admitted, err = starter.startLeadResponse(ctx, run, storedFeature, messages, false)
	}
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

func planningRoundExtensionID(runID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + idempotencyKey))
	return "planning_round_" + hex.EncodeToString(digest[:16])
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
	var reviewer execution.Session
	if !run.Steps.SkipReviewer {
		reviewer, err = starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
		if err != nil {
			return execution.Run{}, false, err
		}
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
	reviewerReady := run.Steps.SkipReviewer ||
		reviewer.Status == execution.SessionStatusWaitingForUser &&
			reviewer.AgentID == agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) &&
			reviewer.Role == worker.RoleReviewer && reviewer.ProviderSessionID != ""
	if run.Status != execution.RunStatusWaitingForUser ||
		lead.Status != execution.SessionStatusWaitingForUser ||
		lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) || lead.Role != worker.RoleLead ||
		lead.ProviderSessionID == "" || !reviewerReady {
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
		ctx, run, currentFeature, lead, prepared, plan.Text, attemptID,
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
	if run.IndependentAcceptanceTests {
		reviewerCheckpoint, checkpointErr := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
		if checkpointErr != nil {
			starter.release(run.ID)
			return execution.Run{}, false, checkpointErr
		}
		if confirmErr := starter.confirmCompletedTurn(ctx, reviewer, reviewerCheckpoint); confirmErr != nil {
			starter.release(run.ID)
			return execution.Run{}, false, fmt.Errorf("%w: the reviewer's planning turn is not confirmed complete: %v", ErrImplementationNotAllowed, confirmErr)
		}
		acceptanceAttemptID := acceptanceTestsAttemptForVersion(reviewer.ID, run.PlanVersion)
		acceptanceRequest, requestErr := starter.acceptanceTestsRequest(
			run, currentFeature, reviewer, prepared, plan.Text, acceptanceAttemptID,
		)
		if requestErr != nil {
			starter.release(run.ID)
			return execution.Run{}, false, requestErr
		}
		parallel, ok := starter.executions.(remoteLeadParallelImplementationExecution)
		if !ok {
			starter.release(run.ID)
			return execution.Run{}, false, errors.New("parallel implementation admission is unavailable")
		}
		admitted, admissionErr := parallel.BeginParallelImplementationTurns(
			ctx, run.ID, lead.ID, checkpoint, attemptID,
			reviewer.ID, reviewerCheckpoint, acceptanceAttemptID,
			"The lead is implementing while the reviewer privately authors independent acceptance tests.",
		)
		if admissionErr != nil {
			starter.release(run.ID)
			return execution.Run{}, false, admissionErr
		}
		if !admitted {
			starter.release(run.ID)
			storedRun, getErr := starter.executions.GetRun(ctx, run.ID)
			return storedRun, false, getErr
		}
		// Durable state, not the in-memory claim, prevents duplicate admission.
		// Both independent worker observations may now progress concurrently.
		starter.release(run.ID)
		go starter.launchAdmitted(request)
		go starter.launchAdmitted(acceptanceRequest)
		startedRun, getErr := starter.executions.GetRun(ctx, run.ID)
		return startedRun, true, getErr
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

func (starter *RemoteLeadStarter) acceptanceTestsRequest(
	run execution.Run,
	storedFeature feature.Feature,
	reviewer execution.Session,
	prepared workspace.Workspace,
	plan string,
	attemptID string,
) (remoteLeadRequest, error) {
	request := remoteLeadRequest{
		runID: run.ID, agentName: "reviewer", waitingReason: acceptanceTestsRunningReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer),
				Model:          run.AgentModels.Reviewer,
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: reviewer.ProviderSessionID,
			Instructions:      acceptanceTestsInstructions(prepared),
			OutputContract:    workerhttp.OutputContractAcceptanceTests,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
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
		(!run.Steps.SkipReviewer && reviewer.Status != execution.SessionStatusWaitingForUser) {
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
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	lead execution.Session,
	prepared workspace.Workspace,
	plan string,
	attemptID string,
) (remoteLeadRequest, error) {
	previewContext, err := starter.previewRunInstructions(ctx, storedFeature.ProjectID)
	if err != nil {
		return remoteLeadRequest{}, err
	}
	mode, idempotencySuffix := workerhttp.AttemptModeResume, ":resume"
	if lead.ProviderSessionID == "" {
		// Without a documented plan, implementation is the lead's first turn.
		mode, idempotencySuffix = workerhttp.AttemptModeStart, ":start"
	}
	instructions := implementationInstructions(prepared, attemptID)
	if run.Steps.SkipPlan {
		instructions = planlessImplementationInstructions(storedFeature, prepared, attemptID)
	}
	if starter.previewCheckApplies(storedFeature) {
		instructions += previewCheckInstructions
	}
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent", waitingReason: implementationReadyReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{
				SessionID: lead.ID, AttemptID: attemptID,
			},
			IdempotencyKey: attemptID + idempotencySuffix,
		},
		request: workerhttp.PutAttemptRequest{
			Mode: mode,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions:      instructions + previewContext,
			OutputContract:    workerhttp.OutputContractImplementationLead,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
}

func (starter *RemoteLeadStarter) previewRunInstructions(ctx context.Context, projectID string) (string, error) {
	const conventions = "Preview compatibility requirement: keep one Docker Compose file at the repository root under a standard Compose filename. Services may use image, build (context, dockerfile, args, and target only), command, entrypoint, environment, working_dir, user, ports, expose, volumes, depends_on, healthcheck, restart, init, tty, stdin_open, labels, networks, and x-* extension fields. Every environment variable must have an explicit value; do not use key-only entries that inherit from the Compose process. Build contexts, Dockerfiles, and bind mounts must resolve inside the repository; use named volumes for persistent data. Do not set explicit volume or network names, resource labels, driver_opts, external resources, IPAM, or attachable networks; service network options may contain aliases only. A service with build must not also set image. Do not use Compose interpolation, env_file, label_file, include, extends, privileged mode, added capabilities, devices, host namespace modes, security_opt, volumes_from, secrets, configs, or build secrets, SSH, or network overrides. Extension fields are accepted only at the document and service levels. Every HTTP service must bind 0.0.0.0, and a frontend must reach its API through its Compose service name or a relative-path development proxy rather than a hardcoded localhost port. Commitarium runs the preview over plain http://127.0.0.1 in a WebKit window that frames the app, where cookies marked Secure (so every SameSite=None cookie, and __Host- or __Secure- names) are dropped, and it sets COMMITARIUM_PREVIEW=1 in every preview container. Keep the app's production cookie settings, and when COMMITARIUM_PREVIEW=1 use sign-in and session cookies that work over plain http (SameSite=Lax, not Secure), preferring the framework's own switch (for example NextAuth follows NEXTAUTH_URL's scheme, express-session takes secure: 'auto'); never require https in the preview. Reviewers must flag changes that violate these rules or break the configured open target."
	if starter.toolchains == nil {
		return "", nil
	}
	manifest, err := starter.toolchains.Get(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("load project preview run configuration: %w", err)
	}
	if manifest.Run == nil {
		return "\n\n" + conventions + " This project currently has no configured preview open target.", nil
	}
	encoded, err := json.Marshal(manifest.Run)
	if err != nil {
		return "", fmt.Errorf("encode project preview run configuration: %w", err)
	}
	return "\n\n" + conventions + "\nConfigured preview open target JSON:\n" + string(encoded), nil
}

func implementationPublicationMarker(attemptID string) string {
	return workspace.ImplementationPublicationInitial.Marker(attemptID)
}

func (starter *RemoteLeadStarter) startLeadResponse(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	messages []execution.PlanningMessage,
	chained bool,
) (remoteLeadRequest, bool, error) {
	solo := run.Steps.SkipReviewer
	if len(messages) == 0 || planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) ||
		(!solo && (len(messages) < 2 || messages[len(messages)-1].Role != worker.RoleReviewer)) ||
		(solo && messages[len(messages)-1].Event.Type == worker.EventPlanSubmitted) {
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
		lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) || lead.Role != worker.RoleLead ||
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
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions:      leadPlanningResponseInstructions(messages[len(messages)-1].Event.Text, reviewerParticipant(run.AgentProviders)),
			OutputContract:    workerhttp.OutputContractPlanningLead,
		},
	}
	chainedReason := "The lead is considering the reviewer's response."
	if solo {
		request.request.Instructions = soloPlanSubmissionInstructions
		submitPlanAlone(&request)
		chainedReason = soloPlanningRunningReason
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	if chained {
		admitted, err := starter.executions.BeginChainedTurn(
			ctx, lead.ID, checkpoint, attemptID,
			chainedReason,
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

func (starter *RemoteLeadStarter) startReviewerResponse(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	messages []execution.PlanningMessage,
	chained bool,
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
		reviewer.AgentID != agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer ||
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
	const evaluatingReason = "The reviewer is evaluating the lead's latest planning response."
	if chained {
		admitted, err := starter.executions.BeginChainedTurn(
			ctx, reviewer.ID, checkpoint, nextAttemptID, evaluatingReason,
		)
		return request, admitted, err
	}
	// A user's Continue planning starts the turn from a waiting run.
	admitted, err := starter.executions.BeginAutonomousTurn(
		ctx, reviewer.ID, checkpoint, nextAttemptID, feature.StatePlanning, evaluatingReason,
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
				AgentProfileID: starter.profileID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: reviewer.ProviderSessionID,
			Instructions:      reviewerResponseInstructions(leadResponse, leadParticipant(run.AgentProviders)),
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	return request, nil
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
	if session.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) || session.Role != worker.RoleLead ||
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
	// A reply after an answered intervention continues the implementation
	// turn before it.
	prior, err := starter.workflowCheckpoint(ctx, lead, checkpoint)
	if err != nil {
		return remoteLeadRequest{}, err
	}
	attemptVersion, turn, implementing := workflowAttemptVersionAndTurn(
		lead.ID, "implementation", prior.AttemptID,
	)
	if !implementing || attemptVersion != run.PlanVersion {
		return remoteLeadRequest{}, ErrCommandNotAllowed
	}
	if !run.Steps.SkipReviewer {
		reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
		if err != nil {
			return remoteLeadRequest{}, err
		}
		if reviewer.Status != execution.SessionStatusWaitingForUser ||
			reviewer.AgentID != agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer ||
			reviewer.ProviderSessionID == "" {
			return remoteLeadRequest{}, ErrCommandNotAllowed
		}
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
	if _, err := starter.featureForCurrentPlan(ctx, run, storedFeature); err != nil {
		return remoteLeadRequest{}, err
	}
	// The lead may have committed locally earlier in this turn, so the checkout
	// only has to descend from the plan's baseline; the remote branch must not
	// have moved.
	var prepared workspace.Workspace
	if run.PlanVersion == 1 {
		prepared, err = starter.workspaces.VerifyImplementationContinuation(
			ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text, "",
		)
	} else {
		revision, revisionErr := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
		if revisionErr != nil {
			return remoteLeadRequest{}, revisionErr
		}
		prepared, err = starter.workspaces.VerifyImplementationContinuation(
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
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: implementationContinuationInstructions(
				prepared, command.Message, attemptID, !run.Steps.SkipPlan,
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
		// A session turn that never reached its worker is sent again with the
		// same identity, so a worker that did receive it is not asked twice.
		if rebuilt, ok := starter.rebuildSessionTurn(ctx, request); ok {
			starter.launchAdmitted(rebuilt)
			return
		}
		if resent, ok := starter.interruptedTurn(ctx, request, err); ok {
			starter.launchAdmitted(resent)
			return
		}
		starter.requireReview(ctx, request, fmt.Errorf("inspect interrupted worker attempt: %w", err))
		return
	}
	if reconciled, reconcileErr := starter.reconcileKnownEndedAttempt(ctx, request, attempt); reconcileErr != nil {
		starter.requireReview(ctx, request, reconcileErr)
		return
	} else if reconciled {
		return
	}
	if continued, continueErr := starter.continueFailedAttempt(ctx, request, attempt); continueErr != nil {
		starter.requireReview(ctx, request, continueErr)
		return
	} else if continued {
		return
	}
	if attempt.State == workerhttp.AttemptStateIndeterminate {
		starter.requireReview(ctx, request, errInterruptedAttempt)
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

func (starter *RemoteLeadStarter) reattachAdmitted(request remoteLeadRequest) {
	ctx := starter.lifetime
	attempt, err := starter.worker.GetAttempt(ctx, request.identity.AttemptReference)
	if err != nil {
		starter.requireReview(ctx, request, fmt.Errorf("inspect interrupted parallel worker attempt: %w", err))
		return
	}
	if reconciled, reconcileErr := starter.reconcileKnownEndedAttempt(ctx, request, attempt); reconcileErr != nil {
		starter.requireReview(ctx, request, reconcileErr)
		return
	} else if reconciled {
		return
	}
	if continued, continueErr := starter.continueFailedAttempt(ctx, request, attempt); continueErr != nil {
		starter.requireReview(ctx, request, continueErr)
		return
	} else if continued {
		return
	}
	if attempt.State == workerhttp.AttemptStateIndeterminate {
		starter.requireReview(ctx, request, errInterruptedAttempt)
		return
	}
	if err := starter.captureProviderSession(ctx, request.identity.SessionID, attempt); err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	starter.observe(ctx, request, attempt)
}

// reconcileKnownEndedAttempt confirms required workflow effects before any
// successor is considered. At present the acceptance-test artifact is the
// complete durable effect for its authoring turn; a private checkout commit by
// itself is useful evidence, but is not enough to advance the workflow.
func (starter *RemoteLeadStarter) reconcileKnownEndedAttempt(
	ctx context.Context,
	request remoteLeadRequest,
	attempt workerhttp.Attempt,
) (bool, error) {
	if attempt.State != workerhttp.AttemptStateTerminal || attempt.Result == nil ||
		attempt.Result.Outcome != workerhttp.OutcomeFailed || attempt.Result.Error == nil ||
		attempt.Result.Error.Code != workerhttp.ErrorIncompleteResult ||
		request.request.OutputContract != workerhttp.OutputContractAcceptanceTests {
		return false, nil
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return false, err
	}
	session, err := starter.executions.GetSession(ctx, request.identity.SessionID)
	if err != nil {
		return false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		return false, err
	}
	if _, found, err := starter.durableAcceptanceTests(ctx, run, session, checkpoint, attempt); err != nil {
		return false, err
	} else if !found {
		return false, nil
	}
	if session.Status == execution.SessionStatusRunning || session.Status == execution.SessionStatusPauseRequested {
		session, err = starter.executions.TransitionSession(
			ctx, session.ID, session.Status, execution.SessionStatusWaitingForUser, attempt.ProviderSessionID,
		)
		if err != nil {
			return false, err
		}
	}
	if session.Status != execution.SessionStatusWaitingForUser {
		return false, fmt.Errorf("acceptance-test effects are durable but reviewer session is %q", session.Status)
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, request.identity.AttemptID+":recovery:effects-confirmed", session.ID,
		worker.Event{
			Type:               worker.EventRecoveryAssessment,
			Text:               "The reviewer process ended without its final reply, but the independent acceptance-test manifest was already stored and validated. No replacement agent was started.",
			RecoveryAssessment: &worker.RecoveryAssessment{Consistent: true, RequiresUserReview: false},
		},
	); err != nil {
		return false, err
	}
	return true, starter.tryStartAcceptanceReview(ctx, run.ID)
}

func (starter *RemoteLeadStarter) continueFailedAttempt(
	ctx context.Context,
	request remoteLeadRequest,
	attempt workerhttp.Attempt,
) (bool, error) {
	// The guidance as its author wrote it, before it is framed for the agent;
	// a model switch's note is Commitarium's own.
	guidance := ""
	if !request.modelSwitch {
		guidance = strings.TrimSpace(request.recoveryMessage)
	}
	// Any failed turn can be retried (ADR-017); an incomplete structured result
	// is one kind of failure.
	// The user's Continue also answers an agent that stopped to ask for help.
	answered := request.answerBlocker && attempt.State == workerhttp.AttemptStateTerminal &&
		attempt.Result != nil && attempt.Result.Outcome == workerhttp.OutcomeCompleted &&
		(attempt.Result.Disposition == workerhttp.DispositionInputRequired || request.guided)
	if answered && request.previewCheck != "" {
		guidance := request.previewCheck
		if message := strings.TrimSpace(request.recoveryMessage); message != "" {
			guidance += "\n\nThe user continued the work order with this guidance: " + message
		}
		request.recoveryMessage = guidance
	} else if answered && request.guided && attempt.Result.Disposition != workerhttp.DispositionInputRequired {
		guidance := "Your previous turn finished, but Commitarium could not accept its result, so " +
			"the work order was blocked."
		if refusal := strings.TrimSpace(request.refusal); refusal != "" {
			guidance += " Commitarium refused it because: " + refusal
		}
		if message := strings.TrimSpace(request.recoveryMessage); message != "" {
			guidance += " The user asked you to continue with this guidance: " + message
		}
		request.recoveryMessage = guidance + " Then finish the turn as before."
	} else if answered {
		guidance := "The user reviewed the blocker you reported and asked you to continue. Check whether " +
			"it still holds, then carry on with the same work."
		if strings.TrimSpace(request.recoveryMessage) != "" {
			guidance += " The user added: " + strings.TrimSpace(request.recoveryMessage)
		}
		request.recoveryMessage = guidance
	}
	failed := attempt.State == workerhttp.AttemptStateTerminal && attempt.Result != nil &&
		(attempt.Result.Outcome == workerhttp.OutcomeFailed ||
			request.modelSwitch && attempt.Result.Outcome == workerhttp.OutcomeStopped) || answered
	if !failed && attempt.State != workerhttp.AttemptStateIndeterminate {
		return false, nil
	}
	if !request.allowRecoveryContinuation {
		return false, nil
	}
	if request.request.OutputContract == "" && request.planningStage == planningStageNone {
		return false, errors.New("the failed turn has no recoverable output contract")
	}
	if attempt.State == workerhttp.AttemptStateIndeterminate {
		closed, err := starter.worker.Supersede(ctx, workerhttp.MutationIdentity{
			AttemptReference: attempt.AttemptReference,
			IdempotencyKey:   attempt.AttemptID + ":recovery:supersede",
		}, workerhttp.SupersedeRequest{Reason: "admit one fenced recovery successor"})
		if err != nil {
			return false, fmt.Errorf("fence original attempt before recovery: %w", err)
		}
		if closed.State != workerhttp.AttemptStateTerminal {
			return false, errors.New("worker did not close the original attempt before recovery")
		}
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return false, err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return false, err
	}
	session, err := starter.executions.GetSession(ctx, request.identity.SessionID)
	if err != nil {
		return false, err
	}
	if session.Status == execution.SessionStatusRunning || session.Status == execution.SessionStatusPauseRequested {
		session, err = starter.executions.TransitionSession(
			ctx, session.ID, session.Status, execution.SessionStatusWaitingForUser, session.ProviderSessionID,
		)
		if err != nil {
			return false, err
		}
	}
	if session.Status != execution.SessionStatusWaitingForUser {
		return false, fmt.Errorf("failed %s session is not ready for one recovery successor", request.agentName)
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		return false, err
	}
	// The conversation's latest attempt may be an answered intervention; the
	// successor still continues the workflow turn before it, and takes that
	// turn's next identity.
	predecessor := checkpoint.AttemptID
	if attempt.AttemptID != checkpoint.AttemptID && isInterventionAttempt(session.ID, checkpoint.AttemptID) {
		predecessor = attempt.AttemptID
	}
	nextAttemptID, err := recoverySuccessorAttemptID(session.ID, predecessor)
	if err != nil {
		return false, err
	}
	prepared, err := starter.workspaceForCurrentPlan(ctx, run, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return false, err
	}
	briefing, err := starter.recoveryBriefing(
		ctx, run, storedFeature, session, checkpoint, attempt, prepared, nextAttemptID,
	)
	if err != nil {
		return false, err
	}
	if request.interruption != "" {
		briefing += interruptionBriefing(request.interruption)
		if len(briefing) > workerhttp.MaxInstructionsBytes {
			return false, errors.New("recovery briefing exceeds the worker instruction limit")
		}
	}
	admit := starter.executions.BeginChainedTurnInState
	// During implementation the reviewer authors private acceptance tests in
	// parallel, so the successor is admitted alongside it.
	if parallel, ok := starter.executions.(remoteLeadParallelChains); ok &&
		run.IndependentAcceptanceTests && storedFeature.State == feature.StateImplementing {
		admit = parallel.BeginChainedTurnAlongside
	}
	admitReason := "The " + request.agentName + " is completing the missing result from a fenced attempt."
	if request.previewCheck != "" {
		admitReason = previewCheckFixingReason
	}
	admitted, err := admit(ctx, session.ID, checkpoint, nextAttemptID, storedFeature.State, admitReason)
	if err != nil {
		return false, err
	}
	if !admitted {
		return true, nil
	}
	request.identity = workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: session.ID, AttemptID: nextAttemptID},
		IdempotencyKey:   nextAttemptID + ":resume",
	}
	request.request = workerhttp.PutAttemptRequest{
		Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: starter.profileID(agentForSession(run, session.Role), session.Role),
			Model:          modelForSession(run, session.Role), ProjectID: storedFeature.ProjectID,
			FeatureID: storedFeature.ID, Role: workerhttp.Role(session.Role), WorkspaceID: prepared.ID,
		},
		ProviderSessionID: session.ProviderSessionID,
		Instructions:      briefing, OutputContract: request.request.OutputContract,
	}
	if session.ProviderSessionID == "" {
		// The failed turn never got a provider conversation, so there is
		// nothing to resume: the successor starts one from durable context.
		request.forceFreshRecovery = true
		request.request.Mode = workerhttp.AttemptModeStart
		request.identity.IdempotencyKey = nextAttemptID + ":start"
	}
	if err := request.request.Validate(request.identity); err != nil {
		return false, err
	}
	if guidance != "" {
		starter.recordGuidance(ctx, session.ID, nextAttemptID, guidance, request.guidanceFromAssistant)
	}
	starter.launchRecoverySuccessor(ctx, request, session, storedFeature, briefing)
	return true, nil
}

// recordGuidance shows the guidance a continuation carries in the agent's
// transcript, as a message to it from whoever wrote it. The event ID's suffix
// names the author, and makes recording it again a no-op.
func (starter *RemoteLeadStarter) recordGuidance(
	ctx context.Context, sessionID, attemptID, guidance string, fromAssistant bool,
) {
	author := "user"
	if fromAssistant {
		author = "assistant"
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, attemptID+":guidance:"+author, sessionID,
		worker.Event{Type: worker.EventUserMessage, Text: guidance},
	); err != nil {
		starter.reportError(fmt.Errorf("record continue guidance for session %q: %w", sessionID, err))
	}
}

func agentForSession(run execution.Run, role worker.Role) string {
	if role == worker.RoleReviewer {
		return run.AgentProviders.ReviewerAgent
	}
	return run.AgentProviders.LeadAgent
}

func modelForSession(run execution.Run, role worker.Role) string {
	if role == worker.RoleReviewer {
		return run.AgentModels.Reviewer
	}
	return run.AgentModels.Lead
}

func recoverySuccessorAttemptID(sessionID, attemptID string) (string, error) {
	// Planning turns are numbered by counting each agent's linked messages,
	// so a successor takes a separate ID space instead of the next turn.
	if version, turn, successor, ok := planningRecoveryVersionAndTurn(sessionID, attemptID); ok {
		return planningRecoveryAttemptID(sessionID, version, turn, successor+1), nil
	}
	if version, turn, ok := planningAttemptVersionAndTurn(sessionID, attemptID); ok {
		return planningRecoveryAttemptID(sessionID, version, turn, 1), nil
	}
	// A review, and the lead's correction or readiness answering it, keep
	// their round, so the rounds still pair up after a retry.
	for _, kind := range roundKinds {
		if version, round, try, ok := workflowAttemptCoordinates(sessionID, kind, attemptID); ok {
			return roundRetryAttemptID(sessionID, kind, version, round, try+1), nil
		}
	}
	for _, kind := range []string{"implementation", "acceptance", "update", "approval", "session"} {
		if version, turn, ok := workflowAttemptVersionAndTurn(sessionID, kind, attemptID); ok {
			return workflowAttemptForVersion(sessionID, kind, version, turn+1), nil
		}
	}
	return "", errors.New("failed turn does not have a recognized recovery attempt identity")
}

// planningRecoveryAttemptID names the successor of a failed planning turn.
// It keeps the logical turn so stage detection still treats it as that turn.
func planningRecoveryAttemptID(sessionID string, version, turn, successor int) string {
	if version == 1 {
		return sessionID + ":planning-recovery:" + strconv.Itoa(turn) + ":" + strconv.Itoa(successor)
	}
	return sessionID + ":planning-recovery:v" + strconv.Itoa(version) + ":" +
		strconv.Itoa(turn) + ":" + strconv.Itoa(successor)
}

func planningRecoveryVersionAndTurn(sessionID, attemptID string) (int, int, int, bool) {
	value, found := strings.CutPrefix(attemptID, sessionID+":planning-recovery:")
	if !found {
		return 0, 0, 0, false
	}
	parts := strings.Split(value, ":")
	version := 1
	if len(parts) == 3 && strings.HasPrefix(parts[0], "v") {
		parsed, err := strconv.Atoi(strings.TrimPrefix(parts[0], "v"))
		if err != nil || parsed < 2 {
			return 0, 0, 0, false
		}
		version, parts = parsed, parts[1:]
	}
	if len(parts) != 2 {
		return 0, 0, 0, false
	}
	turn, turnErr := strconv.Atoi(parts[0])
	successor, successorErr := strconv.Atoi(parts[1])
	if turnErr != nil || successorErr != nil || turn < 1 || successor < 1 {
		return 0, 0, 0, false
	}
	return version, turn, successor, true
}

func (starter *RemoteLeadStarter) recoveryBriefing(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	session execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	attempt workerhttp.Attempt,
	prepared workspace.Workspace,
	nextAttemptID string,
) (string, error) {
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return "", err
	}
	plan := "No submitted plan text was found."
	if len(messages) > 0 {
		plan = messages[len(messages)-1].Event.Text
	}
	events, err := starter.executions.EventsForSession(ctx, session.ID)
	if err != nil {
		return "", err
	}
	start := 0
	if len(events) > 8 {
		start = len(events) - 8
	}
	var history strings.Builder
	for _, event := range events[start:] {
		fmt.Fprintf(&history, "- %s: %s\n", event.Type, event.Text)
	}
	cause := "The prior attempt ended without a usable result."
	if attempt.Result != nil && attempt.Result.Error != nil {
		cause = attempt.Result.Error.Message
	}
	briefing := fmt.Sprintf(
		"Recovery continuation. Complete the same logical %s turn and %s Do not redo durable work that is already correct. Inspect the existing checkout and commits first.\n\nGoal:\n%s\n\nAgreed plan:\n%s\n\nPrior attempt: %s (events recorded through %d)\nWhy it needs continuation: %s\n\nWorkspace: %s, branch %s, base commit %s, Forgejo PR #%d.\n\nRecent durable activity:\n%s",
		session.Role, recoveryDeliverable(session, attempt), storedFeature.AcceptedGoal, plan, checkpoint.AttemptID,
		checkpoint.LastEventSequence, cause, prepared.ID, prepared.Branch,
		prepared.BaseCommitID, prepared.PullRequestNumber, history.String(),
	)
	briefing += recoveryPublicationInstructions(session.ID, nextAttemptID)
	if len(briefing) > workerhttp.MaxInstructionsBytes {
		return "", errors.New("recovery briefing exceeds the worker instruction limit")
	}
	return briefing, nil
}

// recoveryDeliverable says what the successor must return. Conversational
// planning turns have no structured result; their final reply is the result.
func recoveryDeliverable(session execution.Session, attempt workerhttp.Attempt) string {
	if _, _, ok := planningAttemptVersionAndTurn(session.ID, attempt.AttemptID); ok && session.Role == worker.RoleReviewer {
		return "give your complete final reply for that turn again, as your final message; your earlier reply was lost."
	}
	return "return the required structured result."
}

func (starter *RemoteLeadStarter) launchRecoverySuccessor(
	ctx context.Context,
	request remoteLeadRequest,
	session execution.Session,
	storedFeature feature.Feature,
	briefing string,
) {
	if request.recoveryMessage != "" {
		heading := "User recovery guidance:"
		if request.previewCheck != "" {
			heading = "Commitarium's preview check:"
		}
		briefing += "\n\n" + heading + "\n" + request.recoveryMessage
		if len(briefing) > workerhttp.MaxInstructionsBytes {
			starter.requireReview(ctx, request, errors.New("recovery briefing and user guidance exceed the worker instruction limit"))
			return
		}
		request.request.Instructions = briefing
	}
	if request.forceFreshRecovery {
		oldProviderSessionID := session.ProviderSessionID
		request.request.Mode = workerhttp.AttemptModeStart
		request.request.ProviderSessionID = ""
		request.identity.IdempotencyKey = request.identity.AttemptID + ":start"
		fresh, _, err := starter.worker.PutAttempt(ctx, request.identity, request.request)
		if err != nil {
			if inspected, inspectErr := starter.worker.GetAttempt(ctx, request.identity.AttemptReference); inspectErr == nil {
				fresh = inspected
			} else {
				starter.requireReview(ctx, request, errors.Join(err, inspectErr))
				return
			}
		}
		if fresh.ProviderSessionID != "" && fresh.ProviderSessionID != oldProviderSessionID {
			if _, err := starter.executions.RotateProviderSession(ctx, session.ID, oldProviderSessionID, fresh.ProviderSessionID); err != nil {
				starter.requireReview(ctx, request, err)
				return
			}
		}
		_, _ = starter.executions.RecordSessionEventWithID(
			ctx, request.identity.AttemptID+":recovery:fresh-conversation", session.ID,
			worker.Event{Type: worker.EventRecoveryAssessment, Text: "The user chose a fresh fenced conversation. It received the durable goal, plan, workspace, activity history, and recovery guidance.", RecoveryAssessment: &worker.RecoveryAssessment{Consistent: true}},
		)
		starter.observe(ctx, request, fresh)
		return
	}
	attempt, _, err := starter.worker.PutAttempt(ctx, request.identity, request.request)
	if err != nil {
		if inspected, inspectErr := starter.worker.GetAttempt(ctx, request.identity.AttemptReference); inspectErr == nil {
			attempt = inspected
		} else {
			starter.requireReview(ctx, request, errors.Join(err, inspectErr))
			return
		}
	}
	if !resumeLaunchUnavailable(attempt) {
		starter.observe(ctx, request, attempt)
		return
	}
	current, err := starter.executions.GetSession(ctx, session.ID)
	if err != nil || current.Status != execution.SessionStatusRunning {
		starter.requireReview(ctx, request, errors.Join(err, errors.New("resume failed before fresh recovery fallback")))
		return
	}
	current, err = starter.executions.TransitionSession(
		ctx, current.ID, current.Status, execution.SessionStatusWaitingForUser, current.ProviderSessionID,
	)
	if err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
	if err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	nextAttemptID, err := recoverySuccessorAttemptID(session.ID, checkpoint.AttemptID)
	if err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	admitted, err := starter.executions.BeginChainedTurnInState(
		ctx, session.ID, checkpoint, nextAttemptID, storedFeature.State,
		"The original conversation could not resume; one fresh "+request.agentName+" conversation is continuing from durable context.",
	)
	if err != nil || !admitted {
		starter.requireReview(ctx, request, errors.Join(err, errors.New("fresh recovery fallback was not admitted")))
		return
	}
	oldProviderSessionID := current.ProviderSessionID
	request.identity = workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: session.ID, AttemptID: nextAttemptID},
		IdempotencyKey:   nextAttemptID + ":start",
	}
	request.request.Mode = workerhttp.AttemptModeStart
	request.request.ProviderSessionID = ""
	request.request.Instructions = briefing + "\n\nThe original provider conversation was unavailable. You are the single fenced replacement conversation."
	fresh, _, err := starter.worker.PutAttempt(ctx, request.identity, request.request)
	if err != nil {
		if inspected, inspectErr := starter.worker.GetAttempt(ctx, request.identity.AttemptReference); inspectErr == nil {
			fresh = inspected
		} else {
			starter.requireReview(ctx, request, errors.Join(err, inspectErr))
			return
		}
	}
	if fresh.ProviderSessionID != "" && fresh.ProviderSessionID != oldProviderSessionID {
		if _, err := starter.executions.RotateProviderSession(ctx, session.ID, oldProviderSessionID, fresh.ProviderSessionID); err != nil {
			starter.requireReview(ctx, request, err)
			return
		}
	}
	_, _ = starter.executions.RecordSessionEventWithID(
		ctx, nextAttemptID+":recovery:fresh-conversation", session.ID,
		worker.Event{Type: worker.EventRecoveryAssessment, Text: "The original provider conversation could not be resumed, so recovery started one fresh fenced conversation with the durable goal, plan, workspace, and activity history.", RecoveryAssessment: &worker.RecoveryAssessment{Consistent: true}},
	)
	starter.observe(ctx, request, fresh)
}

func resumeLaunchUnavailable(attempt workerhttp.Attempt) bool {
	return attempt.State == workerhttp.AttemptStateTerminal && attempt.Result != nil &&
		attempt.Result.Outcome == workerhttp.OutcomeFailed && attempt.Result.Error != nil &&
		attempt.Result.Error.Retryable &&
		(attempt.Result.Error.Code == workerhttp.ErrorInternal || attempt.Result.Error.Code == workerhttp.ErrorProviderSessionMissing)
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
		starter.requireReview(ctx, request, errInterruptedAttempt)
		return
	}

	result, err := starter.pump.Run(ctx, request.identity.SessionID)
	if err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	if result.AttemptInspected && result.Attempt.State == workerhttp.AttemptStateIndeterminate {
		starter.requireReview(ctx, request, errInterruptedAttempt)
		return
	}
	if !result.AttemptInspected || result.Attempt.State != workerhttp.AttemptStateTerminal {
		starter.requireReview(ctx, request, errors.New("worker stream ended without a terminal attempt"))
		return
	}
	starter.finish(ctx, request, result.Attempt)
}

// recordUsage is accounting only: a failure is reported but never blocks the
// workflow.
func (starter *RemoteLeadStarter) recordUsage(
	ctx context.Context,
	reference workerhttp.AttemptReference,
	assignment workerhttp.Assignment,
	usage *workerhttp.TokenUsage,
) {
	if starter.usage == nil || usage == nil {
		return
	}
	err := starter.usage.RecordAttemptUsage(ctx, execution.AttemptUsage{
		SessionID: reference.SessionID, AttemptID: reference.AttemptID,
		Model: assignment.Model, Effort: assignment.Effort,
		Billing: usage.Billing, CostUSD: usage.CostUSD,
		Usage: execution.TokenUsage{
			InputTokens: usage.InputTokens, CachedInputTokens: usage.CachedInputTokens,
			CacheWriteTokens: usage.CacheWriteTokens, OutputTokens: usage.OutputTokens,
		},
		RecordedAt: time.Now().UTC(),
	})
	if err != nil {
		starter.reportError(fmt.Errorf("record token usage for attempt %q: %w", reference.AttemptID, err))
	}
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
	starter.recordUsage(ctx, request.identity.AttemptReference, attempt.Assignment, attempt.Result.Usage)
	if attempt.Result.Outcome != workerhttp.OutcomeFailed {
		starter.turnEnded(ctx, request.runID, request.identity.SessionID)
		starter.agentAnswered(ctx, attempt.Assignment.AgentProfileID)
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
		if request.interventionID != "" {
			if !worker.InterventionEffect(attempt.Result.InterventionEffect).IsValid() {
				starter.requireReview(ctx, request, fmt.Errorf(
					"completed intervention omitted its structured effect: %w: found effect %q; answer with "+
						"effect guidance_applied, clarification_required, or replanning_required and your response",
					errInterventionEffect, attempt.Result.InterventionEffect))
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
		if attempt.Result.SessionTitle != "" {
			starter.nameSession(ctx, request.runID, attempt.Result.SessionTitle)
		}
		var planningMessage execution.Event
		if request.planningStage != planningStageNone && request.planningStage != planningStagePlanApproval {
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
		if request.request.OutputContract == workerhttp.OutputContractAcceptanceTests {
			if attempt.Result.Disposition != workerhttp.DispositionInputRequired && attempt.Result.AcceptanceTests == nil {
				err = fmt.Errorf("reviewer completed acceptance-test authoring without structured test metadata: %w: "+
					"return action 'authored' with your summary, the private test commit in test_commit_id, and "+
					"each test in tests", errAcceptanceTestsResult)
				break
			}
			if attempt.Result.Disposition != workerhttp.DispositionInputRequired {
				run, loadErr := starter.executions.GetRun(ctx, request.runID)
				if loadErr != nil {
					err = loadErr
					break
				}
				normalized, normalizeErr := attempt.Result.AcceptanceTests.NormalizeInitial(run.PlanVersion)
				if normalizeErr != nil {
					err = fmt.Errorf("reviewer's acceptance tests are invalid: %w: %v; fix that in the tests "+
						"you hand in and return action 'authored' again", errAcceptanceTestsResult, normalizeErr)
					break
				}
				acceptanceArtifacts, ok := starter.artifacts.(remoteLeadAcceptanceArtifactService)
				if !ok {
					err = errors.New("acceptance-test artifact service is unavailable")
					break
				}
				if _, artifactErr := acceptanceArtifacts.UpsertAcceptanceTests(
					ctx, run.FeatureID, normalized,
					workflow.Actor{Kind: workflow.ActorKindAgent, ID: session.ID},
					request.identity.AttemptID+":acceptance-tests",
				); artifactErr != nil {
					err = artifactErr
					break
				}
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
		if request.planningStage == planningStagePlanApproval {
			starter.continueApprovedPlanning(ctx, request)
			return
		}
		if request.request.OutputContract == workerhttp.OutputContractAcceptanceTests {
			if attempt.Result.Disposition == workerhttp.DispositionInputRequired {
				err = starter.handleInputRequired(ctx, request, *attempt.Result)
				break
			}
			err = starter.tryStartAcceptanceReview(ctx, request.runID)
			break
		}
		if request.planningStage == planningStageLeadResponse {
			if planningMessage.Type == worker.EventPlanSubmitted {
				if starter.artifacts != nil {
					if attempt.Result.ImplementationPlan == nil {
						starter.requireReview(ctx, request, fmt.Errorf(
							"submitted plan omitted its structured implementation checklist: %w: submit the plan "+
								"again with its plan_title, plan_subtitle, and steps", errPlanChecklistResult))
						return
					}
					run, loadErr := starter.executions.GetRun(ctx, request.runID)
					if loadErr != nil {
						starter.requireReview(ctx, request, loadErr)
						return
					}
					normalized, normalizeErr := attempt.Result.ImplementationPlan.NormalizeInitial(run.PlanVersion)
					if normalizeErr != nil {
						starter.requireReview(ctx, request, fmt.Errorf(
							"submitted plan's implementation checklist is invalid: %w: %v; fix that in the steps "+
								"and submit the plan again", errPlanChecklistResult, normalizeErr))
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
				if continueErr := starter.continueSubmittedPlan(
					ctx, request.runID, planningMessage,
				); continueErr != nil {
					starter.requirePlanPublicationReview(
						ctx, request.runID, planningMessage, continueErr,
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
			startNext := starter.startReviewerResponse
			if run.Steps.SkipReviewer {
				startNext = starter.startLeadResponse
			}
			next, admitted, startErr := startNext(ctx, run, storedFeature, messages, true)
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
			} else if _, updating := baseUpdateTurnNumber(
				request.identity.SessionID, request.identity.AttemptID,
			); updating {
				err = starter.verifyImplementationBaseUpdate(ctx, request, *attempt.Result)
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
		switched, switchErr := starter.hasModelSwitch(ctx, request.runID, attempt.AttemptID)
		switch {
		case switchErr != nil:
			err = switchErr
		case switched:
			err = starter.resumeAfterModelSwitch(ctx, request, session, attempt)
		default:
			err = starter.failSession(ctx, session, execution.SessionStatusStopped, attempt, "The "+request.agentName+" was stopped.")
		}
	case workerhttp.OutcomeFailed:
		if attempt.Result.Error != nil && attempt.Result.Error.Code == workerhttp.ErrorIncompleteResult {
			err = fmt.Errorf("%s ended before returning its required structured result: %w: the worker "+
				"found: %s; hand in your final result again in the required format, without redoing work "+
				"that is already published", request.agentName, errIncompleteResult, attempt.Result.Error.Message)
		} else {
			err = starter.pauseFailedTurn(ctx, request, session, attempt)
		}
	default:
		err = fmt.Errorf("unsupported worker outcome %q", attempt.Result.Outcome)
	}
	if err != nil {
		starter.requireReview(ctx, request, err)
	}
}

// Checks of the structured result an agent hands in. Each refusal says what
// the result lacks and how to hand it in again; the sentinel's text is the
// blocker's stable fingerprint.
var (
	errIncompleteResult      = agentRejection("the turn ended without its required structured result")
	errPublicationResult     = agentRejection("the result has no publication facts")
	errReviewResult          = agentRejection("the result has no review facts")
	errAcceptanceTestsResult = agentRejection("the acceptance-test result is missing or invalid")
	errPlanChecklistResult   = agentRejection("the submitted plan's checklist is missing or invalid")
	errInterventionEffect    = agentRejection("the intervention answer has no valid effect")
	errReadinessDecision     = agentRejection("the merge-readiness result has no valid decision")
	errPlanApprovalDecision  = agentRejection("the plan approval has no valid decision")
)

// publicationFactsGuidance says how a lead hands in a publication.
const publicationFactsGuidance = "return action 'published' with your summary, the exact lowercase HEAD in " +
	"commit_id, and pull_request_number"

// Checks of what an agent published in Forgejo and its checkout, which its
// next turn can put right (see rejectPublication).
var (
	errAuditCommentMissing    = agentRejection("the audit comment is not on the pull request")
	errAuditCommentInvalid    = agentRejection("the audit comment is not in the required form")
	errReviewNotAsReported    = agentRejection("the Forgejo review does not match the reported review")
	errReportedPullRequest    = agentRejection("the reported pull request is not the work order's")
	errNoNewCommit            = agentRejection("the turn did not publish a new commit")
	errReportedCommitNotFound = stateRejection("the reported commit is not the feature branch head")
	errCheckoutNotAsReported  = stateRejection("the checkout is not as the turn reported")
)

// publicationRejections pairs each workspace verification failure an agent
// can fix with the check that names it.
var publicationRejections = []struct {
	cause     error
	rejection *turnRejection
}{
	{workspace.ErrAuditCommentMissing, errAuditCommentMissing},
	{workspace.ErrAuditCommentInvalid, errAuditCommentInvalid},
	{workspace.ErrReviewNotAsReported, errReviewNotAsReported},
	{workspace.ErrReportedPullRequestMismatch, errReportedPullRequest},
	{workspace.ErrNoNewCommit, errNoNewCommit},
	{workspace.ErrReportedCommitNotPushed, errReportedCommitNotFound},
	{workspace.ErrCheckoutNotAsReported, errCheckoutNotAsReported},
}

// rejectPublication marks a workspace verification failure the agent can fix
// with its check; any other failure is returned as it is.
func rejectPublication(err error) error {
	for _, known := range publicationRejections {
		if errors.Is(err, known.cause) {
			return fmt.Errorf("%w: %w", known.rejection, err)
		}
	}
	return err
}

// errImplementationChecklist is a lead's checklist not matching its
// publication. Its text is the blocker's stable fingerprint; the wrapped
// detail says what to fix.
var errImplementationChecklist = agentRejection("implementation checklist is incomplete or does not end at the published commit")

// checkImplementationChecklist reports how checklist falls short of a
// publication at publishedCommit for planVersion, naming each unfinished
// step and the command that records it, so the lead can fix it in one turn.
func checkImplementationChecklist(
	checklist featureartifact.ImplementationPlan, planVersion int, publishedCommit string,
) error {
	if checklist.PlanVersion != planVersion {
		return fmt.Errorf("%w: the checklist is for plan version %d but the agreed plan is version %d",
			errImplementationChecklist, checklist.PlanVersion, planVersion)
	}
	if len(checklist.Steps) == 0 {
		return fmt.Errorf("%w: the checklist has no steps", errImplementationChecklist)
	}
	var unfinished []string
	for index, step := range checklist.Steps {
		if step.Status == featureartifact.StepCompleted {
			continue
		}
		commit := "<lowercase-commit-id>"
		if index == len(checklist.Steps)-1 {
			commit = strings.ToLower(publishedCommit)
		}
		complete := fmt.Sprintf("'commitarium-artifact plan complete %s %s'", step.ID, commit)
		if step.Status == featureartifact.StepPending {
			unfinished = append(unfinished, fmt.Sprintf(
				"step %d %q is pending: run 'commitarium-artifact plan start %s', then %s",
				step.Position, step.ID, step.ID, complete))
		} else {
			unfinished = append(unfinished, fmt.Sprintf(
				"step %d %q is %s: record its commit with %s", step.Position, step.ID, step.Status, complete))
		}
	}
	if len(unfinished) > 0 {
		return fmt.Errorf("%w: %s", errImplementationChecklist, strings.Join(unfinished, "; "))
	}
	last := checklist.Steps[len(checklist.Steps)-1]
	if last.CommitID != publishedCommit {
		return fmt.Errorf("%w: the last step %q is recorded at commit %s but the published commit is %s; "+
			"the published head must be the commit recorded for the last step",
			errImplementationChecklist, last.ID, last.CommitID, publishedCommit)
	}
	return nil
}

func (starter *RemoteLeadStarter) verifyImplementationPublication(
	ctx context.Context,
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) error {
	if result.Publication == nil {
		return fmt.Errorf("lead completed implementation without structured publication facts: %w: %s",
			errPublicationResult, publicationFactsGuidance)
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
	if starter.artifacts != nil && !run.Steps.SkipPlan {
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
			if err := checkImplementationChecklist(checklist, run.PlanVersion, result.Publication.CommitID); err != nil {
				return err
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
		return fmt.Errorf("verify lead publication before review: %w", rejectPublication(verifyErr))
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
	if !run.Steps.CodeReview() {
		// Without code review (ADR-018, ADR-031) the lead's verified
		// publication is the work order's result; the merge gate and
		// validation still apply.
		return starter.enterMergeGate(
			ctx, request, run, storedFeature, result.Publication.CommitID,
			result.Publication.PullRequestNumber, implementationLeadOnlyReadyReason,
		)
	}
	if run.IndependentAcceptanceTests {
		return starter.tryStartAcceptanceReview(ctx, run.ID)
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

func (starter *RemoteLeadStarter) tryStartAcceptanceReview(ctx context.Context, runID string) error {
	starter.acceptanceJoinMu.Lock()
	locked := true
	defer func() {
		if locked {
			starter.acceptanceJoinMu.Unlock()
		}
	}()
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if !run.IndependentAcceptanceTests {
		return nil
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return err
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return err
	}
	// The first branch to finish leaves the run active. Only the second branch,
	// after both durable checkpoints exist, performs the review handoff.
	if lead.Status != execution.SessionStatusWaitingForUser || reviewer.Status != execution.SessionStatusWaitingForUser {
		return nil
	}
	leadCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return err
	}
	reviewerCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return err
	}
	leadVersion, _, implementing := workflowAttemptVersionAndTurn(lead.ID, "implementation", leadCheckpoint.AttemptID)
	acceptanceVersion, _, authoring := workflowAttemptVersionAndTurn(reviewer.ID, "acceptance", reviewerCheckpoint.AttemptID)
	if !implementing || leadVersion != run.PlanVersion || !authoring || acceptanceVersion != run.PlanVersion {
		return errors.New("parallel implementation checkpoints do not match the current plan")
	}
	if err := starter.confirmCompletedTurn(ctx, lead, leadCheckpoint); err != nil {
		return fmt.Errorf("confirm completed implementation: %w", err)
	}
	verified := false
	events, err := starter.executions.EventsForSession(ctx, lead.ID)
	if err != nil {
		return err
	}
	verifiedEventID := leadCheckpoint.AttemptID + ":publication-verified"
	for _, event := range events {
		if event.ID == verifiedEventID {
			verified = true
			break
		}
	}
	if !verified {
		return nil
	}
	leadAttempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: leadCheckpoint.AttemptID})
	if err != nil {
		return err
	}
	reviewerAttempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: reviewerCheckpoint.AttemptID})
	if err != nil {
		return err
	}
	if leadAttempt.Result == nil || leadAttempt.Result.Publication == nil {
		return errors.New("parallel implementation results are incomplete")
	}
	tests, found, err := starter.durableAcceptanceTests(ctx, run, reviewer, reviewerCheckpoint, reviewerAttempt)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("independent acceptance-test manifest is missing")
	}
	tests.ImplementationCommitID = leadAttempt.Result.Publication.CommitID
	acceptanceArtifacts, ok := starter.artifacts.(remoteLeadAcceptanceArtifactService)
	if !ok {
		return errors.New("acceptance-test artifact service is unavailable")
	}
	if _, err := acceptanceArtifacts.UpsertAcceptanceTests(
		ctx, run.FeatureID, tests,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
		leadCheckpoint.AttemptID+":pin-acceptance-implementation",
	); err != nil {
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
	if len(messages) == 0 || messages[len(messages)-1].Role != worker.RoleLead || messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return errors.New("acceptance review has no durable agreed plan")
	}
	if paused, err := starter.stopAtPauseBoundary(ctx, run.ID, "Implementation and private acceptance tests are ready for independent review."); err != nil || paused {
		return err
	}
	next, admitted, err := starter.startImplementationReview(
		ctx, run, storedFeature, messages[len(messages)-1].Event,
		leadAttempt.Result.Summary, *leadAttempt.Result.Publication, 1,
	)
	if err != nil {
		return err
	}
	starter.acceptanceJoinMu.Unlock()
	locked = false
	if admitted {
		starter.launchAdmitted(next)
	}
	return nil
}

func (starter *RemoteLeadStarter) durableAcceptanceTests(
	ctx context.Context,
	run execution.Run,
	reviewer execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	attempt workerhttp.Attempt,
) (featureartifact.AcceptanceTests, bool, error) {
	if starter.artifacts == nil {
		return featureartifact.AcceptanceTests{}, false, nil
	}
	if attempt.SessionID != reviewer.ID || attempt.AttemptID != checkpoint.AttemptID ||
		attempt.State != workerhttp.AttemptStateTerminal || attempt.Result == nil ||
		attempt.ProviderSessionID != reviewer.ProviderSessionID ||
		attempt.LatestEventSequence != checkpoint.LastEventSequence {
		return featureartifact.AcceptanceTests{}, false, errors.New("acceptance-test attempt does not match its durable checkpoint")
	}
	completed := attempt.Result.Outcome == workerhttp.OutcomeCompleted &&
		attempt.Result.Disposition == workerhttp.DispositionSucceeded &&
		attempt.Result.AcceptanceTests != nil
	incomplete := attempt.Result.Outcome == workerhttp.OutcomeFailed && attempt.Result.Error != nil &&
		attempt.Result.Error.Code == workerhttp.ErrorIncompleteResult
	if !completed && !incomplete {
		return featureartifact.AcceptanceTests{}, false, nil
	}
	artifact, err := starter.artifacts.GetFeatureArtifact(ctx, run.FeatureID, featureartifact.KindAcceptanceTests)
	if errors.Is(err, workflow.ErrArtifactNotFound) {
		return featureartifact.AcceptanceTests{}, false, nil
	}
	if err != nil {
		return featureartifact.AcceptanceTests{}, false, fmt.Errorf("load independent acceptance tests: %w", err)
	}
	tests := featureartifact.AcceptanceTests{}
	if err := json.Unmarshal([]byte(artifact.Document), &tests); err != nil {
		return featureartifact.AcceptanceTests{}, false, fmt.Errorf("decode independent acceptance tests: %w", err)
	}
	if err := tests.Validate(); err != nil || tests.PlanVersion != run.PlanVersion {
		return featureartifact.AcceptanceTests{}, false, errors.New("independent acceptance-test manifest does not match the current plan")
	}
	if completed && tests.TestCommitID != attempt.Result.AcceptanceTests.TestCommitID {
		return featureartifact.AcceptanceTests{}, false, errors.New("independent acceptance-test manifest does not match the private reviewer commit")
	}
	return tests, true, nil
}

// previousReviewedCommit returns the commit the reviewer's previous review
// attempt approved or rejected, and whether it approved it. An empty result
// makes the next review a full one, which is always safe.
func (starter *RemoteLeadStarter) previousReviewedCommit(ctx context.Context, sessionID, attemptID string) (string, bool) {
	attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID})
	if err != nil || attempt.Result == nil || attempt.Result.Review == nil {
		return "", false
	}
	return attempt.Result.Review.CommitID, attempt.Result.Disposition == workerhttp.DispositionSucceeded
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
	go starter.runAcceptanceTestsItself(starter.lifetime, run, storedFeature, publication.CommitID)
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
		reviewer.AgentID != agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer ||
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
	acceptanceTestCommitID := ""
	if run.IndependentAcceptanceTests {
		artifact, artifactErr := starter.artifacts.GetFeatureArtifact(ctx, run.FeatureID, featureartifact.KindAcceptanceTests)
		if artifactErr != nil {
			return remoteLeadRequest{}, false, artifactErr
		}
		tests := featureartifact.AcceptanceTests{}
		if decodeErr := json.Unmarshal([]byte(artifact.Document), &tests); decodeErr != nil {
			return remoteLeadRequest{}, false, decodeErr
		}
		acceptanceTestCommitID = tests.TestCommitID
		// Each review round runs the tests against the corrected commit, so
		// the checklist follows that commit and starts pending again.
		if round > 1 && tests.ImplementationCommitID != publication.CommitID {
			tests.ImplementationCommitID = publication.CommitID
			for index := range tests.Tests {
				tests.Tests[index].Status = featureartifact.AcceptanceTestPending
				tests.Tests[index].Note = ""
			}
			acceptanceArtifacts, ok := starter.artifacts.(remoteLeadAcceptanceArtifactService)
			if !ok {
				return remoteLeadRequest{}, false, errors.New("acceptance-test artifact service is unavailable")
			}
			if _, pinErr := acceptanceArtifacts.UpsertAcceptanceTests(
				ctx, run.FeatureID, tests,
				workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
				implementationReviewAttemptForVersion(reviewer.ID, run.PlanVersion, round)+":pin-acceptance-implementation",
			); pinErr != nil {
				return remoteLeadRequest{}, false, pinErr
			}
		}
	}
	attemptID := implementationReviewAttemptForVersion(reviewer.ID, run.PlanVersion, round)
	// The first review reads the whole feature and the preview rules. Later
	// rounds review only the correction since the commit reviewed last, or,
	// after an approved commit was brought up to date with main, only that
	// merge (ADR-025).
	previewContext := ""
	previousReviewedCommitID := ""
	previousApproved := false
	if round == 1 {
		previewContext, err = starter.previewRunInstructions(ctx, storedFeature.ProjectID)
		if err != nil {
			return remoteLeadRequest{}, false, err
		}
	} else {
		previousReviewedCommitID, previousApproved = starter.previousReviewedCommit(ctx, reviewer.ID, checkpoint.AttemptID)
		previousApproved = previousApproved && previousReviewedCommitID != publication.CommitID
	}
	request := remoteLeadRequest{
		runID: run.ID, agentName: "reviewer", waitingReason: implementationReviewRunningReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: reviewer.ProviderSessionID,
			Instructions: implementationReviewInstructions(
				prepared, leadSummary,
				publication.CommitID, attemptID, previousReviewedCommitID, previousApproved, acceptanceTestCommitID,
			) + previewContext,
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
		lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) || lead.Role != worker.RoleLead ||
		lead.ProviderSessionID == "" {
		return remoteLeadRequest{}, false, errors.New("lead implementation conversation is not safely resumable")
	}
	priorVersion, priorRound, alreadyCorrecting := workflowAttemptVersionAndTurn(
		lead.ID, "correction", checkpoint.AttemptID,
	)
	alreadyCorrecting = alreadyCorrecting && priorVersion == run.PlanVersion
	// A review of the lead's update onto main can request changes too
	// (ADR-025).
	updated, err := starter.baseUpdatePrecedesReview(ctx, run, lead, checkpoint, review)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
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
	} else if !updated && round != 1 {
		return remoteLeadRequest{}, false, errors.New("lead has no preceding correction round")
	} else if implementationVersion, _, implementing := workflowAttemptVersionAndTurn(
		lead.ID, "implementation", checkpoint.AttemptID,
	); !updated && (!implementing || implementationVersion != run.PlanVersion) {
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
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: implementationCorrectionInstructions(
				prepared, review.Summary,
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
		return fmt.Errorf("lead completed correction without structured publication facts: %w: %s, "+
			"and finding_answers answering each review finding", errPublicationResult, publicationFactsGuidance)
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
		workerhttp.ChangesNothing(result.Publication.FindingAnswers),
	); err != nil {
		return fmt.Errorf("verify lead review response: %w", rejectPublication(err))
	}
	verified := fmt.Sprintf(
		"Verified lead response to review #%d: corrected commit %s is the head of Forgejo pull request #%d and its audit comment is attributed to %s.",
		reviewAttempt.Result.Review.ReviewID, result.Publication.CommitID,
		result.Publication.PullRequestNumber, starter.forgejoAuthorFor(run, worker.RoleLead),
	)
	if result.Publication.CommitID == reviewAttempt.Result.Review.CommitID {
		verified = fmt.Sprintf(
			"Verified lead response to review #%d: it changes nothing, answering every finding as already fixed, declined, or out of scope; commit %s stays the head of Forgejo pull request #%d and its audit comment is attributed to %s.",
			reviewAttempt.Result.Review.ReviewID, result.Publication.CommitID,
			result.Publication.PullRequestNumber, starter.forgejoAuthorFor(run, worker.RoleLead),
		)
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, request.identity.AttemptID+":response-verified", request.identity.SessionID,
		worker.Event{Type: worker.EventActivity, Text: verified},
	); err != nil {
		return fmt.Errorf("record verified implementation review response: %w", err)
	}
	if implementationReviewRoundLimitReached(run.ImplementationReviewRoundLimit, round) {
		return starter.waitRun(ctx, request.runID, implementationReviewLimitReason(run.ImplementationReviewRoundLimit), execution.RunWaitKindRoundCap)
	}
	if err := starter.resetAcceptanceTests(
		ctx, run, result.Publication.CommitID, request.identity.AttemptID+":reset-acceptance-tests",
	); err != nil {
		return err
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

// resetAcceptanceTests pins a run's independent acceptance tests to a new
// implementation commit and sets them pending again, so the next review runs
// them against that commit. Tests marked not applicable stay so.
func (starter *RemoteLeadStarter) resetAcceptanceTests(
	ctx context.Context,
	run execution.Run,
	commitID string,
	idempotencyKey string,
) error {
	if !run.IndependentAcceptanceTests {
		return nil
	}
	artifact, err := starter.artifacts.GetFeatureArtifact(ctx, run.FeatureID, featureartifact.KindAcceptanceTests)
	if err != nil {
		return err
	}
	tests := featureartifact.AcceptanceTests{}
	if err := json.Unmarshal([]byte(artifact.Document), &tests); err != nil {
		return err
	}
	tests.ImplementationCommitID = commitID
	for index := range tests.Tests {
		if tests.Tests[index].Status != featureartifact.AcceptanceTestNotApplicable {
			tests.Tests[index].Status = featureartifact.AcceptanceTestPending
			tests.Tests[index].Note = ""
		}
	}
	acceptanceArtifacts, ok := starter.artifacts.(remoteLeadAcceptanceArtifactService)
	if !ok {
		return errors.New("acceptance-test artifact service is unavailable")
	}
	_, err = acceptanceArtifacts.UpsertAcceptanceTests(
		ctx, run.FeatureID, tests,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
		idempotencyKey,
	)
	return err
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
		return fmt.Errorf("reviewer completed without structured review facts: %w: return action 'approved' or "+
			"'changes_requested' with your summary, the reviewed commit in commit_id, pull_request_number, and "+
			"the posted review's ID in review_id", errReviewResult)
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
	if refusal, err := starter.acceptanceReviewRefusal(ctx, run, result); err != nil {
		return err
	} else if refusal != nil {
		return refusal
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
		return fmt.Errorf("verify reviewer publication: %w", rejectPublication(err))
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

func validateAcceptanceReviewResults(
	tests featureartifact.AcceptanceTests,
	planVersion int,
	commitID string,
	disposition workerhttp.Disposition,
) error {
	if err := tests.Validate(); err != nil {
		return err
	}
	if tests.PlanVersion != planVersion {
		return errors.New("acceptance results do not describe the reviewed implementation commit")
	}
	if tests.ImplementationCommitID != commitID {
		return fmt.Errorf("%w: the review is of commit %s but the acceptance tests ran against commit %s; "+
			"review commit %s, the one the tests ran against, and report it in commit_id",
			errAcceptanceResultsCommit, commitID, tests.ImplementationCommitID, tests.ImplementationCommitID)
	}
	for _, test := range tests.Tests {
		switch test.Status {
		case featureartifact.AcceptanceTestPending, featureartifact.AcceptanceTestRunning:
			return acceptanceResultsIncomplete(fmt.Sprintf("acceptance test %q has no final result", test.ID))
		case featureartifact.AcceptanceTestFailed:
			if disposition == workerhttp.DispositionSucceeded {
				return acceptanceResultsIncomplete(fmt.Sprintf(
					"the reviewer approved while its acceptance test %q is recorded as failed; the reviewer must run the test again "+
						"if the test was wrong, or request changes if the implementation is", test.ID))
			}
		}
	}
	return nil
}

// errAcceptanceResultsCommit is a review of another commit than the one the
// acceptance tests ran against.
var errAcceptanceResultsCommit = agentRejection("acceptance results do not describe the reviewed implementation commit")

// errAcceptanceResultsIncomplete is the check behind acceptanceResultsIncomplete.
var errAcceptanceResultsIncomplete = agentRejection("the review's acceptance results are incomplete")

// acceptanceResultsIncomplete refuses a review whose acceptance results only
// the reviewer can put right, by recording them for the reviewed commit.
// Checking the same review again can never pass.
type acceptanceResultsIncomplete string

func (refusal acceptanceResultsIncomplete) Error() string { return string(refusal) }

func (refusal acceptanceResultsIncomplete) Unwrap() error { return errAcceptanceResultsIncomplete }

// acceptanceReviewRefusal checks a finished review against the acceptance
// results recorded for its commit. It returns why the review cannot be
// accepted, or a separate error when the results cannot be read.
func (starter *RemoteLeadStarter) acceptanceReviewRefusal(
	ctx context.Context,
	run execution.Run,
	result workerhttp.TerminalResult,
) (refusal error, err error) {
	if !run.IndependentAcceptanceTests || result.Review == nil {
		return nil, nil
	}
	artifact, err := starter.artifacts.GetFeatureArtifact(ctx, run.FeatureID, featureartifact.KindAcceptanceTests)
	if err != nil {
		return nil, fmt.Errorf("load acceptance results before review verification: %w", err)
	}
	tests := featureartifact.AcceptanceTests{}
	if err := json.Unmarshal([]byte(artifact.Document), &tests); err != nil {
		return nil, fmt.Errorf("decode acceptance results before review verification: %w", err)
	}
	return validateAcceptanceReviewResults(tests, run.PlanVersion, result.Review.CommitID, result.Disposition), nil
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
		lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) || lead.Role != worker.RoleLead ||
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
	} else if updated, err := starter.baseUpdatePrecedesReview(ctx, run, lead, checkpoint, review); err != nil {
		return remoteLeadRequest{}, false, err
	} else if updated {
		// The approval was of the lead's update onto main (ADR-025).
		priorRound = round - 1
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
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: implementationReadinessInstructions(
				prepared, review.Summary,
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

func (starter *RemoteLeadStarter) launchImplementationReadinessVerification(
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) {
	defer starter.release(request.runID)
	if err := starter.verifyImplementationReadiness(starter.lifetime, request, result); err != nil {
		starter.requireReview(starter.lifetime, request, err)
	}
}

// enterMergeGate records the exact commit to merge, moves the work order to
// ready_to_merge, and either waits at the merge gate or merges by policy,
// after isolated validation when the project has it. Reviewed runs enter it
// after the lead's readiness acknowledgement; runs without a reviewer
// (ADR-018) after the lead's verified publication.
func (starter *RemoteLeadStarter) enterMergeGate(
	ctx context.Context,
	request remoteLeadRequest,
	run execution.Run,
	storedFeature feature.Feature,
	commitID string,
	pullRequestNumber int64,
	readyReason string,
) error {
	mergeReady, err := starter.workspaces.RecordMergeReady(
		ctx, storedFeature.ProjectID, storedFeature.ID, commitID, pullRequestNumber,
	)
	if err != nil {
		return fmt.Errorf("record exact merge target: %w", err)
	}
	if _, err := starter.planning.TransitionFeature(
		ctx, storedFeature.ID, feature.StateReadyToMerge,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
		request.identity.AttemptID+":ready-to-merge",
	); err != nil {
		return fmt.Errorf("advance approved implementation to ready to merge: %w", err)
	}
	if paused, err := starter.stopAtPauseBoundary(ctx, run.ID, readyReason); err != nil || paused {
		return err
	}
	if handled, err := starter.previewCheckGate(ctx, run, storedFeature, commitID, true, RecoveryDirective{}); err != nil || handled {
		return err
	}
	if starter.validation != nil {
		job, _, err := starter.validation.EnsureJob(
			ctx, storedFeature.ProjectID, storedFeature.ID, run.ID, mergeReady.ID, commitID,
		)
		if err != nil && !errors.Is(err, validation.ErrNotFound) {
			return fmt.Errorf("create isolated validation job: %w", err)
		}
		if err == nil {
			if _, err := starter.executions.RecordSessionEventWithID(
				ctx, request.identity.AttemptID+":validation-created", request.identity.SessionID,
				worker.Event{Type: worker.EventActivity, Text: "Isolated validation job " + job.ID + " is pending for approved commit " + commitID + "."},
			); err != nil {
				return fmt.Errorf("record validation activity: %w", err)
			}
			return starter.waitRun(ctx, request.runID,
				validationPendingReason,
				execution.RunWaitKindMergeGate,
			)
		}
	}
	if run.MergePolicy == project.MergePolicyAutoAfterGates {
		return starter.mergeAfterGates(ctx, run, true)
	}
	return starter.waitRun(ctx, request.runID, readyReason, execution.RunWaitKindMergeGate)
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
		return fmt.Errorf("lead merge-readiness acknowledgement has an invalid decision: %w: found %q; return "+
			"action 'ready_to_merge' when no material blocker remains, or 'concern' with the remaining concern",
			errReadinessDecision, result.Disposition)
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
		return fmt.Errorf("verify lead merge-readiness decision: %w", rejectPublication(err))
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
		return starter.enterMergeGate(
			ctx, request, run, storedFeature, approved.CommitID, approved.PullRequestNumber,
			implementationApprovedReason,
		)
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

// continueSubmittedPlan moves a submitted plan forward. The reviewer approves
// the exact final plan before it is published and implementation can start;
// a request for changes returns the discussion to the lead. The reviewer's
// approval attempt is the durable record, so recovery resumes at the right
// point.
func (starter *RemoteLeadStarter) continueSubmittedPlan(
	ctx context.Context,
	runID string,
	plan execution.Event,
) error {
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return err
	}
	if len(messages) == 0 || messages[len(messages)-1].Event.ID != plan.ID ||
		messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return fmt.Errorf("%w: the submitted plan is no longer the latest planning message", ErrPlanningNotAllowed)
	}
	published, err := starter.planPublicationRecorded(ctx, plan)
	if err != nil {
		return err
	}
	if published || run.Steps.SkipReviewer {
		return starter.publishSubmittedPlan(ctx, runID, plan)
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return err
	}
	attemptID := planApprovalAttemptForVersion(reviewer.ID, run.PlanVersion, planSubmissionCount(messages))
	if checkpoint.AttemptID == attemptID {
		attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: attemptID})
		if err != nil {
			return err
		}
		if err := validateCompletedTurn(reviewer, checkpoint, attempt); err != nil {
			return err
		}
		switch attempt.Result.Disposition {
		case workerhttp.DispositionSucceeded:
			return starter.publishSubmittedPlan(ctx, runID, plan)
		case workerhttp.DispositionChangesRequested:
			return starter.returnPlanToLead(ctx, run, reviewer.ID, attemptID)
		default:
			return fmt.Errorf("%w: plan approval ended with disposition %q: %w: return action 'approve' with a "+
				"one-line confirmation, or 'request_changes' with exactly what differs from what you agreed",
				ErrPlanningNotAllowed, attempt.Result.Disposition, errPlanApprovalDecision)
		}
	}
	if run.Paused {
		return starter.waitRun(ctx, run.ID, planApprovalReason, execution.RunWaitKindPhaseCheckpoint)
	}
	if reviewer.Status != execution.SessionStatusWaitingForUser || reviewer.ProviderSessionID == "" ||
		reviewer.AgentID != agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer) || reviewer.Role != worker.RoleReviewer {
		return fmt.Errorf("%w: reviewer cannot approve the plan now", ErrPlanningNotAllowed)
	}
	if err := starter.confirmCompletedTurn(ctx, reviewer, checkpoint); err != nil {
		return err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	prepared, err := starter.workspaceForCurrentPlan(ctx, run, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return err
	}
	if !prepared.CheckoutReady() {
		return fmt.Errorf("%w: planning checkout is not ready", ErrPlanningNotAllowed)
	}
	request := remoteLeadRequest{
		runID: run.ID, agentName: "reviewer",
		waitingReason: planApprovalReason,
		planningStage: planningStagePlanApproval,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer),
				ProjectID:      storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleReviewer, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: reviewer.ProviderSessionID,
			Instructions:      planApprovalInstructions(plan.Text, leadParticipant(run.AgentProviders)),
			OutputContract:    workerhttp.OutputContractPlanApproval,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	admitted, err := starter.executions.BeginChainedTurn(ctx, reviewer.ID, checkpoint, attemptID, planApprovalReason)
	if err != nil {
		return err
	}
	if admitted {
		starter.launchAdmitted(request)
	}
	return nil
}

// continueApprovedPlanning runs after the reviewer's approval turn finishes.
func (starter *RemoteLeadStarter) continueApprovedPlanning(ctx context.Context, request remoteLeadRequest) {
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		starter.requireReview(ctx, request, err)
		return
	}
	if len(messages) == 0 || messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		starter.requireReview(ctx, request, errors.New("plan approval has no submitted plan"))
		return
	}
	plan := messages[len(messages)-1].Event
	if err := starter.continueSubmittedPlan(ctx, request.runID, plan); err != nil {
		starter.requirePlanPublicationReview(ctx, request.runID, plan, err)
	}
}

// returnPlanToLead records the reviewer's requested changes as a planning
// message, so the lead revises and resubmits within the normal round limit.
func (starter *RemoteLeadStarter) returnPlanToLead(
	ctx context.Context,
	run execution.Run,
	reviewerSessionID string,
	attemptID string,
) error {
	message, err := starter.messageForAttempt(ctx, reviewerSessionID, attemptID)
	if err != nil {
		return err
	}
	if _, _, err := starter.executions.LinkPlanningMessage(ctx, run.ID, message.ID); err != nil {
		return err
	}
	if run.Paused {
		return starter.waitRun(ctx, run.ID, "The reviewer asked for changes to the final plan.", execution.RunWaitKindPhaseCheckpoint)
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return err
	}
	if planningRoundLimitReached(run.PlanningRoundLimit, len(messages)) {
		return starter.waitRun(ctx, run.ID, planningLimitReason(run.PlanningRoundLimit), execution.RunWaitKindRoundCap)
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	next, admitted, err := starter.startLeadResponse(ctx, run, storedFeature, messages, true)
	if err != nil {
		return err
	}
	if admitted {
		starter.launchAdmitted(next)
	}
	return nil
}

const planApprovalReason = "The reviewer is checking the final plan before implementation."

func planApprovalAttemptForVersion(sessionID string, version int, submission int) string {
	return workflowAttemptForVersion(sessionID, "approval", version, submission)
}

func planApprovalVersionAndTurn(sessionID, attemptID string) (int, int, bool) {
	return workflowAttemptVersionAndTurn(sessionID, "approval", attemptID)
}

// planSubmissionCount numbers the lead's plan submissions in this plan
// version, so each resubmission gets its own approval attempt.
func planSubmissionCount(messages []execution.PlanningMessage) int {
	count := 0
	for _, message := range messages {
		if message.Role == worker.RoleLead && message.Event.Type == worker.EventPlanSubmitted {
			count++
		}
	}
	return count
}

func (starter *RemoteLeadStarter) launchPlanPublication(
	runID string,
	plan execution.Event,
) {
	ctx := starter.lifetime
	if err := starter.continueSubmittedPlan(ctx, runID, plan); err != nil {
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
	if _, err := starter.recordPlanPublication(ctx, run, storedFeature, plan); err != nil {
		return err
	}
	return starter.waitRun(ctx, runID, planningPlanPublishedReason, execution.RunWaitKindPhaseCheckpoint)
}

// recordPlanPublication publishes the run's plan to the draft pull request,
// creating the feature branch and pull request for the first plan.
func (starter *RemoteLeadStarter) recordPlanPublication(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	plan execution.Event,
) (workspace.Workspace, error) {
	var err error
	var prepared workspace.Workspace
	if run.PlanVersion == 1 {
		prepared, _, err = starter.workspaces.PublishPlan(
			ctx, storedFeature.ProjectID, run.FeatureID, plan.ID, plan.Text,
		)
	} else {
		revision, revisionErr := starter.executions.GetPlanRevision(ctx, run.ID, run.PlanVersion)
		if revisionErr != nil {
			return workspace.Workspace{}, revisionErr
		}
		prepared, _, err = starter.workspaces.PublishRevisedPlan(
			ctx, storedFeature.ProjectID, run.FeatureID, plan.ID, plan.Text,
			revision.BaselineCommitID,
		)
	}
	if err != nil {
		return workspace.Workspace{}, err
	}
	activity := "The agreed implementation plan was published to Forgejo pull request #%d."
	if run.Steps.SkipPlan {
		activity = "Forgejo pull request #%d was opened for this work order, without a documented plan."
	}
	if storedFeature.Kind == feature.KindSession {
		activity = "Forgejo pull request #%d was opened for this session."
	}
	_, err = starter.executions.RecordSessionEventWithID(
		ctx, planPublicationEventID(plan), plan.SessionID,
		worker.Event{Type: worker.EventActivity, Text: fmt.Sprintf(activity, prepared.PullRequestNumber)},
	)
	return prepared, err
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
	starter.noteBlocker(ctx, runID, execution.BlockerCheck, "plan publication",
		planningPublicationReviewReason, planningPublicationReviewReason)
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

// pauseFailedTurn keeps a work order recoverable when a provider turn fails
// (ADR-017): the provider's error is recorded, the session stays active, and
// the run waits as a blocker. Continuing retries the turn in a fenced
// successor attempt in the same conversation.
func (starter *RemoteLeadStarter) pauseFailedTurn(
	ctx context.Context,
	request remoteLeadRequest,
	session execution.Session,
	attempt workerhttp.Attempt,
) error {
	cause := strings.TrimSpace(attempt.Result.Summary)
	if attempt.Result.Error != nil && strings.TrimSpace(attempt.Result.Error.Message) != "" {
		cause = strings.TrimSpace(attempt.Result.Error.Message)
	}
	if cause == "" {
		cause = "the provider returned no reason"
	}
	if len(cause) > 500 {
		cause = cause[:500] + "…"
	}
	reason := "The " + request.agentName + "'s turn failed: " + cause +
		". The work order is paused; continue to retry the turn in the same conversation."
	retrying := false
	// A login failure may pass: a refresh that failed once can work on the
	// next turn. It retries like an interruption before it asks for a sign-in.
	login := attempt.Result.Error != nil && attempt.Result.Error.Code == workerhttp.ErrorProviderLoginRequired
	interrupted := providerInterruption(attempt.Result)
	if login {
		interrupted = "the provider did not accept the agent's login"
		// An agent already known to need a sign-in waits at once.
		if starter.agentNeedsSignIn(ctx, attempt.Assignment.AgentProfileID) {
			interrupted = ""
		}
	}
	if interrupted != "" {
		if scheduled, spent, ok := starter.scheduleRetry(ctx, request, interrupted); ok {
			reason, retrying = scheduled, true
		} else {
			reason += spentRetries(spent)
		}
	} else {
		starter.turnEnded(ctx, request.runID, request.identity.SessionID)
	}
	if login && !retrying {
		run, err := starter.executions.GetRun(ctx, request.runID)
		if err != nil {
			return err
		}
		agent := agentForSession(run, session.Role)
		reason = "The " + request.agentName + "'s agent " + agent + " must log in again: " + cause +
			". The work order continues once the agent is connected."
		if waits, ok := starter.executions.(RemoteLeadLoginWaits); ok {
			if err := waits.RecordLoginWait(ctx, run.ID, agent, session.Role); err != nil {
				return err
			}
		}
		starter.markNeedsSignIn(ctx, agent, cause)
	}
	if attempt.Result.Error != nil && attempt.Result.Error.Code == workerhttp.ErrorUsageLimit {
		run, err := starter.executions.GetRun(ctx, request.runID)
		if err != nil {
			return err
		}
		agent := agentForSession(run, session.Role)
		reason = usageLimitReason(agent, attempt.Result.Error.ResetsAt)
		if waits, ok := starter.executions.(remoteLeadLimitWaits); ok {
			if err := waits.RecordLimitWait(ctx, run.ID, agent, session.Role, attempt.Result.Error.ResetsAt); err != nil {
				return err
			}
		}
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, session.ID+":"+attempt.AttemptID+":turn-failed", session.ID,
		worker.Event{
			Type: worker.EventRecoveryAssessment, Text: reason,
			RecoveryAssessment: &worker.RecoveryAssessment{Consistent: true, RequiresUserReview: !retrying},
		},
	); err != nil {
		return err
	}
	if session.Status == execution.SessionStatusStarting {
		next, err := starter.executions.TransitionSession(
			ctx, session.ID, session.Status, execution.SessionStatusRunning, attempt.ProviderSessionID,
		)
		if err != nil {
			return err
		}
		session = next
	}
	if session.Status == execution.SessionStatusRunning {
		if _, err := starter.executions.TransitionSession(
			ctx, session.ID, session.Status, execution.SessionStatusPauseRequested, session.ProviderSessionID,
		); err != nil {
			return err
		}
	}
	kind := execution.BlockerAgent
	switch {
	case attempt.Result.Error != nil && attempt.Result.Error.Code == workerhttp.ErrorUsageLimit:
		kind = execution.BlockerLimit
	case attempt.Result.Error != nil && attempt.Result.Error.Code == workerhttp.ErrorProviderLoginRequired,
		providerInterruption(attempt.Result) != "":
		kind = execution.BlockerEnvironment
	}
	starter.noteBlocker(ctx, request.runID, kind, "", cause, reason)
	return starter.waitRun(ctx, request.runID, reason, execution.RunWaitKindBlocker)
}

func (starter *RemoteLeadStarter) requireReview(
	ctx context.Context,
	request remoteLeadRequest,
	cause error,
) {
	starter.reportError(fmt.Errorf("observe agent run %q: %w", request.runID, cause))
	retries := 0
	if interrupted := interruption(cause); interrupted != "" {
		reason, spent, scheduled := starter.scheduleRetry(ctx, request, interrupted)
		if scheduled {
			starter.awaitRetry(ctx, request, reason, spent+1)
			return
		}
		retries = spent
	} else {
		starter.turnEnded(ctx, request.runID, request.identity.SessionID)
	}
	text := "The coordinator could not complete the " + request.agentName + " turn. " +
		"The work order is paused so its durable work can be checked and continued safely." +
		spentRetries(retries)
	// What failed, so the user (and the assistant) can tell the agent what to fix.
	if detail := strings.TrimSpace(cause.Error()); detail != "" {
		if len(detail) > 400 {
			detail = detail[:400] + "…"
		}
		text += " What failed: " + detail + "."
	}
	kind, check := classifyCause(cause)
	starter.noteBlockerFingerprinted(ctx, request.runID, kind, check, cause.Error(), blockerFingerprintDetail(cause), text)
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
		starter.noteBlocker(ctx, request.runID, execution.BlockerAgent, "", result.Summary, result.Summary)
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

// ResumeAfterEnvironmentRejection tells the lead that the packages it asked
// for were declined and gets it going again. A lead resting on the turn that
// asked gets the message as its next turn, as any reply. Otherwise the run is
// blocked on something it cannot take a reply for (a later turn that failed,
// or a turn of another kind), and the message goes with the user's Continue
// instead, so a rejection never leaves the lead stranded. A run that is not
// blocked (working again, paused by the user, or past the request) is not
// waiting on it and needs nothing.
func (starter *RemoteLeadStarter) ResumeAfterEnvironmentRejection(
	ctx context.Context,
	request projectenvironment.Request,
	message string,
) error {
	commandID := request.ID + ":rejected"
	_, err := starter.SendCommand(ctx, request.SessionID, worker.Command{
		ID: commandID, Type: worker.CommandMessage, Message: message,
	})
	if err == nil || !errors.Is(err, ErrCommandNotAllowed) {
		return err
	}
	run, getErr := starter.executions.GetRun(ctx, request.RunID)
	if getErr != nil {
		return errors.Join(err, getErr)
	}
	if run.Status != execution.RunStatusWaitingForUser || run.Paused ||
		run.WaitKind != execution.RunWaitKindBlocker {
		return nil
	}
	_, admitted, recoverErr := starter.RecoverBlockerWithDirective(ctx, run.ID, commandID, RecoveryDirective{
		Action: RecoveryActionContinue, Message: message,
	})
	if recoverErr != nil {
		return recoverErr
	}
	if !admitted {
		return err
	}
	return nil
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
	if waitKind == execution.RunWaitKindBlocker {
		starter.noteUnnamedBlocker(ctx, runID, reason)
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

const (
	validationPendingReason = "The exact approved revision is waiting for isolated validation before merge."
	validationPassedReason  = "Isolated validation passed. The approved revision is ready to merge."
)

// ValidationPassed moves a run waiting on isolated validation at the merge
// gate to ready, so its reason no longer says validation is pending.
func (starter *RemoteLeadStarter) ValidationPassed(ctx context.Context, runID string) error {
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != execution.RunStatusWaitingForUser || run.Paused ||
		run.WaitKind != execution.RunWaitKindMergeGate || run.Reason != validationPendingReason {
		return nil
	}
	return starter.waitRun(ctx, runID, validationPassedReason, execution.RunWaitKindMergeGate)
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
