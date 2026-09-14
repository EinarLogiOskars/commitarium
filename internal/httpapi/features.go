package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type createFeatureRequest struct {
	Title          string                  `json:"title"`
	Description    string                  `json:"description"`
	AgentProviders *agentProvidersRequest  `json:"agent_providers"`
	AgentModels    *agentModelsRequest     `json:"agent_models"`
	AutonomyPolicy *project.AutonomyPolicy `json:"autonomy_policy"`
	MergePolicy    *project.MergePolicy    `json:"merge_policy"`
	DialogueLimits *dialogueLimitsRequest  `json:"dialogue_limits"`
}

type featureResponse struct {
	ID             string                 `json:"id"`
	ProjectID      string                 `json:"project_id"`
	Title          string                 `json:"title"`
	Description    string                 `json:"description"`
	State          feature.State          `json:"state"`
	AcceptedGoal   string                 `json:"accepted_goal,omitempty"`
	GoalAcceptedAt *time.Time             `json:"goal_accepted_at,omitempty"`
	DialogueLimits dialogueLimitsResponse `json:"dialogue_limits"`
	AgentProviders agentProvidersResponse `json:"agent_providers"`
	AgentModels    agentModelsResponse    `json:"agent_models"`
	MergePolicy    project.MergePolicy    `json:"merge_policy"`
	AutonomyPolicy project.AutonomyPolicy `json:"autonomy_policy"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

func (api *API) createFeatureHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	request := createFeatureRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_json",
			"request body must contain valid JSON",
		)
		return
	}

	projectID := r.PathValue("projectID")
	var overrides feature.SettingsOverrides
	if request.DialogueLimits != nil {
		limits, err := decodeDialogueLimits(request.DialogueLimits, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_dialogue_limits", "dialogue_limits must include non-negative planning_rounds and implementation_review_rounds; zero means unlimited")
			return
		}
		overrides.DialogueLimits = &limits
	}
	if request.AgentProviders != nil {
		providers, err := decodeAgentProviders(request.AgentProviders, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_agent_providers", "agent_providers must include lead and reviewer set to codex or claude")
			return
		}
		overrides.AgentProviders = &providers
	}
	if request.AgentModels != nil {
		models, err := decodeAgentModels(request.AgentModels, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_agent_models", "agent_models must include explicit lead and reviewer model IDs")
			return
		}
		overrides.AgentModels = &models
	}
	overrides.MergePolicy = request.MergePolicy
	overrides.AutonomyPolicy = request.AutonomyPolicy
	if api.modelCatalog != nil {
		storedProject, err := api.projects.GetByID(r.Context(), projectID)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) {
				writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			} else {
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
			return
		}
		effectiveProviders := storedProject.AgentProviders
		if overrides.AgentProviders != nil {
			effectiveProviders = *overrides.AgentProviders
		}
		effectiveModels := storedProject.AgentModels
		if overrides.AgentModels != nil {
			effectiveModels = *overrides.AgentModels
		}
		if err := api.modelCatalog.ValidateSelection(r.Context(), effectiveProviders, effectiveModels); err != nil {
			writeModelSelectionError(w, err)
			return
		}
	}
	createdFeature, err := api.features.Create(
		r.Context(),
		projectID,
		request.Title,
		request.Description,
		overrides,
	)
	if err != nil {
		switch {
		case errors.Is(err, feature.ErrTitleRequired):
			writeError(
				w,
				http.StatusBadRequest,
				"feature_title_required",
				"feature title is required",
			)
		case errors.Is(err, project.ErrNotFound):
			writeError(
				w,
				http.StatusNotFound,
				"project_not_found",
				"project not found",
			)
		case errors.Is(err, project.ErrInvalidAgentProviders):
			writeError(w, http.StatusBadRequest, "invalid_agent_providers", "agent_providers must include lead and reviewer set to codex or claude")
		case errors.Is(err, project.ErrInvalidAgentModels):
			writeError(w, http.StatusBadRequest, "invalid_agent_models", "effective lead and reviewer models must be explicit model IDs; configure project defaults or provide agent_models")
		case errors.Is(err, project.ErrInvalidAutonomyPolicy):
			writeError(w, http.StatusBadRequest, "invalid_autonomy_policy", "autonomy_policy must be review_each_phase or run_to_completion")
		case errors.Is(err, project.ErrInvalidMergePolicy):
			writeError(w, http.StatusBadRequest, "invalid_merge_policy", "merge_policy must be require_user_approval or auto_after_gates")
		case errors.Is(err, project.ErrInvalidDialogueLimits):
			writeError(w, http.StatusBadRequest, "invalid_dialogue_limits", "dialogue limits must be non-negative; zero means unlimited")
		default:
			log.Printf(
				"create feature for project %q: %v",
				projectID,
				err,
			)
			writeError(
				w,
				http.StatusInternalServerError,
				"internal_error",
				"internal server error",
			)
		}
		return
	}

	response := newFeatureResponse(createdFeature)
	w.Header().Set(
		"Location",
		"/api/v1/projects/"+projectID+"/features/"+createdFeature.ID,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode feature response: %v", err)
	}
}

func (api *API) listFeaturesHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	features, err := api.features.List(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		log.Printf("list features for project %q: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	response := make([]featureResponse, 0, len(features))
	for _, storedFeature := range features {
		response = append(response, newFeatureResponse(storedFeature))
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode feature list response: %v", err)
	}
}

func (api *API) getFeatureByIDHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	projectID := r.PathValue("projectID")
	id := r.PathValue("id")

	storedFeature, err := api.features.GetByID(
		r.Context(),
		projectID,
		id,
	)
	if err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(
				w,
				http.StatusNotFound,
				"feature_not_found",
				"feature not found",
			)
			return
		}

		log.Printf(
			"get feature %q for project %q: %v",
			id,
			projectID,
			err,
		)
		writeError(
			w,
			http.StatusInternalServerError,
			"internal_error",
			"internal server error",
		)
		return
	}

	response := newFeatureResponse(storedFeature)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode feature response: %v", err)
	}
}

func newFeatureResponse(storedFeature feature.Feature) featureResponse {
	agentProviders, err := storedFeature.AgentProviders.Normalize()
	if err != nil {
		agentProviders = storedFeature.AgentProviders
	}
	mergePolicy, err := project.NormalizeMergePolicy(storedFeature.MergePolicy)
	if err != nil {
		mergePolicy = storedFeature.MergePolicy
	}
	autonomyPolicy, err := project.NormalizeAutonomyPolicy(storedFeature.AutonomyPolicy)
	if err != nil {
		autonomyPolicy = storedFeature.AutonomyPolicy
	}
	return featureResponse{
		ID:             storedFeature.ID,
		ProjectID:      storedFeature.ProjectID,
		Title:          storedFeature.Title,
		Description:    storedFeature.Description,
		State:          storedFeature.State,
		AcceptedGoal:   storedFeature.AcceptedGoal,
		GoalAcceptedAt: storedFeature.GoalAcceptedAt,
		DialogueLimits: dialogueLimitsResponse{
			PlanningRounds:             storedFeature.DialogueLimits.PlanningRounds,
			ImplementationReviewRounds: storedFeature.DialogueLimits.ImplementationReviewRounds,
		},
		AgentProviders: agentProvidersResponse{Lead: agentProviders.Lead, Reviewer: agentProviders.Reviewer},
		AgentModels:    agentModelsResponse{Lead: storedFeature.AgentModels.Lead, Reviewer: storedFeature.AgentModels.Reviewer},
		MergePolicy:    mergePolicy,
		AutonomyPolicy: autonomyPolicy,
		CreatedAt:      storedFeature.CreatedAt,
		UpdatedAt:      storedFeature.UpdatedAt,
	}
}
