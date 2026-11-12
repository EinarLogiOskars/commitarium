package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/testpreviews"
)

// Test previews (HELPERS_PLAN.md, H6) follow preview checks: a helper that
// tests the app asks for one, and the host lists them, claims a pending one,
// starts it and lets the helper's worker reach it, reports its address or
// why it failed, and removes those that end, saying so.

type TestPreviewService interface {
	List(context.Context) ([]testpreviews.Preview, error)
	Claim(context.Context, string) (testpreviews.Preview, error)
	Ready(context.Context, string, string) (testpreviews.Preview, error)
	Fail(context.Context, string, string) (testpreviews.Preview, error)
	Removed(context.Context, string) error
}

type testPreviewReady struct {
	Address string `json:"address"`
}

type testPreviewFailed struct {
	Reason string `json:"reason"`
}

func (api *API) registerTestPreviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/test-previews", api.listTestPreviewsHandler)
	mux.HandleFunc("POST /api/v1/test-previews/{id}/claim", api.claimTestPreviewHandler)
	mux.HandleFunc("POST /api/v1/test-previews/{id}/ready", api.readyTestPreviewHandler)
	mux.HandleFunc("POST /api/v1/test-previews/{id}/failed", api.failedTestPreviewHandler)
	mux.HandleFunc("POST /api/v1/test-previews/{id}/removed", api.removedTestPreviewHandler)
}

func (api *API) listTestPreviewsHandler(w http.ResponseWriter, r *http.Request) {
	previews, err := api.testPreviews.List(r.Context())
	if err != nil {
		writeTestPreviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"previews": previews}, "test previews")
}

func (api *API) claimTestPreviewHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	preview, err := api.testPreviews.Claim(r.Context(), r.PathValue("id"))
	if err != nil {
		writeTestPreviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview, "claimed test preview")
}

func (api *API) readyTestPreviewHandler(w http.ResponseWriter, r *http.Request) {
	var body testPreviewReady
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	preview, err := api.testPreviews.Ready(r.Context(), r.PathValue("id"), body.Address)
	if err != nil {
		writeTestPreviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview, "ready test preview")
}

func (api *API) failedTestPreviewHandler(w http.ResponseWriter, r *http.Request) {
	var body testPreviewFailed
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	preview, err := api.testPreviews.Fail(r.Context(), r.PathValue("id"), body.Reason)
	if err != nil {
		writeTestPreviewError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview, "failed test preview")
}

func (api *API) removedTestPreviewHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	if err := api.testPreviews.Removed(r.Context(), r.PathValue("id")); err != nil {
		writeTestPreviewError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeTestPreviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, testpreviews.ErrNotFound):
		writeError(w, http.StatusNotFound, "test_preview_not_found", "no such test preview")
	case errors.Is(err, testpreviews.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_test_preview", "a test preview's address is http:// with a private IP address and a port")
	case errors.Is(err, testpreviews.ErrConflict):
		writeError(w, http.StatusConflict, "test_preview_state", "the test preview is not in that state")
	default:
		log.Printf("test previews are unavailable: %v", err)
		writeError(w, http.StatusServiceUnavailable, "test_previews_unavailable", "the test previews could not be read")
	}
}
