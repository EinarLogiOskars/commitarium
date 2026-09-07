package httpapi

import (
	"context"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type ProjectCreator interface {
	Create(
		ctx context.Context,
		name string,
	) (project.Project, error)
}

type API struct {
	projects ProjectCreator
}

func New(projects ProjectCreator) http.Handler {
	api := &API{
		projects: projects,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", api.healthHandler)
	mux.HandleFunc(
		"POST /api/v1/projects",
		api.createProjectHandler,
	)

	return mux
}
