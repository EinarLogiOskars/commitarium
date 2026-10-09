package httpapi

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

type tokenUsageResponse struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
}

type roleUsageResponse struct {
	Role string `json:"role"`
	tokenUsageResponse
}

type phaseUsageResponse struct {
	Phase string `json:"phase"`
	Role  string `json:"role"`
	tokenUsageResponse
}

type featureUsageResponse struct {
	Roles  []roleUsageResponse  `json:"roles"`
	Phases []phaseUsageResponse `json:"phases"`
	Total  tokenUsageResponse   `json:"total"`
}

func newTokenUsageResponse(usage execution.TokenUsage) tokenUsageResponse {
	return tokenUsageResponse{
		InputTokens:       usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		CacheWriteTokens:  usage.CacheWriteTokens,
		OutputTokens:      usage.OutputTokens,
	}
}

// attemptPhase names the workflow phase an attempt belonged to. Attempt IDs
// are "<session>:<kind>:...", and the kind identifies the phase.
func attemptPhase(sessionID, attemptID string) string {
	rest, found := strings.CutPrefix(attemptID, sessionID+":")
	if !found {
		return "other"
	}
	kind, _, _ := strings.Cut(rest, ":")
	switch kind {
	case "turn", "reply":
		return "clarify"
	case "planning", "planning-recovery":
		return "plan"
	case "approval":
		return "plan_approval"
	case "implementation":
		return "implement"
	case "acceptance":
		return "acceptance_tests"
	case "review":
		return "review"
	case "correction":
		return "correction"
	case "readiness":
		return "readiness"
	case "intervention":
		return "intervention"
	default:
		return "other"
	}
}

// phaseOrder keeps the breakdown in workflow order rather than first-use order.
var phaseOrder = map[string]int{
	"clarify": 0, "plan": 1, "plan_approval": 2, "implement": 3, "acceptance_tests": 4,
	"review": 5, "correction": 6, "readiness": 7, "intervention": 8, "other": 9,
}

func (api *API) getFeatureUsageHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	featureID := r.PathValue("id")
	if _, err := api.features.GetByID(r.Context(), projectID, featureID); err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
			return
		}
		log.Printf("verify feature %q for token usage: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	attempts, err := api.usage.FeatureAttemptUsage(r.Context(), featureID)
	if err != nil {
		log.Printf("list token usage for feature %q: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	type phaseKey struct{ phase, role string }
	roles := map[string]execution.TokenUsage{}
	phases := map[phaseKey]execution.TokenUsage{}
	total := execution.TokenUsage{}
	for _, attempt := range attempts {
		roles[attempt.Role] = roles[attempt.Role].Add(attempt.Usage)
		key := phaseKey{attemptPhase(attempt.SessionID, attempt.AttemptID), attempt.Role}
		phases[key] = phases[key].Add(attempt.Usage)
		total = total.Add(attempt.Usage)
	}
	response := featureUsageResponse{
		Roles:  make([]roleUsageResponse, 0, len(roles)),
		Phases: make([]phaseUsageResponse, 0, len(phases)),
		Total:  newTokenUsageResponse(total),
	}
	for role, usage := range roles {
		response.Roles = append(response.Roles, roleUsageResponse{Role: role, tokenUsageResponse: newTokenUsageResponse(usage)})
	}
	sort.Slice(response.Roles, func(i, j int) bool { return response.Roles[i].Role < response.Roles[j].Role })
	for key, usage := range phases {
		response.Phases = append(response.Phases, phaseUsageResponse{
			Phase: key.phase, Role: key.role, tokenUsageResponse: newTokenUsageResponse(usage),
		})
	}
	sort.Slice(response.Phases, func(i, j int) bool {
		left, right := response.Phases[i], response.Phases[j]
		if phaseOrder[left.Phase] != phaseOrder[right.Phase] {
			return phaseOrder[left.Phase] < phaseOrder[right.Phase]
		}
		return left.Role < right.Role
	})
	writeJSON(w, http.StatusOK, response, "feature token usage")
}
