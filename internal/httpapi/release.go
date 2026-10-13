package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/marketplace"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/release"
)

// ReleaseService puts an app on the marketplace (MARKETPLACE_PLAN.md,
// slice 4) and answers its latest release.
type ReleaseService interface {
	Release(ctx context.Context, projectID string) (release.Version, error)
	Status(ctx context.Context, projectID string) (release.Status, error)
}

func (api *API) registerReleaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects/{id}/release", api.getReleaseHandler)
	mux.HandleFunc("POST /api/v1/projects/{id}/release", api.releaseHandler)
}

func (api *API) getReleaseHandler(w http.ResponseWriter, r *http.Request) {
	status, err := api.releases.Status(r.Context(), r.PathValue("id"))
	if err != nil {
		writeReleaseError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status, "release status")
}

func (api *API) releaseHandler(w http.ResponseWriter, r *http.Request) {
	released, err := api.releases.Release(r.Context(), r.PathValue("id"))
	if err != nil {
		writeReleaseError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, released, "release")
}

func writeReleaseError(w http.ResponseWriter, err error) {
	var secret *release.SecretError
	switch {
	case errors.As(err, &secret):
		writeError(w, http.StatusConflict, "release_secret_found",
			"not released: "+secret.Error()+". Remove it from the app and its history first")
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "project not found")
	case errors.Is(err, release.ErrNotAnApp):
		writeError(w, http.StatusConflict, "release_not_an_app", "only apps can be released")
	case errors.Is(err, release.ErrStackRequired):
		writeError(w, http.StatusConflict, "release_stack_required", "set up the app's stack before releasing it")
	case errors.Is(err, release.ErrRepositoryMissing):
		writeError(w, http.StatusConflict, "release_repository_missing", "the app has no repository yet")
	case errors.Is(err, release.ErrNothingToRelease):
		writeError(w, http.StatusConflict, "release_nothing_new", "nothing changed since the last release")
	case errors.Is(err, release.ErrDiverged):
		writeError(w, http.StatusConflict, "release_diverged",
			"the marketplace already has a different app with this name under your account")
	case errors.Is(err, release.ErrUnavailable):
		log.Printf("release: %v", err)
		writeError(w, http.StatusServiceUnavailable, "release_unavailable", "the release could not be made right now")
	case errors.Is(err, marketplace.ErrNotSignedIn), errors.Is(err, marketplace.ErrUnavailable):
		writeMarketplaceError(w, err)
	default:
		log.Printf("release: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
