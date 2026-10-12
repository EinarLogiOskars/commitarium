package httpapi

import (
	"log"
	"net/http"
	"sort"
	"time"
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
