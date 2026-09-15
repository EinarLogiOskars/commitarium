package httpapi

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectdeletion"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func (api *API) deleteProjectHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be empty")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	force := false
	if values, exists := r.URL.Query()["force"]; exists {
		if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
			writeError(w, http.StatusBadRequest, "invalid_force", "force must be true or false")
			return
		}
		force = values[0] == "true"
	}
	projectID := r.PathValue("id")
	result, err := api.projectDeletion.Delete(r.Context(), projectID, key, force)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrNotFound):
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
		case errors.Is(err, projectdeletion.ErrActive), errors.Is(err, workorder.ErrActive):
			writeError(w, http.StatusConflict, "project_has_active_run", "stop active agent work or retry with force=true")
		case errors.Is(err, projectdeletion.ErrConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different project deletion")
		case errors.Is(err, projectdeletion.ErrUnsafeArtifacts),
			errors.Is(err, workorder.ErrUnsafeArtifacts),
			errors.Is(err, workspace.ErrConflict),
			errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict),
			errors.Is(err, workspace.ErrPullRequestConflict):
			writeError(w, http.StatusConflict, "project_deletion_conflict", "the project's internal artifacts could not be safely identified")
		case errors.Is(err, projectdeletion.ErrUnavailable),
			errors.Is(err, workspace.ErrCheckoutUnavailable),
			errors.Is(err, project.ErrForgejoUnavailable),
			errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusServiceUnavailable, "project_deletion_unavailable", "project deletion is temporarily unavailable; retry with the same Idempotency-Key")
		case errors.Is(err, feature.ErrNotFound):
			writeError(w, http.StatusServiceUnavailable, "project_deletion_unavailable", "project deletion was interrupted; retry with the same Idempotency-Key")
		default:
			log.Printf("delete project %q: %v", projectID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, result, "project deletion")
}
