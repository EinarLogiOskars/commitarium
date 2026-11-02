package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/forgejo"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// RepositoryMirrors keeps a project's code in step on a git remote such as
// GitHub, through Forgejo's push mirrors (SERVER_COMPLETE_PLAN.md, S2).
type RepositoryMirrors interface {
	ListPushMirrors(ctx context.Context, owner, name string) ([]forgejo.PushMirror, error)
	AddPushMirror(ctx context.Context, owner, name, remoteURL, username, token string) error
	SyncPushMirrors(ctx context.Context, owner, name string) error
	RemovePushMirror(ctx context.Context, owner, name, mirror string) error
}

type projectGetter interface {
	GetByID(ctx context.Context, id string) (project.Project, error)
}

type addMirrorRequest struct {
	RemoteURL string `json:"remote_url"`
	Username  string `json:"username"`
	Token     string `json:"token"`
}

// RegisterMirrorRoutes adds a project's push-mirror routes to mux.
func RegisterMirrorRoutes(mux *http.ServeMux, projects projectGetter, mirrors RepositoryMirrors) {
	repository := func(w http.ResponseWriter, r *http.Request) (*project.ForgejoRepository, bool) {
		found, err := projects.GetByID(r.Context(), r.PathValue("id"))
		if errors.Is(err, project.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			return nil, false
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "project_unavailable", "the project could not be read")
			return nil, false
		}
		if found.ForgejoRepository == nil {
			writeError(w, http.StatusConflict, "no_repository", "the project has no repository yet")
			return nil, false
		}
		return found.ForgejoRepository, true
	}
	fail := func(w http.ResponseWriter, err error) {
		if errors.Is(err, forgejo.ErrInvalidMirror) || errors.Is(err, project.ErrForgejoUnavailable) {
			status := http.StatusBadGateway
			if errors.Is(err, forgejo.ErrInvalidMirror) {
				status = http.StatusBadRequest
			}
			writeError(w, status, "mirror_failed", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "mirror_failed", "the mirror could not be changed")
	}

	mux.HandleFunc("GET /api/v1/projects/{id}/mirrors", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repository(w, r)
		if !ok {
			return
		}
		found, err := mirrors.ListPushMirrors(r.Context(), repo.Owner, repo.Name)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mirrors": found}, "mirrors")
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/mirrors", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repository(w, r)
		if !ok {
			return
		}
		var request addMirrorRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the mirror request could not be read")
			return
		}
		if err := mirrors.AddPushMirror(r.Context(), repo.Owner, repo.Name,
			request.RemoteURL, request.Username, request.Token); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/v1/projects/{id}/mirrors/sync", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repository(w, r)
		if !ok {
			return
		}
		if err := mirrors.SyncPushMirrors(r.Context(), repo.Owner, repo.Name); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/v1/projects/{id}/mirrors/{name}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repository(w, r)
		if !ok {
			return
		}
		if err := mirrors.RemovePushMirror(r.Context(), repo.Owner, repo.Name, r.PathValue("name")); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
