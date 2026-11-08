package httpapi

import (
	"context"
	"errors"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/outputs"
)

// OutputService keeps the files the assistant's helpers make
// (HELPERS_PLAN.md).
type OutputService interface {
	List(ctx context.Context, owner string) ([]outputs.File, error)
	Path(ctx context.Context, owner, name string) (string, error)
	Delete(ctx context.Context, owner, name string) error
	DeleteProject(ctx context.Context, projectID string) error
}

type outputsResponse struct {
	Outputs []outputs.File `json:"outputs"`
}

// An owner is a project, or the top-level chats ("commitarium"), as with
// attachments.
func (api *API) registerOutputRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/outputs/{owner}", api.listOutputsHandler)
	mux.HandleFunc("GET /api/v1/outputs/{owner}/{name}", api.readOutputHandler)
	mux.HandleFunc("DELETE /api/v1/outputs/{owner}/{name}", api.deleteOutputHandler)
}

func (api *API) listOutputsHandler(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	if !api.attachmentOwner(w, r, owner) {
		return
	}
	files, err := api.outputs.List(r.Context(), owner)
	if err != nil {
		writeOutputError(w, owner, err)
		return
	}
	if files == nil {
		files = []outputs.File{}
	}
	writeJSON(w, http.StatusOK, outputsResponse{Outputs: files}, "outputs")
}

// outputTypes are the kinds the app shows itself; anything else is served
// as bytes to save.
var outputTypes = map[string]string{
	".md": "text/markdown; charset=utf-8", ".markdown": "text/markdown; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".csv": "text/csv; charset=utf-8",
	".json": "application/json", ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
}

// readOutputHandler answers an output itself. A helper's HTML or SVG never
// runs in the coordinator's origin: it is served under a sandbox policy,
// and the app shows it in a sandboxed frame.
func (api *API) readOutputHandler(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	if !api.attachmentOwner(w, r, owner) {
		return
	}
	path, err := api.outputs.Path(r.Context(), owner, name)
	if err != nil {
		writeOutputError(w, owner, err)
		return
	}
	kind, ok := outputTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		kind = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		if kind == "" || strings.HasPrefix(kind, "text/html") || strings.Contains(kind, "javascript") {
			kind = "application/octet-stream"
		}
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:")
	http.ServeFile(w, r, path)
}

func (api *API) deleteOutputHandler(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	if !api.attachmentOwner(w, r, owner) {
		return
	}
	if err := api.outputs.Delete(r.Context(), owner, name); err != nil {
		writeOutputError(w, owner, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeOutputError(w http.ResponseWriter, owner string, err error) {
	switch {
	case errors.Is(err, outputs.ErrNotFound):
		writeError(w, http.StatusNotFound, "output_not_found", "no such file")
	case errors.Is(err, outputs.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_output", "no such file")
	default:
		log.Printf("outputs of %q: %v", owner, err)
		writeError(w, http.StatusServiceUnavailable, "outputs_unavailable", "the files could not be read")
	}
}

// looseAgentsAtWorkHandler lists the helpers at work for the chats outside
// any project, which have no runs.
func (api *API) looseAgentsAtWorkHandler(w http.ResponseWriter, r *http.Request) {
	agents := api.helperAgents(r.Context(), conversations.TopLevel)
	api.nameAgents(r.Context(), agents)
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].Working && !agents[j].Working })
	writeJSON(w, http.StatusOK, agentsAtWorkResponse{Agents: agents}, "agents at work")
}
