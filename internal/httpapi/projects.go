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
	ID                string                     `json:"id"`
	Name              string                     `json:"name"`
	RecoveryPolicy    project.RecoveryPolicy     `json:"recovery_policy"`
	ForgejoRepository *forgejoRepositoryResponse `json:"forgejo_repository,omitempty"`
	CreatedAt         time.Time                  `json:"created_at"`
}

type forgejoRepositoryResponse struct {
	Owner         string    `json:"owner"`
	Name          string    `json:"name"`
	DefaultBranch string    `json:"default_branch"`
	BoundAt       time.Time `json:"bound_at"`
}

type bindForgejoRepositoryRequest struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
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

	w.Header().Set(
		"Location",
		"/api/v1/projects/"+createdProject.ID,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(newProjectResponse(createdProject)); err != nil {
		log.Printf("encode project response: %v", err)
	}
}

func (api *API) listProjectsHandler(w http.ResponseWriter, r *http.Request) {
	projects, err := api.projects.List(r.Context())
	if err != nil {
		log.Printf("list projects: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	response := make([]projectResponse, 0, len(projects))
	for _, storedProject := range projects {
		response = append(response, newProjectResponse(storedProject))
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode project list response: %v", err)
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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(newProjectResponse(foundProject)); err != nil {
		log.Printf("encode project response: %v", err)
	}
}

func (api *API) bindForgejoRepositoryHandler(w http.ResponseWriter, r *http.Request) {
	request := bindForgejoRepositoryRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	projectID := r.PathValue("id")
	boundProject, err := api.projects.BindForgejoRepository(
		r.Context(), projectID, request.Owner, request.Name,
	)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrNotFound):
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
		case errors.Is(err, project.ErrForgejoOwnerRequired):
			writeError(w, http.StatusBadRequest, "forgejo_owner_required", "Forgejo repository owner is required")
		case errors.Is(err, project.ErrForgejoRepositoryNameRequired):
			writeError(w, http.StatusBadRequest, "forgejo_repository_name_required", "Forgejo repository name is required")
		case errors.Is(err, project.ErrInvalidForgejoRepositoryCoordinate):
			writeError(w, http.StatusBadRequest, "invalid_forgejo_repository", "Forgejo repository owner and name must not contain slashes")
		case errors.Is(err, project.ErrForgejoRepositoryNotFound):
			writeError(w, http.StatusNotFound, "forgejo_repository_not_found", "Forgejo repository not found")
		case errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusConflict, "forgejo_repository_not_ready", "Forgejo repository must be non-empty, unarchived, and have a default branch")
		case errors.Is(err, project.ErrForgejoRepositoryAlreadyBound):
			writeError(w, http.StatusConflict, "forgejo_repository_already_bound", "project already has a different Forgejo repository")
		case errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "forgejo_unavailable", "Forgejo repository verification is unavailable")
		default:
			log.Printf("bind Forgejo repository to project %q: %v", projectID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(newProjectResponse(boundProject)); err != nil {
		log.Printf("encode bound project response: %v", err)
	}
}

func newProjectResponse(storedProject project.Project) projectResponse {
	response := projectResponse{
		ID: storedProject.ID, Name: storedProject.Name,
		RecoveryPolicy: storedProject.RecoveryPolicy, CreatedAt: storedProject.CreatedAt,
	}
	if storedProject.ForgejoRepository != nil {
		response.ForgejoRepository = &forgejoRepositoryResponse{
			Owner:         storedProject.ForgejoRepository.Owner,
			Name:          storedProject.ForgejoRepository.Name,
			DefaultBranch: storedProject.ForgejoRepository.DefaultBranch,
			BoundAt:       storedProject.ForgejoRepository.BoundAt,
		}
	}
	return response
}
