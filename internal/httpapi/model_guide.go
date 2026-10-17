package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
)

// ModelGuideService is the official model guide merged with the user's
// overrides (ADR-031).
type ModelGuideService interface {
	AsOf() string
	Entries(ctx context.Context, extra []string) ([]modelguide.Entry, error)
	SetTier(ctx context.Context, id string, tier modelguide.Tier) (modelguide.Entry, error)
	Reset(ctx context.Context, id string) (modelguide.Entry, error)
}

type modelGuideResponse struct {
	AsOf   string             `json:"as_of"`
	Models []modelguide.Entry `json:"models"`
}

type modelGuideTierRequest struct {
	Tier modelguide.Tier `json:"tier"`
}

// getModelGuideHandler lists every model the official guide rates, the user
// rated, or a connected agent offers, with its tier.
func (api *API) getModelGuideHandler(w http.ResponseWriter, r *http.Request) {
	offered := make([]string, 0)
	if api.modelCatalog != nil {
		for _, catalog := range api.modelCatalog.List(r.Context()) {
			for _, model := range catalog.Models {
				offered = append(offered, model.ID)
			}
		}
	}
	entries, err := api.modelGuide.Entries(r.Context(), offered)
	if err != nil {
		log.Printf("list the model guide: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, modelGuideResponse{AsOf: api.modelGuide.AsOf(), Models: entries}, "model guide")
}

// setModelGuideTierHandler rates a model for the user. Setting the tier it
// already has keeps it after the official tier changed.
func (api *API) setModelGuideTierHandler(w http.ResponseWriter, r *http.Request) {
	var body modelGuideTierRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	entry, err := api.modelGuide.SetTier(r.Context(), r.PathValue("model"), body.Tier)
	writeModelGuideEntry(w, entry, err)
}

// resetModelGuideTierHandler returns a model to the official guide.
func (api *API) resetModelGuideTierHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	entry, err := api.modelGuide.Reset(r.Context(), r.PathValue("model"))
	writeModelGuideEntry(w, entry, err)
}

func writeModelGuideEntry(w http.ResponseWriter, entry modelguide.Entry, err error) {
	switch {
	case errors.Is(err, modelguide.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_model_guide_request", err.Error())
	case err != nil:
		log.Printf("change the model guide: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	default:
		writeJSON(w, http.StatusOK, entry, "model guide entry")
	}
}
