package httpapi

import (
	"context"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type ProjectService interface {
	Create(ctx context.Context, name string, recoveryPolicy project.RecoveryPolicy) (project.Project, error)
	GetByID(ctx context.Context, id string) (project.Project, error)
	List(ctx context.Context) ([]project.Project, error)
	BindForgejoRepository(ctx context.Context, projectID, owner, name string) (project.Project, error)
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
	SessionsForRun(ctx context.Context, runID string) ([]execution.Session, error)
	GetSession(ctx context.Context, id string) (execution.Session, error)
	EventsForSession(ctx context.Context, sessionID string) ([]execution.Event, error)
	SubscribeSessionEvents(sessionID string) (<-chan execution.Event, func())
}

type RunStarter interface {
	Start(
		ctx context.Context,
		runID string,
		projectID string,
		featureID string,
		goal string,
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

type API struct {
	projects   ProjectService
	features   FeatureService
	workflow   WorkflowService
	execution  ExecutionService
	controller SessionController
	starter    RunStarter
	workspaces WorkspaceService
}

func New(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
	starter RunStarter,
) http.Handler {
	return newAPI(projects, features, workflow, executionService, controller, starter, nil)
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
		projects, features, workflow, executionService, controller, starter, workspaces,
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
) http.Handler {
	api := &API{
		projects:   projects,
		features:   features,
		workflow:   workflow,
		execution:  executionService,
		controller: controller,
		starter:    starter,
		workspaces: workspaces,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.healthHandler)
	mux.HandleFunc(
		"POST /api/v1/projects",
		api.createProjectHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects",
		api.listProjectsHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects/{id}",
		api.getProjectByIDHandler,
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
	mux.HandleFunc("GET /api/v1/runs/{id}", api.getRunHandler)
	mux.HandleFunc("GET /api/v1/sessions/{id}", api.getSessionHandler)
	mux.HandleFunc("GET /api/v1/sessions/{id}/events", api.getSessionEventsHandler)
	mux.HandleFunc("GET /api/v1/sessions/{id}/events/stream", api.streamSessionEventsHandler)
	mux.HandleFunc("POST /api/v1/sessions/{id}/commands", api.sendSessionCommandHandler)
	mux.HandleFunc("POST /api/v1/sessions/{id}/goal-acceptance", api.acceptGoalHandler)

	return mux
}
