package httpapi

import (
	"context"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// agentLimitResponse is one agent at its usage limit: when it resets, when
// known, and how many runs wait on it.
type agentLimitResponse struct {
	Agent    string     `json:"agent"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Waiting  int        `json:"waiting"`
}

// agentLimitsHandler lists the agents whose usage limit has paused work.
func (api *API) agentLimitsHandler(w http.ResponseWriter, r *http.Request) {
	limits := make([]agentLimitResponse, 0)
	waits, ok := api.execution.(runLimitWaits)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"limits": limits}, "agent limits")
		return
	}
	pending, err := waits.LimitWaits(r.Context())
	if err != nil {
		log.Printf("list usage limit waits: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	byAgent := map[string]*agentLimitResponse{}
	for _, wait := range pending {
		limit, found := byAgent[wait.AgentID]
		if !found {
			limit = &agentLimitResponse{Agent: wait.AgentID}
			byAgent[wait.AgentID] = limit
		}
		limit.Waiting++
		if wait.ResetsAt != nil && (limit.ResetsAt == nil || wait.ResetsAt.After(*limit.ResetsAt)) {
			resetsAt := *wait.ResetsAt
			limit.ResetsAt = &resetsAt
		}
	}
	for _, limit := range byAgent {
		limits = append(limits, *limit)
	}
	sort.Slice(limits, func(i, j int) bool { return limits[i].Agent < limits[j].Agent })
	writeJSON(w, http.StatusOK, map[string]any{"limits": limits}, "agent limits")
}

// AgentUsage is one agent's latest report of its account's usage limits.
type AgentUsage = agent.Usage

// AgentUsageService reports each connected agent's usage limits as its
// worker last saw them. An agent whose worker does not answer is left out; a
// nil list means the workers cannot be asked at all (the simulated runner).
type AgentUsageService interface {
	AgentUsage(ctx context.Context) ([]AgentUsage, error)
}

// agentUsageHandler answers each agent's usage windows, for the Agents page.
func (api *API) agentUsageHandler(w http.ResponseWriter, r *http.Request) {
	usage := make([]AgentUsage, 0)
	if api.agentUsage != nil {
		reported, err := api.agentUsage.AgentUsage(r.Context())
		if err != nil {
			log.Printf("agent usage: %v", err)
		} else if reported != nil {
			usage = reported
		}
	}
	// An agent that has not worked yet reports no windows: an empty list,
	// never null.
	for index := range usage {
		if usage[index].Windows == nil {
			usage[index].Windows = []workerhttp.UsageWindow{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": usage}, "agent usage")
}
