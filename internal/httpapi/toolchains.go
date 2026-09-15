package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

type configureToolchainRequest struct {
	Source   toolchain.Source  `json:"source"`
	Tools    map[string]string `json:"tools"`
	Services []string          `json:"services"`
}

func (api *API) listToolchainPresetsHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": toolchain.Presets()}, "toolchain presets")
}

func (api *API) getProjectToolchainHandler(w http.ResponseWriter, r *http.Request) {
	manifest, err := api.toolchains.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeToolchainError(w, r.PathValue("id"), "read", err)
		return
	}
	writeJSON(w, http.StatusOK, manifest, "project toolchain")
}

func (api *API) configureProjectToolchainHandler(w http.ResponseWriter, r *http.Request) {
	var request configureToolchainRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one valid JSON object with no unknown fields")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one valid JSON object with no unknown fields")
		return
	}
	manifest, err := api.toolchains.Configure(r.Context(), r.PathValue("id"), toolchain.Manifest{
		Source: request.Source, Tools: request.Tools, Services: request.Services,
	})
	if err != nil {
		api.writeToolchainError(w, r.PathValue("id"), "configure", err)
		return
	}
	writeJSON(w, http.StatusOK, manifest, "project toolchain")
}

func (api *API) detectProjectToolchainHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) > 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be empty")
		return
	}
	suggestion, err := api.toolchains.Detect(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeToolchainError(w, r.PathValue("id"), "detect", err)
		return
	}
	writeJSON(w, http.StatusOK, suggestion, "toolchain detection")
}

func (api *API) writeToolchainError(w http.ResponseWriter, projectID, action string, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "project not found")
	case errors.Is(err, toolchain.ErrInvalidManifest):
		writeError(w, http.StatusBadRequest, "invalid_toolchain", "tools must use supported names and explicit versions; services must use safe identifiers")
	case errors.Is(err, project.ErrForgejoRepositoryNotFound),
		errors.Is(err, project.ErrForgejoRepositoryNotReady),
		errors.Is(err, project.ErrForgejoUnavailable):
		writeError(w, http.StatusServiceUnavailable, "repository_unavailable", "the internal repository cannot be inspected")
	case errors.Is(err, toolchain.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "toolchain_unavailable", "the internal project toolchain record is unavailable")
	default:
		log.Printf("%s toolchain for project %q: %v", action, projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
