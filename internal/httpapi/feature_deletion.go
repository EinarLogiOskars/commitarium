package httpapi

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

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
	force := false
	if values, exists := r.URL.Query()["force"]; exists {
		if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			writeError(w, http.StatusBadRequest, "invalid_force", "force must be true or false")
			return
		}
		force = values[0] == "true"
	}
	var result workorder.Result
	if force {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required for force deletion")
			return
		}
		result, err = api.featureDeletion.ForceDelete(r.Context(), projectID, featureID, key)
	} else {
		result, err = api.featureDeletion.Delete(r.Context(), projectID, featureID)
	}
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
			errors.Is(err, workorder.ErrUnavailable),
			errors.Is(err, project.ErrForgejoUnavailable),
			errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusServiceUnavailable, "feature_deletion_unavailable", "work-order deletion is temporarily unavailable; retry with the same Idempotency-Key")
		default:
			log.Printf("delete feature %q from project %q: %v", featureID, projectID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, result, "feature deletion")
}
