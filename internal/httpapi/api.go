package httpapi

import (
	"context"
	"io"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectdeletion"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type ProjectService interface {
	Create(ctx context.Context, name string, recoveryPolicy project.RecoveryPolicy, dialogueLimits project.DialogueLimits, agentProviders project.AgentProviders, mergePolicy project.MergePolicy, autonomyPolicy ...project.AutonomyPolicy) (project.Project, error)
	GetByID(ctx context.Context, id string) (project.Project, error)
	List(ctx context.Context) ([]project.Project, error)
	UpdateDialogueLimits(ctx context.Context, projectID string, limits project.DialogueLimits) (project.Project, error)
	UpdateAgentProviders(ctx context.Context, projectID string, providers project.AgentProviders) (project.Project, error)
	UpdateMergePolicy(ctx context.Context, projectID string, policy project.MergePolicy) (project.Project, error)
	UpdateAutonomyPolicy(ctx context.Context, projectID string, policy project.AutonomyPolicy) (project.Project, error)
	BindForgejoRepository(ctx context.Context, projectID, owner, name string) (project.Project, error)
	GetRepositoryOverview(ctx context.Context, projectID string) (project.RepositoryOverview, error)
}

type ProjectAgentModelCreator interface {
	CreateWithAgentModels(ctx context.Context, name string, recoveryPolicy project.RecoveryPolicy, dialogueLimits project.DialogueLimits, agentProviders project.AgentProviders, agentModels project.AgentModels, mergePolicy project.MergePolicy, autonomyPolicy ...project.AutonomyPolicy) (project.Project, error)
}

type ProvisionedProjectCreator interface {
	CreateProvisionedWithAgentModels(
		ctx context.Context,
		requestKey string,
		name string,
		recoveryPolicy project.RecoveryPolicy,
		dialogueLimits project.DialogueLimits,
		agentProviders project.AgentProviders,
		agentModels project.AgentModels,
		mergePolicy project.MergePolicy,
		autonomyPolicy ...project.AutonomyPolicy,
	) (project.Project, error)
}

type ProjectRepositoryProvisioner interface {
	ProvisionForgejoRepository(ctx context.Context, projectID string) (project.Project, error)
}

type ProjectAgentSettingsUpdater interface {
	UpdateAgentSettings(ctx context.Context, projectID string, providers project.AgentProviders, models project.AgentModels) (project.Project, error)
}

type ProjectImporter interface {
	Import(ctx context.Context, spec project.ImportSpec, bundle io.Reader) (project.Project, bool, error)
}

type FeatureService interface {
	Create(
		ctx context.Context,
		projectID string,
		title string,
		description string,
		overrides feature.SettingsOverrides,
	) (feature.Feature, error)
	GetByID(
		ctx context.Context,
		projectID string,
		id string,
	) (feature.Feature, error)
	List(ctx context.Context, projectID string) ([]feature.Feature, error)
}

type WorkflowService interface {
	TransitionFeature(
		ctx context.Context,
		featureID string,
		state feature.State,
		actor workflow.Actor,
		idempotencyKey string,
	) (workflow.Event, error)
	EventsForFeature(
		ctx context.Context,
		featureID string,
	) ([]workflow.Event, error)
	SubscribeFeatureEvents(featureID string) (<-chan workflow.Event, func())
}

type ExecutionService interface {
	GetRun(ctx context.Context, id string) (execution.Run, error)
	RunsForFeature(ctx context.Context, featureID string) ([]execution.Run, error)
	SessionsForRun(ctx context.Context, runID string) ([]execution.Session, error)
	GetSession(ctx context.Context, id string) (execution.Session, error)
	EventsForSession(ctx context.Context, sessionID string) ([]execution.Event, error)
	SubscribeSessionEvents(sessionID string) (<-chan execution.Event, func())
	PlanningMessagesForRun(ctx context.Context, runID string) ([]execution.PlanningMessage, error)
	SubscribePlanningMessages(runID string) (<-chan execution.PlanningMessage, func())
	GetLatestIntervention(ctx context.Context, runID string) (execution.Intervention, error)
	InterventionTargetsForRun(ctx context.Context, runID string) ([]execution.InterventionTarget, error)
}

type RunStarter interface {
	Start(
		ctx context.Context,
		runID string,
		projectID string,
		featureID string,
		goal string,
		dialogueLimits project.DialogueLimits,
		agentProviders project.AgentProviders,
		mergePolicy project.MergePolicy,
		autonomyPolicy ...project.AutonomyPolicy,
	) (execution.Run, bool, error)
}

type ModelRunStarter interface {
	StartWithModels(
		ctx context.Context,
		runID string,
		projectID string,
		featureID string,
		goal string,
		dialogueLimits project.DialogueLimits,
		agentProviders project.AgentProviders,
		agentModels project.AgentModels,
		mergePolicy project.MergePolicy,
		autonomyPolicy ...project.AutonomyPolicy,
	) (execution.Run, bool, error)
}

type SessionController interface {
	SendCommand(
		ctx context.Context,
		sessionID string,
		command worker.Command,
	) (execution.Command, error)
	AcceptGoal(
		ctx context.Context,
		sessionID string,
		goal string,
		actor workflow.Actor,
		idempotencyKey string,
	) (workflow.Event, error)
}

type WorkspaceService interface {
	Get(ctx context.Context, projectID, featureID string) (workspace.Workspace, error)
	GetCompletedHandoff(ctx context.Context, projectID, featureID string) (workspace.Workspace, error)
	GetProjectHandoff(ctx context.Context, projectID string) (workspace.ProjectHandoff, error)
	Prepare(
		ctx context.Context,
		projectID string,
		featureID string,
	) (workspace.Workspace, bool, error)
}

type FeatureDeletionService interface {
	Delete(ctx context.Context, projectID, featureID string) (workorder.Result, error)
}

type ProjectDeletionService interface {
	Delete(ctx context.Context, projectID, idempotencyKey string, force bool) (projectdeletion.Result, error)
}

type ModelCatalogService interface {
	List(context.Context) []modelcatalog.Catalog
	Refresh(context.Context) ([]modelcatalog.Catalog, error)
	ValidateSelection(context.Context, project.AgentProviders, project.AgentModels) error
}

type ToolchainService interface {
	Get(context.Context, string) (toolchain.Manifest, error)
	Configure(context.Context, string, toolchain.Manifest) (toolchain.Manifest, error)
	Detect(context.Context, string) (toolchain.Suggestion, error)
}

type ToolchainAssistantService interface {
	Start(context.Context, string, project.AgentProvider, string, string, toolchain.AssistantPurpose, string) (toolchain.AssistantSession, bool, error)
	Get(context.Context, string, string) (toolchain.AssistantSession, error)
	Reply(context.Context, string, string, string, string) (toolchain.AssistantSession, bool, error)
	Apply(context.Context, string, string) (toolchain.Manifest, error)
}

type RealWorkflowStarter interface {
	StartPlanning(
		ctx context.Context,
		runID string,
		idempotencyKey string,
	) (execution.Run, bool, error)
	StartPlanningReview(
		ctx context.Context,
		runID string,
		idempotencyKey string,
	) (execution.Run, bool, error)
	StartPlanningRound(
		ctx context.Context,
		runID string,
		idempotencyKey string,
	) (execution.Run, bool, error)
	StartImplementation(
		ctx context.Context,
		runID string,
		idempotencyKey string,
	) (execution.Run, bool, error)
	Merge(ctx context.Context, runID string, idempotencyKey string) (execution.Run, bool, error)
	Pause(ctx context.Context, runID string, actionID string) (execution.Run, bool, error)
	Resume(ctx context.Context, runID string, actionID string) (execution.Run, bool, error)
	RecoverBlocker(ctx context.Context, runID string, actionID string) (execution.Run, bool, error)
	QueueIntervention(ctx context.Context, runID, interventionID string, target worker.Role, message string) (execution.Intervention, bool, error)
}

type API struct {
	projects           ProjectService
	projectImporter    ProjectImporter
	features           FeatureService
	workflow           WorkflowService
	execution          ExecutionService
	controller         SessionController
	starter            RunStarter
	workspaces         WorkspaceService
	realWorkflow       RealWorkflowStarter
	featureDeletion    FeatureDeletionService
	projectDeletion    ProjectDeletionService
	modelCatalog       ModelCatalogService
	toolchains         ToolchainService
	toolchainAssistant ToolchainAssistantService
}

func New(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
	starter RunStarter,
) http.Handler {
	return newAPI(projects, features, workflow, executionService, controller, starter, nil, nil, nil, nil)
}

func NewWithWorkspaceService(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
	starter RunStarter,
	workspaces WorkspaceService,
) http.Handler {
	return newAPI(
		projects, features, workflow, executionService, controller, starter, workspaces, nil, nil, nil,
	)
}

func NewWithWorkspaceAndRealWorkflowService(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
	starter RunStarter,
	workspaces WorkspaceService,
	realWorkflow RealWorkflowStarter,
) http.Handler {
	return newAPI(
		projects, features, workflow, executionService, controller, starter,
		workspaces, realWorkflow, nil, nil,
	)
}

func NewWithWorkspaceRealWorkflowAndDeletionService(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
	starter RunStarter,
	workspaces WorkspaceService,
	realWorkflow RealWorkflowStarter,
	featureDeletion FeatureDeletionService,
) http.Handler {
	return newAPI(
		projects, features, workflow, executionService, controller, starter,
		workspaces, realWorkflow, featureDeletion, nil,
	)
}

func NewWithWorkspaceRealWorkflowDeletionAndModels(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
	starter RunStarter,
	workspaces WorkspaceService,
	realWorkflow RealWorkflowStarter,
	featureDeletion FeatureDeletionService,
	modelCatalog ModelCatalogService,
	toolchains ToolchainService,
	toolchainAssistant ToolchainAssistantService,
	additional ...any,
) http.Handler {
	extras := []any{toolchains, toolchainAssistant}
	extras = append(extras, additional...)
	return newAPI(
		projects, features, workflow, executionService, controller, starter,
		workspaces, realWorkflow, featureDeletion, modelCatalog, extras...,
	)
}

func newAPI(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
	starter RunStarter,
	workspaces WorkspaceService,
	realWorkflow RealWorkflowStarter,
	featureDeletion FeatureDeletionService,
	modelCatalog ModelCatalogService,
	extras ...any,
) http.Handler {
	var toolchainService ToolchainService
	var toolchainAssistant ToolchainAssistantService
	var projectDeletion ProjectDeletionService
	for _, extra := range extras {
		switch typed := extra.(type) {
		case ToolchainService:
			toolchainService = typed
		case ToolchainAssistantService:
			toolchainAssistant = typed
		case ProjectDeletionService:
			projectDeletion = typed
		}
	}
	api := &API{
		projects:           projects,
		features:           features,
		workflow:           workflow,
		execution:          executionService,
		controller:         controller,
		starter:            starter,
		workspaces:         workspaces,
		realWorkflow:       realWorkflow,
		featureDeletion:    featureDeletion,
		projectDeletion:    projectDeletion,
		modelCatalog:       modelCatalog,
		toolchains:         toolchainService,
		toolchainAssistant: toolchainAssistant,
	}
	api.projectImporter, _ = projects.(ProjectImporter)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.healthHandler)
	if modelCatalog != nil {
		mux.HandleFunc("GET /api/v1/models", api.listModelsHandler)
		mux.HandleFunc("POST /api/v1/models/refresh", api.refreshModelsHandler)
	}
	if toolchainService != nil {
		mux.HandleFunc("GET /api/v1/toolchain-presets", api.listToolchainPresetsHandler)
		mux.HandleFunc("GET /api/v1/projects/{id}/toolchain", api.getProjectToolchainHandler)
		mux.HandleFunc("PUT /api/v1/projects/{id}/toolchain", api.configureProjectToolchainHandler)
		mux.HandleFunc("POST /api/v1/projects/{id}/toolchain/detect", api.detectProjectToolchainHandler)
	}
	if toolchainAssistant != nil {
		mux.HandleFunc("POST /api/v1/projects/{id}/toolchain/assistant-sessions", api.startToolchainAssistantHandler)
		mux.HandleFunc("GET /api/v1/projects/{id}/toolchain/assistant-sessions/{sessionID}", api.getToolchainAssistantHandler)
		mux.HandleFunc("POST /api/v1/projects/{id}/toolchain/assistant-sessions/{sessionID}/messages", api.replyToolchainAssistantHandler)
		mux.HandleFunc("POST /api/v1/projects/{id}/toolchain/assistant-sessions/{sessionID}/apply", api.applyToolchainAssistantHandler)
	}
	mux.HandleFunc(
		"POST /api/v1/projects",
		api.createProjectHandler,
	)
	if api.projectImporter != nil {
		mux.HandleFunc(
			"PUT /api/v1/project-imports/{importID}",
			api.importProjectHandler,
		)
	}
	mux.HandleFunc(
		"GET /api/v1/projects",
		api.listProjectsHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects/{id}",
		api.getProjectByIDHandler,
	)
	if projectDeletion != nil {
		mux.HandleFunc(
			"DELETE /api/v1/projects/{id}",
			api.deleteProjectHandler,
		)
	}
	mux.HandleFunc(
		"GET /api/v1/projects/{id}/repository-overview",
		api.getProjectRepositoryOverviewHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/dialogue-limits",
		api.updateProjectDialogueLimitsHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/agent-providers",
		api.updateProjectAgentProvidersHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/agent-settings",
		api.updateProjectAgentSettingsHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/merge-policy",
		api.updateProjectMergePolicyHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/autonomy-policy",
		api.updateProjectAutonomyPolicyHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/forgejo-repository",
		api.bindForgejoRepositoryHandler,
	)
	if _, ok := projects.(ProjectRepositoryProvisioner); ok {
		mux.HandleFunc(
			"POST /api/v1/projects/{id}/forgejo-repository",
			api.provisionForgejoRepositoryHandler,
		)
	}
	mux.HandleFunc(
		"POST /api/v1/projects/{projectID}/features",
		api.createFeatureHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects/{projectID}/features",
		api.listFeaturesHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects/{projectID}/features/{id}",
		api.getFeatureByIDHandler,
	)
	if featureDeletion != nil {
		mux.HandleFunc(
			"DELETE /api/v1/projects/{projectID}/features/{id}",
			api.deleteFeatureHandler,
		)
	}
	mux.HandleFunc(
		"POST /api/v1/projects/{projectID}/features/{id}/transitions",
		api.transitionFeatureHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects/{projectID}/features/{id}/events",
		api.getFeatureEventsHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects/{projectID}/features/{id}/events/stream",
		api.streamFeatureEventsHandler,
	)
	mux.HandleFunc(
		"POST /api/v1/projects/{projectID}/features/{id}/runs",
		api.startRunHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects/{projectID}/features/{id}/runs",
		api.listFeatureRunsHandler,
	)
	if workspaces != nil {
		mux.HandleFunc(
			"GET /api/v1/projects/{projectID}/handoff",
			api.getProjectHandoffHandler,
		)
		mux.HandleFunc(
			"GET /api/v1/projects/{projectID}/features/{id}/workspace",
			api.getWorkspaceHandler,
		)
		mux.HandleFunc(
			"GET /api/v1/projects/{projectID}/features/{id}/handoff",
			api.getCompletedHandoffHandler,
		)
		mux.HandleFunc(
			"PUT /api/v1/projects/{projectID}/features/{id}/workspace",
			api.prepareWorkspaceHandler,
		)
	}
	if realWorkflow != nil {
		mux.HandleFunc("POST /api/v1/runs/{id}/planning", api.startPlanningHandler)
		mux.HandleFunc("POST /api/v1/runs/{id}/planning/reviewer", api.startPlanningReviewHandler)
		mux.HandleFunc("POST /api/v1/runs/{id}/planning/round", api.startPlanningRoundHandler)
		mux.HandleFunc("POST /api/v1/runs/{id}/implementation", api.startImplementationHandler)
		mux.HandleFunc("POST /api/v1/runs/{id}/merge", api.mergeRunHandler)
		mux.HandleFunc("POST /api/v1/runs/{id}/pause", api.pauseRunHandler)
		mux.HandleFunc("POST /api/v1/runs/{id}/resume", api.resumeRunHandler)
		mux.HandleFunc("POST /api/v1/runs/{id}/recover", api.recoverRunHandler)
		mux.HandleFunc("POST /api/v1/runs/{id}/interventions", api.queueInterventionHandler)
	}
	mux.HandleFunc("GET /api/v1/runs/{id}", api.getRunHandler)
	mux.HandleFunc("GET /api/v1/runs/{id}/planning/messages", api.getPlanningMessagesHandler)
	mux.HandleFunc("GET /api/v1/runs/{id}/planning/messages/stream", api.streamPlanningMessagesHandler)
	mux.HandleFunc("GET /api/v1/sessions/{id}", api.getSessionHandler)
	mux.HandleFunc("GET /api/v1/sessions/{id}/events", api.getSessionEventsHandler)
	mux.HandleFunc("GET /api/v1/sessions/{id}/events/stream", api.streamSessionEventsHandler)
	mux.HandleFunc("POST /api/v1/sessions/{id}/commands", api.sendSessionCommandHandler)
	mux.HandleFunc("POST /api/v1/sessions/{id}/goal-acceptance", api.acceptGoalHandler)

	return mux
}
