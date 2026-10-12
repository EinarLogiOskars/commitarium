package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type runModelChanger interface {
	ChangeModels(ctx context.Context, runID string, models project.AgentModels, now bool) (execution.Run, error)
}

type changeRunModelsRequest struct {
	LeadModel     string `json:"lead_model,omitempty"`
	ReviewerModel string `json:"reviewer_model,omitempty"`
	// When is "now" (stop the agent and resume it on the new model) or
	// "next_turn" (let the current turn finish).
	When string `json:"when"`
}

// changeRunModelsHandler switches a working run's models, so a wrong model
// does not need a reset. Only models change: each role keeps its agent.
func (api *API) changeRunModelsHandler(w http.ResponseWriter, r *http.Request) {
	changer, ok := api.realWorkflow.(runModelChanger)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "model_change_unavailable", "changing models needs real agents")
		return
	}
	var body changeRunModelsRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	if body.When != "now" && body.When != "next_turn" {
		writeError(w, http.StatusBadRequest, "invalid_model_change", "when must be now or next_turn")
		return
	}
	runID := r.PathValue("id")
	run, err := api.execution.GetRun(r.Context(), runID)
	if err != nil {
		writeRunModelsError(w, runID, err)
		return
	}
	models := project.AgentModels{
		Lead: strings.TrimSpace(body.LeadModel), Reviewer: strings.TrimSpace(body.ReviewerModel),
	}
	if models.Lead == "" && models.Reviewer == "" {
		writeError(w, http.StatusBadRequest, "invalid_model_change", "name a lead or reviewer model")
		return
	}
	if api.modelCatalog != nil {
		selection := run.AgentModels
		if models.Lead != "" {
			selection.Lead = models.Lead
		}
		if models.Reviewer != "" {
			selection.Reviewer = models.Reviewer
		}
		if err := api.modelCatalog.ValidateSelection(r.Context(), run.AgentProviders, selection); err != nil {
			writeModelSelectionError(w, err)
			return
		}
	}
	changed, err := changer.ChangeModels(r.Context(), runID, models, body.When == "now")
	if err != nil {
		writeRunModelsError(w, runID, err)
		return
	}
	api.writeRun(w, r, http.StatusOK, changed)
}

func writeRunModelsError(w http.ResponseWriter, runID string, err error) {
	switch {
	case errors.Is(err, execution.ErrNotFound):
		writeError(w, http.StatusNotFound, "run_not_found", "run not found")
	case errors.Is(err, orchestration.ErrInvalidRunRequest), errors.Is(err, execution.ErrInvalidRun):
		writeError(w, http.StatusConflict, "run_not_changeable", "the run has ended")
	case errors.Is(err, orchestration.ErrModelChangeUnavailable), errors.Is(err, execution.ErrModelChangeUnsupported):
		writeError(w, http.StatusServiceUnavailable, "model_change_unavailable", "changing models is unavailable")
	default:
		log.Printf("change models of run %q: %v", runID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
