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
	// Models splits the phase by the models and efforts that spent it
	// (ADR-031); attempts recorded without a model are left out.
	Models []phaseModelUsageResponse `json:"models,omitempty"`
}

type phaseModelUsageResponse struct {
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	tokenUsageResponse
}

// modelUsageResponse is one model's (and effort's) share of a role: a run's
// model can be switched while it works, and planning may use another.
type modelUsageResponse struct {
	Role   string `json:"role"`
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	tokenUsageResponse
}

type featureUsageResponse struct {
	Roles  []roleUsageResponse  `json:"roles"`
	Models []modelUsageResponse `json:"models"`
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
	type modelKey struct{ role, model, effort string }
	models := map[modelKey]execution.TokenUsage{}
	type phaseModelKey struct {
		phase         phaseKey
		model, effort string
	}
	phaseModels := map[phaseModelKey]execution.TokenUsage{}
	total := execution.TokenUsage{}
	for _, attempt := range attempts {
		roles[attempt.Role] = roles[attempt.Role].Add(attempt.Usage)
		key := phaseKey{attemptPhase(attempt.SessionID, attempt.AttemptID), attempt.Role}
		phases[key] = phases[key].Add(attempt.Usage)
		if attempt.Model != "" {
			model := modelKey{attempt.Role, attempt.Model, attempt.Effort}
			models[model] = models[model].Add(attempt.Usage)
			split := phaseModelKey{key, attempt.Model, attempt.Effort}
			phaseModels[split] = phaseModels[split].Add(attempt.Usage)
		}
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
	response.Models = make([]modelUsageResponse, 0, len(models))
	for key, usage := range models {
		response.Models = append(response.Models, modelUsageResponse{
			Role: key.role, Model: key.model, Effort: key.effort, tokenUsageResponse: newTokenUsageResponse(usage),
		})
	}
	sort.Slice(response.Models, func(i, j int) bool {
		left, right := response.Models[i], response.Models[j]
		if left.Role != right.Role {
			return left.Role < right.Role
		}
		if left.Model != right.Model {
			return left.Model < right.Model
		}
		return left.Effort < right.Effort
	})
	for key, usage := range phases {
		phase := phaseUsageResponse{
			Phase: key.phase, Role: key.role, tokenUsageResponse: newTokenUsageResponse(usage),
		}
		for split, splitUsage := range phaseModels {
			if split.phase == key {
				phase.Models = append(phase.Models, phaseModelUsageResponse{
					Model: split.model, Effort: split.effort, tokenUsageResponse: newTokenUsageResponse(splitUsage),
				})
			}
		}
		sort.Slice(phase.Models, func(i, j int) bool {
			if phase.Models[i].Model != phase.Models[j].Model {
				return phase.Models[i].Model < phase.Models[j].Model
			}
			return phase.Models[i].Effort < phase.Models[j].Effort
		})
		response.Phases = append(response.Phases, phase)
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
