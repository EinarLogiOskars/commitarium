package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

// A new project is set up in one step: the stack the vision assistant
// recommended (or the user chose) is saved, and a visible quick change
// scaffolds the app on it and writes the compose file for the preview.

const projectSetupTitle = "Set the project up"

var (
	errProjectHasCode  = errors.New("the repository already has code")
	errInvalidStack    = errors.New("invalid stack")
	errSetupNeedsStack = errors.New("choose at least one runtime")
)

type projectSetupRequest struct {
	Presets    []string `json:"presets"`
	Frameworks string   `json:"frameworks"`
}

type projectSetupResponse struct {
	WorkOrder featureResponse    `json:"work_order"`
	Toolchain toolchain.Manifest `json:"toolchain"`
}

func (api *API) setUpProjectHandler(w http.ResponseWriter, r *http.Request) {
	var body projectSetupRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	projectID := r.PathValue("id")
	started, manifest, created, err := api.setUpProject(r.Context(), projectID, body.Presets, body.Frameworks)
	switch {
	case err == nil:
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, projectSetupResponse{WorkOrder: newFeatureResponse(started), Toolchain: manifest}, "project setup")
	case errors.Is(err, errPreviewSetupUnavailable):
		writeError(w, http.StatusServiceUnavailable, "project_setup_unavailable", "setting a project up needs real agents")
	case errors.Is(err, errProjectHasCode):
		writeError(w, http.StatusConflict, "project_has_code", "the repository already has code; set its stack on the Stack page")
	case errors.Is(err, errInvalidStack), errors.Is(err, errSetupNeedsStack), errors.Is(err, toolchain.ErrInvalidManifest):
		writeError(w, http.StatusBadRequest, "invalid_stack", err.Error())
	default:
		api.writeToolchainError(w, projectID, "set the project up", err)
	}
}

// setUpProject saves the stack and starts the setup work order, or answers the
// one already running.
func (api *API) setUpProject(
	ctx context.Context,
	projectID string,
	presets []string,
	frameworks string,
) (feature.Feature, toolchain.Manifest, bool, error) {
	if api.toolchains == nil || !api.canSetUpPreviews() {
		return feature.Feature{}, toolchain.Manifest{}, false, errPreviewSetupUnavailable
	}
	api.previewSetup.Lock()
	defer api.previewSetup.Unlock()
	features, err := api.features.List(ctx, projectID)
	if err != nil {
		return feature.Feature{}, toolchain.Manifest{}, false, err
	}
	for _, existing := range features {
		if existing.Title == projectSetupTitle && !existing.Background && !existing.State.IsTerminal() {
			manifest, err := api.toolchains.Get(ctx, projectID)
			return existing, manifest, false, err
		}
	}
	overview, err := api.projects.GetRepositoryOverview(ctx, projectID)
	if err != nil {
		return feature.Feature{}, toolchain.Manifest{}, false, err
	}
	if overview.HasCode() {
		return feature.Feature{}, toolchain.Manifest{}, false, errProjectHasCode
	}
	tools, names, err := presetTools(presets)
	if err != nil {
		return feature.Feature{}, toolchain.Manifest{}, false, err
	}
	manifest, err := api.toolchains.Configure(ctx, projectID, toolchain.Manifest{
		Source: toolchain.SourcePicker, Tools: tools,
	})
	if err != nil {
		return feature.Feature{}, toolchain.Manifest{}, false, err
	}
	statement := ""
	if api.visions != nil {
		if stored, err := api.visions.Get(ctx, projectID); err == nil {
			statement = stored.Plan.Statement
		}
	}
	started, err := api.startQuickChange(ctx, projectID, projectSetupTitle,
		projectSetupDescription(statement, strings.Join(names, " and "), frameworks), false)
	if err != nil {
		log.Printf("start the setup of project %q: %v", projectID, err)
		return feature.Feature{}, manifest, false, err
	}
	return started, manifest, true, nil
}

// presetTools merges the runtime presets into one set of tools.
func presetTools(ids []string) (map[string]string, []string, error) {
	if len(ids) == 0 {
		return nil, nil, errSetupNeedsStack
	}
	known := map[string]toolchain.Preset{}
	for _, preset := range toolchain.Presets() {
		known[preset.ID] = preset
	}
	tools := map[string]string{}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		preset, ok := known[id]
		if !ok {
			return nil, nil, fmt.Errorf("%w: unknown runtime %q", errInvalidStack, id)
		}
		for tool, version := range preset.Tools {
			if existing, ok := tools[tool]; ok && existing != version {
				return nil, nil, fmt.Errorf("%w: %s is chosen in two versions", errInvalidStack, tool)
			}
			tools[tool] = version
		}
		names = append(names, preset.DisplayName)
	}
	return tools, names, nil
}

func projectSetupDescription(statement, runtimes, frameworks string) string {
	var builder strings.Builder
	builder.WriteString("Set this new project up so work can start on it.\n\n")
	if statement = strings.TrimSpace(statement); statement != "" {
		builder.WriteString("What the app is: " + statement + "\n\n")
	}
	builder.WriteString("Stack: " + runtimes)
	if frameworks = strings.TrimSpace(frameworks); frameworks != "" {
		builder.WriteString(", built with " + frameworks)
	}
	builder.WriteString(".\n\n" +
		"- Scaffold a minimal app on this stack that starts and shows a simple start page. Build none of the " +
		"app's features yet; later work orders do that.\n" +
		"- Set up the stack's usual test runner with one passing test, so later work orders have a test command.\n" +
		"- Add a .gitignore for the stack and a README saying what the app is and that `docker compose up` runs it.\n" +
		"- Add the compose file for Commitarium's preview:\n\n" + previewRules)
	return builder.String()
}
