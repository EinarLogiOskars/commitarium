package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

type createFeatureRequest struct {
	Title                      string                  `json:"title"`
	Description                string                  `json:"description"`
	AgentProviders             *agentProvidersRequest  `json:"agent_providers"`
	AgentModels                *agentModelsRequest     `json:"agent_models"`
	AutonomyPolicy             *project.AutonomyPolicy `json:"autonomy_policy"`
	MergePolicy                *project.MergePolicy    `json:"merge_policy"`
	DialogueLimits             *dialogueLimitsRequest  `json:"dialogue_limits"`
	IndependentAcceptanceTests *bool                   `json:"independent_acceptance_tests"`
	// Kind is "work_order" (the default) or "session" (ADR-019).
	Kind feature.Kind `json:"kind"`
}

type featureResponse struct {
	ID                         string                 `json:"id"`
	ProjectID                  string                 `json:"project_id"`
	Title                      string                 `json:"title"`
	Description                string                 `json:"description"`
	State                      feature.State          `json:"state"`
	AcceptedGoal               string                 `json:"accepted_goal,omitempty"`
	GoalAcceptedAt             *time.Time             `json:"goal_accepted_at,omitempty"`
	DialogueLimits             dialogueLimitsResponse `json:"dialogue_limits"`
	AgentProviders             agentProvidersResponse `json:"agent_providers"`
	AgentModels                agentModelsResponse    `json:"agent_models"`
	MergePolicy                project.MergePolicy    `json:"merge_policy"`
	AutonomyPolicy             project.AutonomyPolicy `json:"autonomy_policy"`
	IndependentAcceptanceTests bool                   `json:"independent_acceptance_tests"`
	Steps                      workStepsResponse      `json:"steps"`
	Kind                       feature.Kind           `json:"kind"`
	// Background marks a work order Commitarium runs by itself, such as the
	// preview setup.
	Background bool `json:"background,omitempty"`
	// CreatedBy is "user" for a work order the user made by hand, which the
	// assistant never acts on by itself, or "assistant" (ADR-027).
	CreatedBy feature.Creator `json:"created_by"`
	// PreviewCheck marks a work order that merges only once Commitarium's
	// preview check of its commit passed (ADR-028).
	PreviewCheck bool `json:"preview_check,omitempty"`
	// Session reports a session's agent activity in feature lists (ADR-019).
	Session   *sessionActivityResponse `json:"session,omitempty"`
	CreatedAt time.Time                `json:"created_at"`
	UpdatedAt time.Time                `json:"updated_at"`
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
	// A work order may be created without agents; they are then chosen at
	// Start (ADR-027). Models name no agent on their own.
	fillSkippedReviewer(request.AgentProviders, request.AgentModels, request.Kind == feature.KindSession)
	if request.AgentModels != nil && request.AgentProviders == nil {
		writeError(w, http.StatusBadRequest, "agents_not_chosen", "agent_models need agent_providers naming the agents that run them")
		return
	}
	if request.AgentProviders != nil {
		providers, err := api.decodeAgentProviders(r.Context(), request.AgentProviders, false)
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
	overrides.IndependentAcceptanceTests = request.IndependentAcceptanceTests
	overrides.Kind = request.Kind
	if api.toolchains != nil {
		manifest, err := api.projectToolchain(r.Context(), projectID)
		if err != nil {
			api.writeToolchainError(w, projectID, "check before work-order creation", err)
			return
		}
		if manifest.Status != toolchain.StatusConfigured {
			writeError(w, http.StatusConflict, "project_toolchain_required", "choose or detect the project toolchain before creating a work order")
			return
		}
	}
	if overrides.AgentProviders != nil && overrides.AgentModels != nil {
		providers, models := *overrides.AgentProviders, *overrides.AgentModels
		err := api.modelOffered(r.Context(), providers.LeadAgent, providers.Lead, modelcatalog.RoleLead, models.Lead)
		if err == nil && request.Kind != feature.KindSession {
			err = api.modelOffered(r.Context(), providers.ReviewerAgent, providers.Reviewer, modelcatalog.RoleReviewer, models.Reviewer)
		}
		if err != nil {
			writeChoiceError(w, err)
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
		case errors.Is(err, feature.ErrInvalidKind):
			writeError(w, http.StatusBadRequest, "invalid_kind", "kind must be work_order or session")
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
			writeError(w, http.StatusBadRequest, "invalid_agent_models", "lead and reviewer models must be explicit model IDs")
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

	if createdFeature.Kind == feature.KindSession && api.starter != nil {
		// Creating a session starts it (ADR-019): its branch and pull request
		// open and it waits for the user's first message.
		if _, ok := api.startFeature(w, r, createdFeature, "session-"+createdFeature.ID, nil); !ok {
			return
		}
		if started, err := api.features.GetByID(r.Context(), projectID, createdFeature.ID); err == nil {
			createdFeature = started
		}
	}
	// Building everything, the assistant takes over what the user adds.
	api.wakeForUsersWorkOrder(r.Context(), createdFeature)
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
		item := newFeatureResponse(storedFeature)
		if storedFeature.Kind == feature.KindSession {
			item.Session = api.sessionActivity(r.Context(), storedFeature.ID)
		}
		response = append(response, item)
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
	agentProviders, err := storedFeature.AgentProviders.NormalizeChosen()
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
		AgentProviders:             newAgentProvidersResponse(agentProviders),
		AgentModels:                agentModelsResponse{Lead: storedFeature.AgentModels.Lead, Reviewer: storedFeature.AgentModels.Reviewer},
		MergePolicy:                mergePolicy,
		AutonomyPolicy:             autonomyPolicy,
		IndependentAcceptanceTests: storedFeature.IndependentAcceptanceTests,
		Steps:                      newWorkStepsResponse(storedFeature.Steps),
		Kind:                       featureKind(storedFeature.Kind),
		Background:                 storedFeature.Background,
		CreatedBy:                  featureCreator(storedFeature.CreatedBy),
		PreviewCheck:               storedFeature.PreviewCheck,
		CreatedAt:                  storedFeature.CreatedAt,
		UpdatedAt:                  storedFeature.UpdatedAt,
	}
}

// featureCreator reports who created a work order; one without a stored
// creator is the user's.
func featureCreator(creator feature.Creator) feature.Creator {
	if creator == feature.CreatedByAssistant {
		return creator
	}
	return feature.CreatedByUser
}

// featureKind reports a work order's kind; one without a stored kind is a work
// order.
func featureKind(kind feature.Kind) feature.Kind {
	if kind == "" {
		return feature.KindWorkOrder
	}
	return kind
}

// sessionActivityResponse is what a session's agent is doing: working on a
// message, or waiting since its last reply.
type sessionActivityResponse struct {
	Working   bool      `json:"working"`
	UpdatedAt time.Time `json:"updated_at"`
}

// sessionActivity reads a session's newest run; nil when it has none or the
// runs cannot be read, which lists show as idle.
func (api *API) sessionActivity(ctx context.Context, featureID string) *sessionActivityResponse {
	if api.execution == nil {
		return nil
	}
	runs, err := api.execution.RunsForFeature(ctx, featureID)
	if err != nil || len(runs) == 0 {
		return nil
	}
	run := newestRun(runs)
	return &sessionActivityResponse{Working: run.Status == execution.RunStatusRunning, UpdatedAt: run.UpdatedAt}
}
