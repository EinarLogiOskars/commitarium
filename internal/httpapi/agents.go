package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// AgentService manages agents: provider accounts used as lead or reviewer
// (ADR-016).
type AgentService interface {
	List(ctx context.Context) ([]agent.Agent, error)
	Get(ctx context.Context, id string) (agent.Agent, error)
	Create(ctx context.Context, name string, provider project.AgentProvider) (agent.Agent, error)
	Rename(ctx context.Context, id, name string) (agent.Agent, error)
	Delete(ctx context.Context, id string) error
}

type agentResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func newAgentResponse(a agent.Agent) agentResponse {
	return agentResponse{ID: a.ID, Name: a.Name, Provider: string(a.Provider), CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
}

type agentRequest struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

func (api *API) listAgentsHandler(w http.ResponseWriter, r *http.Request) {
	agents, err := api.agents.List(r.Context())
	if err != nil {
		writeAgentError(w, "", err)
		return
	}
	response := make([]agentResponse, 0, len(agents))
	for _, stored := range agents {
		response = append(response, newAgentResponse(stored))
	}
	writeJSON(w, http.StatusOK, response, "agent list")
}

func (api *API) createAgentHandler(w http.ResponseWriter, r *http.Request) {
	request := agentRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	created, err := api.agents.Create(r.Context(), request.Name, project.AgentProvider(strings.TrimSpace(request.Provider)))
	if err != nil {
		writeAgentError(w, "", err)
		return
	}
	w.Header().Set("Location", "/api/v1/agents/"+created.ID)
	writeJSON(w, http.StatusCreated, newAgentResponse(created), "agent")
}

func (api *API) renameAgentHandler(w http.ResponseWriter, r *http.Request) {
	request := agentRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	renamed, err := api.agents.Rename(r.Context(), r.PathValue("id"), request.Name)
	if err != nil {
		writeAgentError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, newAgentResponse(renamed), "agent")
}

func (api *API) deleteAgentHandler(w http.ResponseWriter, r *http.Request) {
	if err := api.agents.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeAgentError(w, r.PathValue("id"), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeAgentError(w http.ResponseWriter, id string, err error) {
	switch {
	case errors.Is(err, agent.ErrNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "agent not found")
	case errors.Is(err, agent.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_agent", err.Error())
	case errors.Is(err, agent.ErrInUse):
		writeError(w, http.StatusConflict, "agent_in_use", "a project default or an active work order still uses this agent")
	case errors.Is(err, agent.ErrConflict):
		writeError(w, http.StatusConflict, "agent_conflict", "too many agents share this name")
	default:
		log.Printf("agent %q: %v", id, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
