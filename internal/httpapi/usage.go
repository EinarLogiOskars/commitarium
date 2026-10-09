package httpapi

import (
	"errors"
	"log"
	"net/http"

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

type featureUsageResponse struct {
	Roles []roleUsageResponse `json:"roles"`
	Total tokenUsageResponse  `json:"total"`
}

func newTokenUsageResponse(usage execution.TokenUsage) tokenUsageResponse {
	return tokenUsageResponse{
		InputTokens:       usage.InputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		CacheWriteTokens:  usage.CacheWriteTokens,
		OutputTokens:      usage.OutputTokens,
	}
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
	roles, err := api.usage.FeatureUsageByRole(r.Context(), featureID)
	if err != nil {
		log.Printf("total token usage for feature %q: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	response := featureUsageResponse{Roles: make([]roleUsageResponse, 0, len(roles))}
	total := execution.TokenUsage{}
	for _, role := range roles {
		response.Roles = append(response.Roles, roleUsageResponse{
			Role: role.Role, tokenUsageResponse: newTokenUsageResponse(role.Usage),
		})
		total = total.Add(role.Usage)
	}
	response.Total = newTokenUsageResponse(total)
	writeJSON(w, http.StatusOK, response, "feature token usage")
}
