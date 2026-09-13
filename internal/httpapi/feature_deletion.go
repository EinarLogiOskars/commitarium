package httpapi

import (
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func (api *API) deleteFeatureHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be empty")
		return
	}
	projectID := r.PathValue("projectID")
	featureID := r.PathValue("id")
	result, err := api.featureDeletion.Delete(r.Context(), projectID, featureID)
	if err != nil {
		switch {
		case errors.Is(err, feature.ErrNotFound):
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
		case errors.Is(err, workorder.ErrActive):
			writeError(w, http.StatusConflict, "feature_active", "stop active agent work before deleting this feature")
		case errors.Is(err, workorder.ErrUnsafeArtifacts),
			errors.Is(err, workspace.ErrConflict),
			errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict),
			errors.Is(err, workspace.ErrPullRequestConflict):
			writeError(w, http.StatusConflict, "feature_deletion_conflict", "the work order's isolated artifacts could not be safely identified")
		case errors.Is(err, workspace.ErrCheckoutUnavailable),
			errors.Is(err, project.ErrForgejoUnavailable),
			errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusServiceUnavailable, "feature_deletion_unavailable", "the managed checkout or Forgejo is temporarily unavailable")
		default:
			log.Printf("delete feature %q from project %q: %v", featureID, projectID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, result, "feature deletion")
}
