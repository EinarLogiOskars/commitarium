package httpapi

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func (api *API) startPlanningHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	runID := r.PathValue("id")
	startedRun, _, err := api.planning.StartPlanning(r.Context(), runID, idempotencyKey)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, workflow.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different operation")
		case errors.Is(err, orchestration.ErrPlanningNotAllowed),
			errors.Is(err, feature.ErrInvalidTransition),
			errors.Is(err, workspace.ErrGoalNotAccepted),
			errors.Is(err, workspace.ErrFeatureNotDraft),
			errors.Is(err, workspace.ErrProjectRepositoryNotBound),
			errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict),
			errors.Is(err, workspace.ErrCheckoutUnavailable),
			errors.Is(err, workspace.ErrPullRequestConflict):
			writeError(w, http.StatusConflict, "planning_not_ready", "planning requires a waiting lead, an accepted goal, and a verified managed workspace with a draft pull request")
		default:
			log.Printf("start planning for run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Location", "/api/v1/runs/"+startedRun.ID)
	api.writeRun(w, r, http.StatusAccepted, startedRun)
}

func (api *API) startPlanningReviewHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	runID := r.PathValue("id")
	startedRun, _, err := api.planning.StartPlanningReview(r.Context(), runID, idempotencyKey)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, execution.ErrRecordConflict):
			writeError(w, http.StatusConflict, "reviewer_conflict", "stored reviewer session conflicts with this run")
		case errors.Is(err, orchestration.ErrPlanningNotAllowed):
			writeError(w, http.StatusConflict, "reviewer_not_ready", "reviewer planning requires a completed lead proposal and a verified managed workspace")
		default:
			log.Printf("start planning reviewer for run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Location", "/api/v1/runs/"+startedRun.ID)
	api.writeRun(w, r, http.StatusAccepted, startedRun)
}
