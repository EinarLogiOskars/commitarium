package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

const maxInterventionRequestBytes = 128 * 1024

type interventionRequest struct {
	Target  worker.Role `json:"target"`
	Message string      `json:"message"`
}

func (api *API) queueInterventionHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxInterventionRequestBytes)
	request := interventionRequest{}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid_intervention", "request must contain exactly target and message")
		return
	}
	if (request.Target != worker.RoleLead && request.Target != worker.RoleReviewer) ||
		strings.TrimSpace(request.Message) == "" {
		writeError(w, http.StatusBadRequest, "invalid_intervention", "target must be lead or reviewer and message must not be blank")
		return
	}

	runID := r.PathValue("id")
	_, _, err := api.realWorkflow.QueueIntervention(
		r.Context(), runID, interventionIDForKey(key), request.Target, request.Message,
	)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, execution.ErrInvalidIntervention):
			writeError(w, http.StatusBadRequest, "invalid_intervention", "target must be lead or reviewer and message must not be blank")
		case errors.Is(err, execution.ErrInterventionConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different intervention")
		case errors.Is(err, execution.ErrInterventionInProgress):
			writeError(w, http.StatusConflict, "intervention_in_progress", "the run already has an unfinished intervention")
		case errors.Is(err, execution.ErrInterventionTargetUnavailable):
			writeError(w, http.StatusConflict, "intervention_target_unavailable", "the selected agent conversation is not available for intervention")
		case errors.Is(err, execution.ErrInterventionNotAllowed):
			writeError(w, http.StatusConflict, "intervention_not_allowed", "the run cannot accept an intervention in its current state")
		default:
			log.Printf("queue intervention for run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	run, err := api.execution.GetRun(r.Context(), runID)
	if err != nil {
		log.Printf("reload run %q after intervention: %v", runID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	w.Header().Set("Location", "/api/v1/runs/"+runID)
	api.writeRun(w, r, http.StatusAccepted, run)
}

func interventionIDForKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return "int_" + hex.EncodeToString(digest[:16])
}
