package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
)

// Preview checks (ADR-028) follow isolated validation's jobs: the desktop
// lists the pending checks, claims one, builds and starts the app's compose
// file for its commit on the host, and completes it with what each
// container did. The workflow then merges the work order or hands the
// evidence to its lead.

type PreviewCheckService interface {
	Pending(context.Context) ([]previewcheck.Check, error)
	Claim(context.Context, string) (previewcheck.Check, bool, error)
	Complete(context.Context, string, bool, previewcheck.Evidence) (previewcheck.Check, bool, error)
	ForRun(context.Context, string) ([]previewcheck.Check, error)
}

// previewCheckCompleter moves a run on once its preview check finished.
type previewCheckCompleter interface {
	PreviewCheckCompleted(ctx context.Context, checkID string) error
}

type previewCheckCompletion struct {
	Passed   bool                  `json:"passed"`
	Evidence previewcheck.Evidence `json:"evidence"`
}

// listPendingPreviewChecksHandler lists the checks waiting for the desktop.
func (api *API) listPendingPreviewChecksHandler(w http.ResponseWriter, r *http.Request) {
	checks, err := api.previewChecks.Pending(r.Context())
	if err != nil {
		writePreviewCheckError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks}, "pending preview checks")
}

func (api *API) claimPreviewCheckHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	check, _, err := api.previewChecks.Claim(r.Context(), r.PathValue("id"))
	if err != nil {
		writePreviewCheckError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, check, "claimed preview check")
}

// completePreviewCheckHandler records the desktop's result and moves the
// work order on. A repeated completion answers the stored check and moves
// it on again, which changes nothing once it has.
func (api *API) completePreviewCheckHandler(w http.ResponseWriter, r *http.Request) {
	var body previewCheckCompletion
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	check, _, err := api.previewChecks.Complete(r.Context(), r.PathValue("id"), body.Passed, body.Evidence)
	if err != nil {
		writePreviewCheckError(w, err)
		return
	}
	if completer, ok := api.realWorkflow.(previewCheckCompleter); ok {
		if err := completer.PreviewCheckCompleted(context.WithoutCancel(r.Context()), check.ID); err != nil {
			log.Printf("continue after preview check %q: %v", check.ID, err)
		}
	}
	writeJSON(w, http.StatusOK, check, "completed preview check")
}

// listFeaturePreviewChecksHandler lists a work order's preview checks,
// newest first, for its page.
func (api *API) listFeaturePreviewChecksHandler(w http.ResponseWriter, r *http.Request) {
	projectID, featureID := r.PathValue("projectID"), r.PathValue("id")
	if _, err := api.features.GetByID(r.Context(), projectID, featureID); err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(w, http.StatusNotFound, "feature_not_found", "work order not found")
			return
		}
		log.Printf("load work order %q for its preview checks: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	runs, err := api.execution.RunsForFeature(r.Context(), featureID)
	if err != nil {
		log.Printf("list runs of work order %q for its preview checks: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	checks := make([]previewcheck.Check, 0)
	for _, run := range runs {
		listed, err := api.previewChecks.ForRun(r.Context(), run.ID)
		if err != nil {
			writePreviewCheckError(w, err)
			return
		}
		checks = append(checks, listed...)
	}
	sort.SliceStable(checks, func(i, j int) bool { return checks[i].CreatedAt.After(checks[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks}, "work order preview checks")
}

func writePreviewCheckError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, previewcheck.ErrNotFound):
		writeError(w, http.StatusNotFound, "preview_check_not_found", "preview check not found")
	case errors.Is(err, previewcheck.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_preview_check", "the preview check result is invalid")
	case errors.Is(err, previewcheck.ErrConflict):
		writeError(w, http.StatusConflict, "preview_check_conflict", "the preview check is not in the required state")
	default:
		log.Printf("preview check API: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
