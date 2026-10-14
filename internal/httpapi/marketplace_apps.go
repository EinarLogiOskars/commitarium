package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/install"
	"github.com/EinarLogiOskars/commitarium/internal/marketplace"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// InstallService fetches marketplace apps and remembers what was installed.
type InstallService interface {
	Download(ctx context.Context, owner, name string) (install.Download, func(), error)
	Record(projectID string, app marketplace.App) error
	Installed() ([]install.Origin, error)
}

type marketplaceAppResponse struct {
	marketplace.App
	Yours              bool   `json:"yours"`
	InstalledProjectID string `json:"installed_project_id,omitempty"`
	InstalledVersion   string `json:"installed_version,omitempty"`
}

func (api *API) registerMarketplaceAppRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/marketplace/apps", api.listMarketplaceAppsHandler)
	mux.HandleFunc("POST /api/v1/marketplace/apps/{owner}/{name}/install", api.installMarketplaceAppHandler)
	mux.HandleFunc("GET /api/v1/marketplace/installs", api.listMarketplaceInstallsHandler)
}

func (api *API) listMarketplaceAppsHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	status, err := api.marketplace.Status(ctx)
	if err != nil {
		writeMarketplaceError(w, err)
		return
	}
	if status.Account == nil {
		writeMarketplaceError(w, marketplace.ErrNotSignedIn)
		return
	}
	apps, err := api.marketplace.Apps(ctx)
	if err != nil {
		writeMarketplaceError(w, err)
		return
	}
	installed := api.installedApps(ctx)
	response := make([]marketplaceAppResponse, 0, len(apps))
	for _, app := range apps {
		item := marketplaceAppResponse{App: app, Yours: strings.EqualFold(app.Owner, status.Account.Username)}
		if origin, ok := installed[strings.ToLower(app.Owner+"/"+app.Name)]; ok {
			item.InstalledProjectID, item.InstalledVersion = origin.ProjectID, origin.Version
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": response}, "marketplace apps")
}

// listMarketplaceInstallsHandler answers where each installed app came from,
// for the Apps area. It needs no sign-in.
func (api *API) listMarketplaceInstallsHandler(w http.ResponseWriter, r *http.Request) {
	installs := []install.Origin{}
	for _, origin := range api.installedApps(r.Context()) {
		installs = append(installs, origin)
	}
	writeJSON(w, http.StatusOK, map[string]any{"installs": installs}, "marketplace installs")
}

// installedApps answers the installs whose project still exists, newest per
// app, keyed by "owner/name".
func (api *API) installedApps(ctx context.Context) map[string]install.Origin {
	origins, err := api.installs.Installed()
	if err != nil {
		log.Printf("marketplace installs: %v", err)
		return map[string]install.Origin{}
	}
	projects, err := api.projects.List(ctx)
	if err != nil {
		return map[string]install.Origin{}
	}
	exists := map[string]bool{}
	for _, stored := range projects {
		exists[stored.ID] = true
	}
	installed := map[string]install.Origin{}
	for _, origin := range origins {
		key := strings.ToLower(origin.Owner + "/" + origin.Name)
		if exists[origin.ProjectID] && origin.InstalledAt.After(installed[key].InstalledAt) {
			installed[key] = origin
		}
	}
	return installed
}

func (api *API) installMarketplaceAppHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	owner, name := r.PathValue("owner"), r.PathValue("name")
	if api.projectImporter == nil {
		writeError(w, http.StatusServiceUnavailable, "install_unavailable", "installing is unavailable")
		return
	}
	download, cleanup, err := api.installs.Download(ctx, owner, name)
	if err != nil {
		writeInstallError(w, err)
		return
	}
	defer cleanup()
	bundle, err := os.Open(download.Bundle)
	if err != nil {
		writeInstallError(w, install.ErrUnavailable)
		return
	}
	defer bundle.Close()

	spec := project.ImportSpec{
		ImportID:       "marketplace-" + safeIDPart(owner) + "-" + safeIDPart(name) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Name:           appName(download),
		RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		MergePolicy:    project.MergePolicyAutoAfterGates,
		AutonomyPolicy: project.AutonomyPolicyRunToCompletion,
		DialogueLimits: project.DefaultDialogueLimits(),
		DefaultBranch:  "main",
	}
	imported, _, err := api.projectImporter.Import(ctx, spec, bundle)
	if err != nil {
		writeInstallError(w, err)
		return
	}
	if err := api.callOwn(ctx, http.MethodPut, "/api/v1/projects/"+imported.ID+"/app", "", map[string]bool{"app": true}, nil); err != nil {
		log.Printf("install %s/%s: mark as app: %v", owner, name, err)
	}
	if err := api.installs.Record(imported.ID, download.App); err != nil {
		log.Printf("install %s/%s: %v", owner, name, err)
	}
	api.applyPortableFiles(ctx, imported.ID, download.Files)
	writeJSON(w, http.StatusCreated, map[string]string{"project_id": imported.ID, "version": download.App.Version}, "install")
}

func safeIDPart(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
		} else {
			builder.WriteRune('-')
		}
	}
	part := strings.Trim(builder.String(), "-")
	if len(part) > 40 {
		part = part[:40]
	}
	if part == "" {
		return "app"
	}
	return part
}

func writeInstallError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, marketplace.ErrAppNotFound):
		writeError(w, http.StatusNotFound, "marketplace_app_not_found", "that app is not on the marketplace")
	case errors.Is(err, install.ErrUnavailable):
		log.Printf("install: %v", err)
		writeError(w, http.StatusServiceUnavailable, "install_unavailable", "the app could not be installed right now")
	case errors.Is(err, marketplace.ErrNotSignedIn), errors.Is(err, marketplace.ErrUnavailable):
		writeMarketplaceError(w, err)
	default:
		log.Printf("install: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

type visionSeeder interface {
	Seed(ctx context.Context, projectID string, contents []byte) (vision.Vision, error)
}

// applyPortableFiles gives an installed app the stack and vision its release
// carries (ADR-026), from the fetched files rather than the forge, which may
// still be indexing the import.
func (api *API) applyPortableFiles(ctx context.Context, projectID string, files map[string][]byte) {
	if contents, ok := files[toolchain.PortableStackPath]; ok && api.toolchains != nil {
		if manifest, err := toolchain.ParsePortableStack(contents); err != nil {
			log.Printf("install %s: ignore the release's stack: %v", projectID, err)
		} else if _, err := api.toolchains.Configure(ctx, projectID, manifest); err != nil {
			log.Printf("install %s: configure the stack: %v", projectID, err)
		} else {
			api.ensurePreviewSetupLater(projectID)
		}
	}
	if contents, ok := files[vision.PortableVisionPath]; ok {
		if seeder, ok := api.visions.(visionSeeder); ok {
			if _, err := seeder.Seed(ctx, projectID, contents); err != nil {
				log.Printf("install %s: seed the vision: %v", projectID, err)
			}
		}
	}
}

// appName is the installed app's name: the one its release carries, else
// the marketplace's title for it.
func appName(download install.Download) string {
	if contents, ok := download.Files[marketplace.AppManifestPath]; ok {
		if manifest, err := marketplace.ParseAppManifest(contents); err == nil {
			return manifest.Name
		}
	}
	return download.App.Title
}
