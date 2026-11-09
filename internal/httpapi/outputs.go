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

// outputTypes are the kinds the app shows itself, and shownTypes those
// served inline; the rest are files to open in another app or save, and
// anything unknown is bytes.
var outputTypes = map[string]string{
	".md": "text/markdown; charset=utf-8", ".markdown": "text/markdown; charset=utf-8",
	".txt": "text/plain; charset=utf-8", ".csv": "text/csv; charset=utf-8",
	".json": "application/json", ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".svg": "image/svg+xml",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".doc":  "application/msword", ".ppt": "application/vnd.ms-powerpoint", ".xls": "application/vnd.ms-excel",
	".odt": "application/vnd.oasis.opendocument.text", ".odp": "application/vnd.oasis.opendocument.presentation",
	".ods": "application/vnd.oasis.opendocument.spreadsheet", ".rtf": "application/rtf",
	".key": "application/vnd.apple.keynote", ".pages": "application/vnd.apple.pages",
	".numbers": "application/vnd.apple.numbers", ".epub": "application/epub+zip",
	".zip": "application/zip", ".ico": "image/x-icon", ".mp3": "audio/mpeg", ".wav": "audio/wav",
	".mp4": "video/mp4", ".mov": "video/quicktime", ".tsv": "text/tab-separated-values; charset=utf-8",
}

var shownTypes = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".csv": true, ".tsv": true, ".json": true, ".html": true,
	".htm": true, ".pdf": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true,
}

// readOutputHandler answers an output itself. A helper's HTML or SVG never
// runs in the coordinator's origin: everything is served under a sandbox
// policy, and the app shows HTML in a sandboxed frame and SVG as an image.
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
	extension := strings.ToLower(filepath.Ext(name))
	kind, ok := outputTypes[extension]
	if !ok {
		kind = "application/octet-stream"
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:")
	if !shownTypes[extension] {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
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
