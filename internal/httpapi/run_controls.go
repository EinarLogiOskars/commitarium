package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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

type recoveryControlRequest struct {
	Action  orchestration.RecoveryAction `json:"action"`
	Message string                       `json:"message,omitempty"`
}

type directedRecoveryWorkflow interface {
	RecoverBlockerWithDirective(context.Context, string, string, orchestration.RecoveryDirective) (execution.Run, bool, error)
}

func (api *API) pauseRunHandler(w http.ResponseWriter, r *http.Request) {
	api.changeRunPause(w, r, true)
}

func (api *API) resumeRunHandler(w http.ResponseWriter, r *http.Request) {
	api.changeRunPause(w, r, false)
}

func (api *API) recoverRunHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 20*1024+1))
	if err != nil || len(body) > 20*1024 {
		writeError(w, http.StatusBadRequest, "invalid_body", "recovery request is too large")
		return
	}
	control := recoveryControlRequest{Action: orchestration.RecoveryActionContinue}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &control); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_body", "recovery request must be valid JSON")
			return
		}
	}
	if !validRecoveryAction(control.Action) {
		writeError(w, http.StatusBadRequest, "invalid_recovery_action", "action must be recheck, continue, replace, or leave_paused")
		return
	}
	runID := r.PathValue("id")
	var recovered execution.Run
	if directed, ok := api.realWorkflow.(directedRecoveryWorkflow); ok {
		recovered, _, err = directed.RecoverBlockerWithDirective(
			r.Context(), runID, runActionIDForKey(key),
			orchestration.RecoveryDirective{Action: control.Action, Message: control.Message},
		)
	} else if control.Action == orchestration.RecoveryActionContinue {
		recovered, _, err = api.realWorkflow.RecoverBlocker(r.Context(), runID, runActionIDForKey(key))
	} else {
		writeError(w, http.StatusBadRequest, "unsupported_recovery_action", "this coordinator does not support the requested recovery action")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, orchestration.ErrRecoveryNotAllowed):
			writeError(w, http.StatusConflict, "recovery_not_allowed", "the run is not waiting on a recovery blocker")
		default:
			log.Printf("recover blocked run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	response, responseErr := api.newRunResponse(r.Context(), recovered)
	if responseErr != nil {
		log.Printf("build recovery response for run %q: %v", recovered.ID, responseErr)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	response.Recovery = newRecoveryResponse(control.Action, recovered, response.Sessions, response.AgentProviders)
	writeJSON(w, http.StatusAccepted, response, "run recovery")
}

func validRecoveryAction(action orchestration.RecoveryAction) bool {
	return action == orchestration.RecoveryActionRecheck ||
		action == orchestration.RecoveryActionContinue ||
		action == orchestration.RecoveryActionReplace ||
		action == orchestration.RecoveryActionLeavePaused
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
		case errors.Is(err, execution.ErrRunActionConflict), errors.Is(err, workflow.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different operation")
		case errors.Is(err, execution.ErrInvalidStatusTransition), errors.Is(err, execution.ErrStateConflict),
			errors.Is(err, orchestration.ErrRunControlNotAllowed):
			writeError(w, http.StatusConflict, "run_control_not_allowed", "the run cannot be paused or resumed from its current state")
		case errors.Is(err, orchestration.ErrInterventionPending):
			writeError(w, http.StatusConflict, "intervention_pending", "the queued intervention must be answered before the workflow can continue")
		case errors.Is(err, orchestration.ErrInterventionClarificationRequired):
			writeError(w, http.StatusConflict, "intervention_clarification_required", "the selected agent needs another user message before the workflow can continue")
		case errors.Is(err, orchestration.ErrInterventionReplanningRequired):
			writeError(w, http.StatusConflict, "intervention_replanning_required", "the requested scope change needs a safe replanning decision before the workflow can continue")
		case errors.Is(err, feature.ErrInvalidTransition),
			errors.Is(err, workspace.ErrFeatureNotReplannable),
			errors.Is(err, workspace.ErrConflict),
			errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict),
			errors.Is(err, workspace.ErrPullRequestConflict),
			errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusConflict, "intervention_replanning_required", "the existing feature branch, checkout, pull request, or plan could not be confirmed; inspect them before retrying replanning")
		case errors.Is(err, workspace.ErrCheckoutUnavailable), errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "replanning_unavailable", "the managed checkout or Forgejo is temporarily unavailable for replanning")
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
