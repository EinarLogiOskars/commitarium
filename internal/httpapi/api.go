package httpapi

import (
	"context"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type ProjectService interface {
	Create(ctx context.Context, name string) (project.Project, error)
	GetByID(ctx context.Context, id string) (project.Project, error)
}

type API struct {
	projects ProjectService
}

func New(projects ProjectService) http.Handler {
	api := &API{
		projects: projects,
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

	return mux
}
