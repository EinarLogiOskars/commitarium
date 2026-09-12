package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
)

func (api *API) pauseRunHandler(w http.ResponseWriter, r *http.Request) {
	api.changeRunPause(w, r, true)
}

func (api *API) resumeRunHandler(w http.ResponseWriter, r *http.Request) {
	api.changeRunPause(w, r, false)
}

func (api *API) changeRunPause(w http.ResponseWriter, r *http.Request, pause bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be empty")
		return
	}
	runID := r.PathValue("id")
	var changed execution.Run
	if pause {
		changed, _, err = api.realWorkflow.Pause(r.Context(), runID, runActionIDForKey(key))
	} else {
		changed, _, err = api.realWorkflow.Resume(r.Context(), runID, runActionIDForKey(key))
	}
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, execution.ErrRunActionConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different operation")
		case errors.Is(err, execution.ErrInvalidStatusTransition), errors.Is(err, orchestration.ErrRunControlNotAllowed):
			writeError(w, http.StatusConflict, "run_control_not_allowed", "the run cannot be paused or resumed from its current state")
		default:
			log.Printf("change pause state for run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	api.writeRun(w, r, http.StatusAccepted, changed)
}

func runActionIDForKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "run_action_" + hex.EncodeToString(digest[:16])
}
