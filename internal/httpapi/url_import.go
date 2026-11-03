package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// URLBundler clones a repository by its URL into a bundle the folder import
// takes (SERVER_COMPLETE_PLAN.md, S3).
type URLBundler interface {
	BundleFromURL(ctx context.Context, remoteURL, username, token string) (string, string, func(), error)
}

type importFromURLRequest struct {
	Name           string                 `json:"name"`
	URL            string                 `json:"url"`
	Username       string                 `json:"username"`
	Token          string                 `json:"token"`
	RecoveryPolicy project.RecoveryPolicy `json:"recovery_policy"`
	MergePolicy    project.MergePolicy    `json:"merge_policy"`
	AutonomyPolicy project.AutonomyPolicy `json:"autonomy_policy"`
	DialogueLimits *dialogueLimitsRequest `json:"dialogue_limits"`
}

// Linker links an imported project to the repository it came from (S2).
type Linker interface {
	Link(ctx context.Context, projectID, url, username, token string) error
}

// RegisterURLImportRoute adds PUT /api/v1/project-imports/{importID}/url:
// the repository is cloned, bundled and imported as a folder's would be, so
// a server, which has no folder of the user's, imports too. The project
// stays linked to the repository.
func RegisterURLImportRoute(mux *http.ServeMux, importer ProjectImporter, bundler URLBundler, linker Linker) {
	mux.HandleFunc("PUT /api/v1/project-imports/{importID}/url", func(w http.ResponseWriter, r *http.Request) {
		var request importFromURLRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_project_import_metadata",
				"the import must be one JSON object with no unknown fields")
			return
		}
		limits, err := decodeDialogueLimits(request.DialogueLimits, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_dialogue_limits",
				"dialogue_limits must include non-negative planning_rounds and implementation_review_rounds; zero means unlimited")
			return
		}
		bundlePath, defaultBranch, cleanup, err := bundler.BundleFromURL(
			r.Context(), request.URL, strings.TrimSpace(request.Username), strings.TrimSpace(request.Token))
		if err != nil {
			if errors.Is(err, project.ErrInvalidGitBundle) {
				// The clone's own reason: a bad address, or no access.
				writeError(w, http.StatusBadRequest, "invalid_repository_url", err.Error())
				return
			}
			writeImportError(w, r, err)
			return
		}
		defer cleanup()
		bundle, err := os.Open(bundlePath)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "project_import_unavailable", "project import is temporarily unavailable")
			return
		}
		defer bundle.Close()
		imported, created, err := importer.Import(r.Context(), project.ImportSpec{
			ImportID: r.PathValue("importID"), Name: request.Name,
			RecoveryPolicy: request.RecoveryPolicy, MergePolicy: request.MergePolicy,
			AutonomyPolicy: request.AutonomyPolicy, DialogueLimits: limits,
			DefaultBranch: defaultBranch,
		}, bundle)
		if err != nil {
			writeImportError(w, r, err)
			return
		}
		// The import stands without the link, which the project page can
		// make again.
		if err := linker.Link(r.Context(), imported.ID, request.URL,
			strings.TrimSpace(request.Username), strings.TrimSpace(request.Token)); err != nil {
			log.Printf("link imported project %s: %v", imported.ID, err)
		}
		w.Header().Set("Location", "/api/v1/projects/"+imported.ID)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(newProjectResponse(imported)); err != nil {
			log.Printf("encode imported project response: %v", err)
		}
	})
}
