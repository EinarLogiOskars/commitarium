package httpapi

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func (api *API) mergeRunHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	runID := r.PathValue("id")
	merged, _, err := api.realWorkflow.Merge(r.Context(), runID, idempotencyKey)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, orchestration.ErrImplementationNotAllowed),
			errors.Is(err, workspace.ErrFeatureNotReadyToMerge),
			errors.Is(err, workspace.ErrConflict),
			errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict),
			errors.Is(err, workspace.ErrPullRequestConflict),
			errors.Is(err, feature.ErrInvalidTransition):
			writeError(w, http.StatusConflict, "merge_not_ready", "merge requires the unchanged Forgejo pull request and exact commit approved by both agents")
		case errors.Is(err, workspace.ErrCheckoutUnavailable),
			errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "merge_unavailable", "the approved Forgejo revision cannot be merged right now")
		default:
			log.Printf("merge run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	api.writeRun(w, r, http.StatusOK, merged)
}
