package httpapi

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
)

func (api *API) startPlanningReviewHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	runID := r.PathValue("id")
	startedRun, _, err := api.realWorkflow.StartPlanningReview(r.Context(), runID, idempotencyKey)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, execution.ErrRecordConflict):
			writeError(w, http.StatusConflict, "reviewer_conflict", "stored reviewer session conflicts with this run")
		case errors.Is(err, orchestration.ErrRunControlNotAllowed):
			writeError(w, http.StatusConflict, "run_paused", "resume the run before starting another workflow phase")
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

func (api *API) startPlanningRoundHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	runID := r.PathValue("id")
	startedRun, _, err := api.realWorkflow.StartPlanningRound(r.Context(), runID, idempotencyKey)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, execution.ErrRecordConflict):
			writeError(w, http.StatusConflict, "planning_round_conflict", "stored planning sessions conflict with this run")
		case errors.Is(err, execution.ErrRunActionConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different operation")
		case errors.Is(err, orchestration.ErrRunControlNotAllowed):
			writeError(w, http.StatusConflict, "run_paused", "resume the run before starting another workflow phase")
		case errors.Is(err, orchestration.ErrPlanningNotAllowed), errors.Is(err, execution.ErrStateConflict):
			writeError(w, http.StatusConflict, "planning_round_not_ready", "the planning loop requires a completed reviewer response and a ready run")
		default:
			log.Printf("start planning round for run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Location", "/api/v1/runs/"+startedRun.ID)
	api.writeRun(w, r, http.StatusAccepted, startedRun)
}
