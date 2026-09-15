package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type startToolchainAssistantRequest struct {
	Provider project.AgentProvider `json:"provider"`
	Model    string                `json:"model"`
	Message  string                `json:"message"`
}

type replyToolchainAssistantRequest struct {
	Message string `json:"message"`
}

func (api *API) startToolchainAssistantHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	var request startToolchainAssistantRequest
	if !decodeOneJSON(r, &request) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one valid JSON object with no unknown fields")
		return
	}
	if err := api.validateAssistantModel(r, request.Provider, request.Model); err != nil {
		if errors.Is(err, project.ErrInvalidAgentProviders) {
			writeError(w, http.StatusBadRequest, "invalid_agent_provider", "provider must be codex or claude")
			return
		}
		writeModelSelectionError(w, err)
		return
	}
	session, created, err := api.toolchainAssistant.Start(r.Context(), r.PathValue("id"), request.Provider, request.Model, request.Message, key)
	if err != nil {
		api.writeAssistantError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	w.Header().Set("Location", "/api/v1/projects/"+r.PathValue("id")+"/toolchain/assistant-sessions/"+session.ID)
	writeJSON(w, status, session, "toolchain assistant session")
}

func (api *API) getToolchainAssistantHandler(w http.ResponseWriter, r *http.Request) {
	session, err := api.toolchainAssistant.Get(r.Context(), r.PathValue("id"), r.PathValue("sessionID"))
	if err != nil {
		api.writeAssistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session, "toolchain assistant session")
}

func (api *API) replyToolchainAssistantHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	var request replyToolchainAssistantRequest
	if !decodeOneJSON(r, &request) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one valid JSON object with no unknown fields")
		return
	}
	session, created, err := api.toolchainAssistant.Reply(r.Context(), r.PathValue("id"), r.PathValue("sessionID"), request.Message, key)
	if err != nil {
		api.writeAssistantError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, session, "toolchain assistant session")
}

func (api *API) applyToolchainAssistantHandler(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) > 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be empty")
		return
	}
	manifest, err := api.toolchainAssistant.Apply(r.Context(), r.PathValue("id"), r.PathValue("sessionID"))
	if err != nil {
		api.writeAssistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest, "project toolchain")
}

func decodeOneJSON(r *http.Request, destination any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func (api *API) validateAssistantModel(r *http.Request, provider project.AgentProvider, model string) error {
	if !provider.IsValid() {
		return project.ErrInvalidAgentProviders
	}
	if _, err := (project.AgentModels{Lead: model, Reviewer: model}).Normalize(); err != nil {
		return err
	}
	if api.modelCatalog == nil {
		return nil
	}
	for _, catalog := range api.modelCatalog.List(r.Context()) {
		if catalog.Provider != provider || catalog.Role != modelcatalog.RoleLead || catalog.FetchedAt == nil {
			continue
		}
		for _, available := range catalog.Models {
			if available.ID == model {
				return nil
			}
		}
		return modelcatalog.ErrModelUnavailable
	}
	return modelcatalog.ErrCatalogUnavailable
}

func (api *API) writeAssistantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "project not found")
	case errors.Is(err, toolchain.ErrAssistantNotFound):
		writeError(w, http.StatusNotFound, "toolchain_assistant_not_found", "toolchain assistant session not found")
	case errors.Is(err, toolchain.ErrInvalidManifest):
		writeError(w, http.StatusBadRequest, "invalid_toolchain_assistant_request", "provider, exact model, and a non-empty message are required")
	case errors.Is(err, toolchain.ErrAssistantConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for different setup assistant input")
	case errors.Is(err, toolchain.ErrAssistantNotReady):
		writeError(w, http.StatusConflict, "toolchain_assistant_not_ready", "the setup assistant is not waiting for this action")
	case errors.Is(err, toolchain.ErrAssistantUnavailable), errors.Is(err, workerhttp.ErrRequestFailed), errors.Is(err, workerhttp.ErrRemote):
		writeError(w, http.StatusServiceUnavailable, "toolchain_assistant_unavailable", "the setup assistant worker is unavailable")
	default:
		log.Printf("toolchain assistant: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
