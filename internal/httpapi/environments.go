package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type environmentDecisionRequest struct {
	Reason string `json:"reason"`
}
type environmentCompletionRequest struct {
	ResolvedPackages map[string]string `json:"resolved_packages"`
}

func (api *API) listEnvironmentRequestsHandler(w http.ResponseWriter, r *http.Request) {
	requests, err := api.environments.ListByProject(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeEnvironmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": requests}, "environment requests")
}

func (api *API) getEnvironmentRequestHandler(w http.ResponseWriter, r *http.Request) {
	request, err := api.environments.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeEnvironmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, request, "environment request")
}

func (api *API) approveEnvironmentRequestHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	request, _, err := api.environments.Approve(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeEnvironmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, request, "approved environment request")
}

func (api *API) rejectEnvironmentRequestHandler(w http.ResponseWriter, r *http.Request) {
	var body environmentDecisionRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	request, _, err := api.environments.Reject(r.Context(), r.PathValue("id"), body.Reason)
	if err != nil {
		api.writeEnvironmentError(w, err)
		return
	}
	if _, err := api.controller.SendCommand(r.Context(), request.SessionID, worker.Command{
		ID: request.ID + ":rejected", Type: worker.CommandMessage,
		Message: "The user declined the requested system packages. Continue in the existing managed environment, choose an alternative that does not require them, or explain why the work remains blocked.",
	}); err != nil {
		log.Printf("resume run after environment rejection %q: %v", request.ID, err)
		writeError(w, http.StatusConflict, "environment_resume_failed", "the request was rejected but the agent conversation could not resume yet; retry this operation")
		return
	}
	writeJSON(w, http.StatusOK, request, "rejected environment request")
}

func (api *API) beginEnvironmentProvisioningHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	request, _, err := api.environments.BeginProvisioning(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeEnvironmentError(w, err)
		return
	}
	packages, err := api.environments.ApprovedPackages(r.Context())
	if err != nil {
		api.writeEnvironmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": request, "approved_packages": packages}, "environment provisioning specification")
}

func (api *API) completeEnvironmentProvisioningHandler(w http.ResponseWriter, r *http.Request) {
	var body environmentCompletionRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	request, _, err := api.environments.Complete(r.Context(), r.PathValue("id"), body.ResolvedPackages)
	if err != nil {
		api.writeEnvironmentError(w, err)
		return
	}
	if _, err := api.controller.SendCommand(r.Context(), request.SessionID, worker.Command{
		ID: request.ID + ":ready", Type: worker.CommandMessage,
		Message: "The user approved the requested system packages and Commitarium provisioned them for the managed agent and validation environments. Reconcile the workspace, confirm the required tools are now available, and continue the same implementation turn without repeating completed work.",
	}); err != nil {
		log.Printf("resume run after environment provisioning %q: %v", request.ID, err)
		writeError(w, http.StatusConflict, "environment_resume_failed", "the environment is ready but the agent conversation could not resume yet; retry this operation")
		return
	}
	writeJSON(w, http.StatusOK, request, "completed environment provisioning")
}

func (api *API) failEnvironmentProvisioningHandler(w http.ResponseWriter, r *http.Request) {
	var body environmentDecisionRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	request, _, err := api.environments.Fail(r.Context(), r.PathValue("id"), body.Reason)
	if err != nil {
		api.writeEnvironmentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, request, "failed environment provisioning")
}

func (api *API) writeEnvironmentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, projectenvironment.ErrNotFound):
		writeError(w, http.StatusNotFound, "environment_request_not_found", "environment request not found")
	case errors.Is(err, projectenvironment.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_environment_request", "the environment request is invalid")
	case errors.Is(err, projectenvironment.ErrConflict):
		writeError(w, http.StatusConflict, "environment_request_conflict", "the environment request is not in the required state")
	default:
		log.Printf("project environment request: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func decodeSingleJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one valid JSON object with no unknown fields")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one valid JSON object with no unknown fields")
		return false
	}
	return true
}

func emptyBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body must be empty")
		return false
	}
	return true
}
