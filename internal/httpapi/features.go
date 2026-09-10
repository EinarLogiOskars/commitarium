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
	Title       string `json:"title"`
	Description string `json:"description"`
}

type featureResponse struct {
	ID             string        `json:"id"`
	ProjectID      string        `json:"project_id"`
	Title          string        `json:"title"`
	Description    string        `json:"description"`
	State          feature.State `json:"state"`
	AcceptedGoal   string        `json:"accepted_goal,omitempty"`
	GoalAcceptedAt *time.Time    `json:"goal_accepted_at,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
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
	createdFeature, err := api.features.Create(
		r.Context(),
		projectID,
		request.Title,
		request.Description,
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
	return featureResponse{
		ID:             storedFeature.ID,
		ProjectID:      storedFeature.ProjectID,
		Title:          storedFeature.Title,
		Description:    storedFeature.Description,
		State:          storedFeature.State,
		AcceptedGoal:   storedFeature.AcceptedGoal,
		GoalAcceptedAt: storedFeature.GoalAcceptedAt,
		CreatedAt:      storedFeature.CreatedAt,
		UpdatedAt:      storedFeature.UpdatedAt,
	}
}
