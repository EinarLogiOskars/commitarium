package httpapi

import (
	"context"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
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

type API struct {
	projects ProjectService
	features FeatureService
}

func New(
	projects ProjectService,
	features FeatureService,
) http.Handler {
	api := &API{
		projects: projects,
		features: features,
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

	return mux
}
