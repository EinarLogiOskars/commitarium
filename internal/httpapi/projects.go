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
	Name string `json:"name"`
}

type projectResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
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
		ID:        createdProject.ID,
		Name:      createdProject.Name,
		CreatedAt: createdProject.CreatedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode project response: %v", err)
	}
}
