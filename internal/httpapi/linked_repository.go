package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/forgejo"
	"github.com/EinarLogiOskars/commitarium/internal/linkedrepo"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// LinkedRepositories links projects to a repository on GitHub or another
// git host, to pull from and push to (SERVER_COMPLETE_PLAN.md, S2).
type LinkedRepositories interface {
	Get(projectID string) (linkedrepo.View, error)
	Link(ctx context.Context, projectID, url, username, token string) error
	Unlink(projectID string) error
	Pull(ctx context.Context, projectID string) (linkedrepo.Pulled, error)
	Push(ctx context.Context, projectID, branch string) (linkedrepo.Pushed, error)
}

type linkRequest struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

// RegisterLinkedRepositoryRoutes adds a project's linked-repository routes.
func RegisterLinkedRepositoryRoutes(mux *http.ServeMux, linked LinkedRepositories) {
	path := "/api/v1/projects/{id}/linked-repository"
	fail := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, linkedrepo.ErrNotLinked):
			writeError(w, http.StatusNotFound, "not_linked", err.Error())
		case errors.Is(err, project.ErrNotFound):
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
		case errors.Is(err, linkedrepo.ErrInvalidLink), errors.Is(err, linkedrepo.ErrNoToken),
			errors.Is(err, linkedrepo.ErrInvalidBranch):
			writeError(w, http.StatusBadRequest, "invalid_link", err.Error())
		case errors.Is(err, forgejo.ErrNotFastForward), errors.Is(err, linkedrepo.ErrNoRepository):
			writeError(w, http.StatusConflict, "not_in_step", err.Error())
		default:
			log.Printf("linked repository: %v", err)
			writeError(w, http.StatusBadGateway, "sync_failed", err.Error())
		}
	}

	mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		view, err := linked.Get(r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, view, "linked repository")
	})
	mux.HandleFunc("PUT "+path, func(w http.ResponseWriter, r *http.Request) {
		var request linkRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the link could not be read")
			return
		}
		if err := linked.Link(r.Context(), r.PathValue("id"), request.URL, request.Username, request.Token); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE "+path, func(w http.ResponseWriter, r *http.Request) {
		if err := linked.Unlink(r.PathValue("id")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST "+path+"/pull", func(w http.ResponseWriter, r *http.Request) {
		pulled, err := linked.Pull(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, pulled, "pull")
	})
	mux.HandleFunc("POST "+path+"/push", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Branch string `json:"branch"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the push could not be read")
			return
		}
		pushed, err := linked.Push(r.Context(), r.PathValue("id"), request.Branch)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, pushed, "push")
	})
}
