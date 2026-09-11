package httpapi

import (
	"context"
	"io"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type ProjectService interface {
	Create(ctx context.Context, name string, recoveryPolicy project.RecoveryPolicy, dialogueLimits project.DialogueLimits, agentProviders project.AgentProviders, mergePolicy project.MergePolicy) (project.Project, error)
	GetByID(ctx context.Context, id string) (project.Project, error)
	List(ctx context.Context) ([]project.Project, error)
	UpdateDialogueLimits(ctx context.Context, projectID string, limits project.DialogueLimits) (project.Project, error)
	UpdateAgentProviders(ctx context.Context, projectID string, providers project.AgentProviders) (project.Project, error)
	UpdateMergePolicy(ctx context.Context, projectID string, policy project.MergePolicy) (project.Project, error)
	BindForgejoRepository(ctx context.Context, projectID, owner, name string) (project.Project, error)
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
	Prepare(
		ctx context.Context,
		projectID string,
		featureID string,
	) (workspace.Workspace, bool, error)
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
}

type API struct {
	projects        ProjectService
	projectImporter ProjectImporter
	features        FeatureService
	workflow        WorkflowService
	execution       ExecutionService
	controller      SessionController
	starter         RunStarter
	workspaces      WorkspaceService
	realWorkflow    RealWorkflowStarter
}

func New(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
	starter RunStarter,
) http.Handler {
	return newAPI(projects, features, workflow, executionService, controller, starter, nil, nil)
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
		projects, features, workflow, executionService, controller, starter, workspaces, nil,
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
		workspaces, realWorkflow,
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
) http.Handler {
	api := &API{
		projects:     projects,
		features:     features,
		workflow:     workflow,
		execution:    executionService,
		controller:   controller,
		starter:      starter,
		workspaces:   workspaces,
		realWorkflow: realWorkflow,
	}
	api.projectImporter, _ = projects.(ProjectImporter)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.healthHandler)
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
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/dialogue-limits",
		api.updateProjectDialogueLimitsHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/agent-providers",
		api.updateProjectAgentProvidersHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/merge-policy",
		api.updateProjectMergePolicyHandler,
	)
	mux.HandleFunc(
		"PUT /api/v1/projects/{id}/forgejo-repository",
		api.bindForgejoRepositoryHandler,
	)
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
			"GET /api/v1/projects/{projectID}/features/{id}/workspace",
			api.getWorkspaceHandler,
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
