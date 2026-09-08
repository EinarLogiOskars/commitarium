package httpapi

import (
	"context"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

type ProjectService interface {
	Create(ctx context.Context, name string) (project.Project, error)
	GetByID(ctx context.Context, id string) (project.Project, error)
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
	GetSession(ctx context.Context, id string) (execution.Session, error)
	EventsForSession(ctx context.Context, sessionID string) ([]execution.Event, error)
}

type SessionController interface {
	SendCommand(
		ctx context.Context,
		sessionID string,
		command worker.Command,
	) (execution.Command, error)
}

type API struct {
	projects   ProjectService
	features   FeatureService
	workflow   WorkflowService
	execution  ExecutionService
	controller SessionController
}

func New(
	projects ProjectService,
	features FeatureService,
	workflow WorkflowService,
	executionService ExecutionService,
	controller SessionController,
) http.Handler {
	api := &API{
		projects:   projects,
		features:   features,
		workflow:   workflow,
		execution:  executionService,
		controller: controller,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.healthHandler)
	mux.HandleFunc(
		"POST /api/v1/projects",
		api.createProjectHandler,
	)
	mux.HandleFunc(
		"GET /api/v1/projects/{id}",
		api.getProjectByIDHandler,
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
	mux.HandleFunc("GET /api/v1/sessions/{id}", api.getSessionHandler)
	mux.HandleFunc("GET /api/v1/sessions/{id}/events", api.getSessionEventsHandler)
	mux.HandleFunc("POST /api/v1/sessions/{id}/commands", api.sendSessionCommandHandler)

	return mux
}
