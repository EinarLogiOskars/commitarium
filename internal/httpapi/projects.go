package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type createProjectRequest struct {
	Name           string                 `json:"name"`
	RecoveryPolicy project.RecoveryPolicy `json:"recovery_policy"`
}

type projectResponse struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	RecoveryPolicy project.RecoveryPolicy `json:"recovery_policy"`
	CreatedAt      time.Time              `json:"created_at"`
}

func (api *API) createProjectHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	request := createProjectRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_json",
			"request body must contain valid JSON",
		)
		return
	}

	createdProject, err := api.projects.Create(
		r.Context(),
		request.Name,
		request.RecoveryPolicy,
	)
	if err != nil {
		if errors.Is(err, project.ErrNameRequired) {
			writeError(
				w,
				http.StatusBadRequest,
				"project_name_required",
				"project name is required",
			)
			return
		}
		if errors.Is(err, project.ErrInvalidRecoveryPolicy) {
			writeError(
				w,
				http.StatusBadRequest,
				"invalid_recovery_policy",
				"recovery_policy must be approval_required or automatic",
			)
			return
		}
		log.Printf("create project: %v", err)
		writeError(
			w,
			http.StatusInternalServerError,
			"internal_error",
			"internal server error",
		)
		return
	}

	response := projectResponse{
		ID:             createdProject.ID,
		Name:           createdProject.Name,
		RecoveryPolicy: createdProject.RecoveryPolicy,
		CreatedAt:      createdProject.CreatedAt,
	}

	w.Header().Set(
		"Location",
		"/api/v1/projects/"+createdProject.ID,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode project response: %v", err)
	}
}

func (api *API) getProjectByIDHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	id := r.PathValue("id")

	foundProject, err := api.projects.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(
				w,
				http.StatusNotFound,
				"project_not_found",
				"project not found",
			)
			return
		}
		log.Printf("get project %q: %v", id, err)

		writeError(
			w,
			http.StatusInternalServerError,
			"internal_error",
			"internal server error",
		)
		return
	}

	response := projectResponse{
		ID:             foundProject.ID,
		Name:           foundProject.Name,
		RecoveryPolicy: foundProject.RecoveryPolicy,
		CreatedAt:      foundProject.CreatedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode project response: %v", err)
	}
}
