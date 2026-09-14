package httpapi

import (
	"log"
	"net/http"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type modelCatalogResponse struct {
	Catalogs []modelCatalogEntryResponse `json:"catalogs"`
}

type modelCatalogEntryResponse struct {
	Provider  project.AgentProvider `json:"provider"`
	Role      modelcatalog.Role     `json:"role"`
	Models    []workerhttp.Model    `json:"models"`
	FetchedAt *time.Time            `json:"fetched_at,omitempty"`
	LastError string                `json:"last_error,omitempty"`
}

func (api *API) listModelsHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, newModelCatalogResponse(api.modelCatalog.List(r.Context())), "model catalog")
}

func (api *API) refreshModelsHandler(w http.ResponseWriter, r *http.Request) {
	catalogs, err := api.modelCatalog.Refresh(r.Context())
	if err != nil {
		log.Printf("refresh worker model catalogs: %v", err)
	}
	writeJSON(w, http.StatusOK, newModelCatalogResponse(catalogs), "model catalog")
}

func newModelCatalogResponse(catalogs []modelcatalog.Catalog) modelCatalogResponse {
	entries := make([]modelCatalogEntryResponse, len(catalogs))
	for index, catalog := range catalogs {
		entries[index] = modelCatalogEntryResponse{
			Provider: catalog.Provider, Role: catalog.Role, Models: catalog.Models,
			FetchedAt: catalog.FetchedAt, LastError: catalog.LastError,
		}
	}
	return modelCatalogResponse{Catalogs: entries}
}
