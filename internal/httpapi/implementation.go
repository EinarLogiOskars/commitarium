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
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func (api *API) startImplementationHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	runID := r.PathValue("id")
	startedRun, _, err := api.realWorkflow.StartImplementation(
		r.Context(), runID, idempotencyKey,
	)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, workflow.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different operation")
		case errors.Is(err, orchestration.ErrImplementationNotAllowed),
			errors.Is(err, feature.ErrInvalidTransition),
			errors.Is(err, workspace.ErrFeatureNotPlanning),
			errors.Is(err, workspace.ErrConflict),
			errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict),
			errors.Is(err, workspace.ErrCheckoutUnavailable),
			errors.Is(err, workspace.ErrPullRequestConflict),
			errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusConflict, "implementation_not_ready", "implementation requires a published agreed plan and an unchanged clean managed workspace with its draft pull request")
		case errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "forgejo_unavailable", "Forgejo implementation verification is unavailable")
		default:
			log.Printf("start implementation for run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Location", "/api/v1/runs/"+startedRun.ID)
	api.writeRun(w, r, http.StatusAccepted, startedRun)
}
