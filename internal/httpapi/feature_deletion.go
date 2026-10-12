package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
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

type workOrderResetter interface {
	ReturnToReady(ctx context.Context, projectID, featureID, requestKey string) (feature.Feature, error)
}

// returnToReadyHandler sets a started work order back to Ready, keeping its
// handoff brief (ADR-017). Agent work is stopped first, so the request needs
// an Idempotency-Key and is safe to retry.
func (api *API) returnToReadyHandler(w http.ResponseWriter, r *http.Request) {
	resetter, ok := api.featureDeletion.(workOrderResetter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "return_to_ready_unavailable", "returning work orders to Ready is unavailable")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	projectID, featureID := r.PathValue("projectID"), r.PathValue("id")
	reset, err := resetter.ReturnToReady(r.Context(), projectID, featureID, key)
	if err != nil {
		switch {
		case errors.Is(err, feature.ErrNotFound):
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
		case errors.Is(err, workorder.ErrNotStarted):
			writeError(w, http.StatusConflict, "feature_not_started", "only a started work order that has not merged can return to Ready")
		case errors.Is(err, workorder.ErrActive):
			writeError(w, http.StatusConflict, "feature_active", "agent work is still stopping; retry with the same Idempotency-Key")
		case errors.Is(err, workorder.ErrUnsafeArtifacts),
			errors.Is(err, workspace.ErrConflict):
			writeError(w, http.StatusConflict, "feature_reset_conflict", "the work order's isolated artifacts could not be safely identified")
		case errors.Is(err, workorder.ErrUnavailable),
			errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "return_to_ready_unavailable", "returning to Ready is temporarily unavailable; retry with the same Idempotency-Key")
		default:
			log.Printf("return feature %q of project %q to Ready: %v", featureID, projectID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, newFeatureResponse(reset), "feature")
}

var errWorkOrderEnded = errors.New("the work order has already ended")

// closeWorkOrder ends a work order without merging it, keeping its history:
// started work first returns to Ready (its agents stop and its branch and
// pull request close), then it is cancelled. Closing a closed one is a no-op.
func (api *API) closeWorkOrder(ctx context.Context, projectID, featureID, key string) (feature.Feature, error) {
	stored, err := api.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return feature.Feature{}, err
	}
	switch {
	case stored.State == feature.StateCancelled:
		return stored, nil
	case stored.State.IsTerminal():
		return feature.Feature{}, errWorkOrderEnded
	}
	if stored.State != feature.StateDraft && stored.State != feature.StateReady {
		resetter, ok := api.featureDeletion.(workOrderResetter)
		if !ok {
			return feature.Feature{}, fmt.Errorf("%w: closing started work is unavailable", workorder.ErrUnavailable)
		}
		if _, err := resetter.ReturnToReady(ctx, projectID, featureID, key+":ready"); err != nil {
			return feature.Feature{}, err
		}
	}
	if api.workflow == nil {
		return feature.Feature{}, fmt.Errorf("%w: closing is unavailable", workorder.ErrUnavailable)
	}
	if _, err := api.workflow.TransitionFeature(ctx, featureID, feature.StateCancelled,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID}, key+":close"); err != nil {
		return feature.Feature{}, err
	}
	return api.features.GetByID(ctx, projectID, featureID)
}

// closeWorkOrderHandler closes a work order (closeWorkOrder). Agent work is
// stopped first, so the request needs an Idempotency-Key and is safe to retry.
func (api *API) closeWorkOrderHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	projectID, featureID := r.PathValue("projectID"), r.PathValue("id")
	closed, err := api.closeWorkOrder(r.Context(), projectID, featureID, key)
	if err != nil {
		writeCloseError(w, projectID, featureID, err)
		return
	}
	writeJSON(w, http.StatusOK, newFeatureResponse(closed), "feature")
}

func writeCloseError(w http.ResponseWriter, projectID, featureID string, err error) {
	switch {
	case errors.Is(err, feature.ErrNotFound):
		writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
	case errors.Is(err, errWorkOrderEnded):
		writeError(w, http.StatusConflict, "feature_ended", "the work order has already merged")
	case errors.Is(err, workorder.ErrActive):
		writeError(w, http.StatusConflict, "feature_active", "agent work is still stopping; retry with the same Idempotency-Key")
	case errors.Is(err, workorder.ErrUnsafeArtifacts), errors.Is(err, workspace.ErrConflict),
		errors.Is(err, workspace.ErrBranchConflict), errors.Is(err, workspace.ErrCheckoutConflict),
		errors.Is(err, workspace.ErrPullRequestConflict):
		writeError(w, http.StatusConflict, "feature_close_conflict", "the work order's branch or pull request could not be safely identified, so nothing was closed")
	case errors.Is(err, workorder.ErrUnavailable), errors.Is(err, project.ErrForgejoUnavailable):
		writeError(w, http.StatusServiceUnavailable, "close_unavailable", "closing is temporarily unavailable; retry with the same Idempotency-Key")
	default:
		log.Printf("close feature %q of project %q: %v", featureID, projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
