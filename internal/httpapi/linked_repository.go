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

// LinkedRepositories keeps projects in step with a repository on GitHub or
// another git host (SERVER_COMPLETE_PLAN.md, S2).
type LinkedRepositories interface {
	Get(ctx context.Context, projectID string) (linkedrepo.View, error)
	Link(ctx context.Context, projectID, url, username, token string) error
	Unlink(projectID string) error
	SetAutomatic(projectID string, automatic bool) error
	BringIn(ctx context.Context, projectID string) (linkedrepo.Comparison, error)
	Push(ctx context.Context, projectID string) (linkedrepo.Comparison, error)
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
		case errors.Is(err, linkedrepo.ErrInvalidLink), errors.Is(err, linkedrepo.ErrNoToken):
			writeError(w, http.StatusBadRequest, "invalid_link", err.Error())
		case errors.Is(err, linkedrepo.ErrTheirsAhead), errors.Is(err, forgejo.ErrNotFastForward),
			errors.Is(err, linkedrepo.ErrNoRepository):
			writeError(w, http.StatusConflict, "not_in_step", err.Error())
		default:
			log.Printf("linked repository: %v", err)
			writeError(w, http.StatusBadGateway, "sync_failed", err.Error())
		}
	}
	answer := func(w http.ResponseWriter, comparison linkedrepo.Comparison, err error) {
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, comparison, "comparison")
	}

	mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		view, err := linked.Get(r.Context(), r.PathValue("id"))
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
	mux.HandleFunc("PUT "+path+"/automatic", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Automatic bool `json:"automatic"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the setting could not be read")
			return
		}
		if err := linked.SetAutomatic(r.PathValue("id"), request.Automatic); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST "+path+"/bring-in", func(w http.ResponseWriter, r *http.Request) {
		comparison, err := linked.BringIn(r.Context(), r.PathValue("id"))
		answer(w, comparison, err)
	})
	mux.HandleFunc("POST "+path+"/push", func(w http.ResponseWriter, r *http.Request) {
		comparison, err := linked.Push(r.Context(), r.PathValue("id"))
		answer(w, comparison, err)
	})
}
