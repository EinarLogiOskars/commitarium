package httpapi

import (
	"context"
	"errors"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	// Each output's versions (H8): listed newest first, read one by one, and
	// restored as the latest.
	Versions(ctx context.Context, owner, name string) ([]outputs.Version, error)
	VersionPath(ctx context.Context, owner, name string, number int) (string, error)
	Restore(ctx context.Context, owner, name string, number int) (outputs.File, error)
}

// outputWork is what working on a file together (H8) needs of the
// assistant: ending a helper's work on its files, and where a helper's files
// are while it builds them.
type outputWork interface {
	OutputDeleted(ctx context.Context, scope, helperID string) error
	HelperFiles(ctx context.Context, scope, helperID string) (string, error)
}

// outputFile is an output as the app lists it, with whether its helper is
// at work on it now and whether it still keeps its session and directory.
type outputFile struct {
	outputs.File
	Working bool `json:"working,omitempty"`
	Kept    bool `json:"kept,omitempty"`
}

type outputsResponse struct {
	Outputs []outputFile `json:"outputs"`
}

type outputVersionsResponse struct {
	Versions []outputs.Version `json:"versions"`
}

type helperFilesResponse struct {
	Files []outputs.Pending `json:"files"`
}

// An owner is a project, or the top-level chats ("commitarium"), as with
// attachments.
func (api *API) registerOutputRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/outputs/{owner}", api.listOutputsHandler)
	mux.HandleFunc("GET /api/v1/outputs/{owner}/{name}", api.readOutputHandler)
	mux.HandleFunc("DELETE /api/v1/outputs/{owner}/{name}", api.deleteOutputHandler)
	mux.HandleFunc("GET /api/v1/outputs/{owner}/{name}/versions", api.outputVersionsHandler)
	mux.HandleFunc("GET /api/v1/outputs/{owner}/{name}/versions/{version}", api.readOutputVersionHandler)
	mux.HandleFunc("POST /api/v1/outputs/{owner}/{name}/versions/{version}/restore", api.restoreOutputHandler)
	// What a helper has in its outputs/ folder while it works, read-only.
	mux.HandleFunc("GET /api/v1/helper-files/{owner}/{helper}", api.listHelperFilesHandler)
	mux.HandleFunc("GET /api/v1/helper-files/{owner}/{helper}/{name}", api.readHelperFileHandler)
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
	helpers := map[string]conversations.Helper{}
	if lister, ok := api.conversations.(helperLister); ok {
		if listed, err := lister.HelpersOf(r.Context(), owner); err == nil {
			for _, helper := range listed {
				helpers[helper.ID] = helper
			}
		}
	}
	listed := make([]outputFile, 0, len(files))
	for _, file := range files {
		entry := outputFile{File: file}
		if helper, ok := helpers[file.Helper]; ok && helper.Kind == conversations.HelperTask && !helper.TestApp {
			entry.Working, entry.Kept = !helper.Done, helper.Kept
		}
		listed = append(listed, entry)
	}
	writeJSON(w, http.StatusOK, outputsResponse{Outputs: listed}, "outputs")
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

// readOutputHandler answers an output's latest version.
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
	serveOutput(w, r, name, path)
}

func (api *API) outputVersionsHandler(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	if !api.attachmentOwner(w, r, owner) {
		return
	}
	versions, err := api.outputs.Versions(r.Context(), owner, name)
	if err != nil {
		writeOutputError(w, owner, err)
		return
	}
	writeJSON(w, http.StatusOK, outputVersionsResponse{Versions: versions}, "output versions")
}

func (api *API) readOutputVersionHandler(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	if !api.attachmentOwner(w, r, owner) {
		return
	}
	number, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeOutputError(w, owner, outputs.ErrNotFound)
		return
	}
	path, err := api.outputs.VersionPath(r.Context(), owner, name, number)
	if err != nil {
		writeOutputError(w, owner, err)
		return
	}
	serveOutput(w, r, name, path)
}

// restoreOutputHandler makes an older version the latest, as a new version.
func (api *API) restoreOutputHandler(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	if !api.attachmentOwner(w, r, owner) {
		return
	}
	number, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeOutputError(w, owner, outputs.ErrNotFound)
		return
	}
	file, err := api.outputs.Restore(r.Context(), owner, name, number)
	if err != nil {
		writeOutputError(w, owner, err)
		return
	}
	writeJSON(w, http.StatusOK, file, "output")
}

// listHelperFilesHandler lists what a helper has in its outputs/ folder
// now, so the app shows a file as it is built.
func (api *API) listHelperFilesHandler(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	directory, ok := api.helperFiles(w, r, owner)
	if !ok {
		return
	}
	files := outputs.Live(directory)
	if files == nil {
		files = []outputs.Pending{}
	}
	writeJSON(w, http.StatusOK, helperFilesResponse{Files: files}, "helper files")
}

// readHelperFileHandler serves one file of a helper's outputs/ folder as
// it is now, as an output is served, and never through a link.
func (api *API) readHelperFileHandler(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	directory, ok := api.helperFiles(w, r, owner)
	if !ok {
		return
	}
	if !outputs.ValidName(name) {
		writeOutputError(w, owner, outputs.ErrNotFound)
		return
	}
	for _, file := range outputs.Live(directory) {
		if file.Name != name {
			continue
		}
		// The helper may change its folder at any time: serve only an ordinary
		// file whose path has no link in it, inside its folder.
		resolved, err := filepath.EvalSymlinks(file.Path)
		inside := filepath.Join(directory, outputs.Dir) + string(filepath.Separator)
		if info, statErr := os.Lstat(file.Path); err != nil || statErr != nil || resolved != file.Path ||
			!strings.HasPrefix(resolved, inside) || !info.Mode().IsRegular() {
			break
		}
		serveOutput(w, r, name, file.Path)
		return
	}
	writeOutputError(w, owner, outputs.ErrNotFound)
}

// helperFiles answers a helper's directory while it is kept.
func (api *API) helperFiles(w http.ResponseWriter, r *http.Request, owner string) (string, bool) {
	if !api.attachmentOwner(w, r, owner) {
		return "", false
	}
	work, ok := api.conversations.(outputWork)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant is unavailable")
		return "", false
	}
	directory, err := work.HelperFiles(r.Context(), owner, r.PathValue("helper"))
	if err != nil {
		if errors.Is(err, conversations.ErrNotFound) {
			writeError(w, http.StatusNotFound, "helper_not_found", "the helper has no files now")
			return "", false
		}
		writeConversationError(w, err)
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		writeError(w, http.StatusNotFound, "helper_not_found", "the helper has no files now")
		return "", false
	}
	return resolved, true
}

// serveOutput answers a file a helper made. A helper's HTML or SVG never
// runs in the coordinator's origin: everything is served under a sandbox
// policy, and the app shows HTML in a sandboxed frame and SVG as an image.
func serveOutput(w http.ResponseWriter, r *http.Request, name, path string) {
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
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}

// deleteOutputHandler removes an output with its versions; once the last of
// a helper's files is gone, the helper lets go of its directory and session.
func (api *API) deleteOutputHandler(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	if !api.attachmentOwner(w, r, owner) {
		return
	}
	helper := ""
	if files, err := api.outputs.List(r.Context(), owner); err == nil {
		for _, file := range files {
			if file.Name == name {
				helper = file.Helper
			}
		}
	}
	if err := api.outputs.Delete(r.Context(), owner, name); err != nil {
		writeOutputError(w, owner, err)
		return
	}
	if work, ok := api.conversations.(outputWork); ok && helper != "" {
		if err := work.OutputDeleted(r.Context(), owner, helper); err != nil {
			log.Printf("end the helper of %s's deleted output: %v", owner, err)
		}
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
