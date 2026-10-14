package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

// Project setup without friction: a project's stack is detected when it has
// none, and a project without a compose file gets one from a background quick
// change that Commitarium starts, merges, and retries by itself.

const (
	previewSetupTitle = "Set up the preview"
	previewFixTitle   = "Fix the preview"
	// maxPreviewFixAttempts bounds the background fixes after a setup; the
	// user is asked after that.
	maxPreviewFixAttempts = 2
	previewSetupTimeout   = 2 * time.Minute
)

// previewRules are the rules a preview's compose file must follow. They match
// the desktop's preview validation.
const previewRules = `- Put one compose file at the repository root (compose.yaml), with one service per part the app needs (for example frontend, API, database). Servers listen on 0.0.0.0.
- Run the app for development with live reload: start dev servers that reload on change (for example Vite's dev server, uvicorn --reload, nodemon) and bind-mount the source folders they watch (for example ./frontend/src:/app/src). Do not mount over dependency folders such as node_modules.
- Keep data in named volumes.
- Bind mounts, build contexts, and Dockerfiles stay inside the repository. No privileged mode, host networking, added capabilities, devices, secrets, env_file, or external volumes or networks. Every environment variable has an explicit value.
- The frontend reaches the API through a relative-path dev proxy (for example /api), not a hardcoded localhost port.
- Publish the port the user opens in a browser, and name it at the top level of the compose file:
  x-commitarium-preview:
    service: <the service the user opens>
    port: <its container port>`

func previewSetupDescription() string {
	return "Add a compose file that runs this project for Commitarium's preview and for `docker compose up`.\n\n" +
		previewRules + "\n- Mention in the README that `docker compose up` runs the project locally."
}

func previewFixDescription(failure string) string {
	return "The preview failed to start. Fix the compose setup or the app so it starts, keeping the preview rules:\n\n" +
		previewRules + "\n\nError and recent output:\n\n```\n" + strings.TrimSpace(failure) + "\n```"
}

// projectToolchain reads a project's stack. A project without one gets its
// stack detected and saved when detection is confident, and then its preview
// set up in the background.
func (api *API) projectToolchain(ctx context.Context, projectID string) (toolchain.Manifest, error) {
	manifest, err := api.toolchains.Get(ctx, projectID)
	if err != nil || manifest.Status != toolchain.StatusNeedsSetup {
		return manifest, err
	}
	suggestion, err := api.toolchains.Detect(ctx, projectID)
	if err != nil || suggestion.Confidence != "high" || len(suggestion.Tools) == 0 {
		return manifest, nil
	}
	configured, err := api.toolchains.Configure(ctx, projectID, toolchain.Manifest{
		Source: toolchain.SourceDetected, Tools: suggestion.Tools, Services: suggestion.Services, Run: suggestion.Run,
	})
	if err != nil {
		log.Printf("save detected stack for project %q: %v", projectID, err)
		return manifest, nil
	}
	api.ensurePreviewSetupLater(projectID)
	return configured, nil
}

// ensurePreviewSetupLater sets up the preview in the background, after the
// request that configured the stack has answered.
func (api *API) ensurePreviewSetupLater(projectID string) {
	if !api.canSetUpPreviews() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), previewSetupTimeout)
		defer cancel()
		if _, err := api.ensurePreviewSetup(ctx, projectID); err != nil {
			log.Printf("set up the preview for project %q: %v", projectID, err)
		}
	}()
}

var (
	errPreviewSetupUnavailable = errors.New("background preview setup needs real agents")
	errPreviewHasCompose       = errors.New("the repository already has a compose file")
	errPreviewFixesExhausted   = errors.New("the background fixes for the preview are used up")
)

// ensurePreviewSetup starts the background preview setup when the project's
// repository has no compose file and nothing is setting one up yet. It
// returns the work order setting the preview up, if any.
func (api *API) ensurePreviewSetup(ctx context.Context, projectID string) (*feature.Feature, error) {
	if !api.canSetUpPreviews() {
		return nil, errPreviewSetupUnavailable
	}
	api.previewSetup.Lock()
	defer api.previewSetup.Unlock()
	if active, err := api.activeBackgroundWorkOrder(ctx, projectID); err != nil || active != nil {
		return active, err
	}
	overview, err := api.projects.GetRepositoryOverview(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if repositoryHasCompose(overview) {
		return nil, errPreviewHasCompose
	}
	if !overview.HasCode() {
		// Nothing to preview yet. Reading the stack checks again, so the setup
		// starts once the first code is merged.
		return nil, nil
	}
	started, err := api.startBackgroundWorkOrder(ctx, projectID, previewSetupTitle, previewSetupDescription())
	if err != nil {
		return nil, err
	}
	return &started, nil
}

// fixPreview starts a background fix for a preview that failed to start,
// unless one is running or the fixes since the last setup are used up.
func (api *API) fixPreview(ctx context.Context, projectID string, failure string) (*feature.Feature, error) {
	if !api.canSetUpPreviews() {
		return nil, errPreviewSetupUnavailable
	}
	api.previewSetup.Lock()
	defer api.previewSetup.Unlock()
	if active, err := api.activeBackgroundWorkOrder(ctx, projectID); err != nil || active != nil {
		return active, err
	}
	features, err := api.features.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if previewFixesSinceSetup(features) >= maxPreviewFixAttempts {
		return nil, errPreviewFixesExhausted
	}
	started, err := api.startBackgroundWorkOrder(ctx, projectID, previewFixTitle, previewFixDescription(failure))
	if err != nil {
		return nil, err
	}
	return &started, nil
}

// canSetUpPreviews reports whether background work can run: it needs real
// agents and the work-order and project services.
func (api *API) canSetUpPreviews() bool {
	return api.starter != nil && api.realWorkflow != nil && api.features != nil && api.projects != nil
}

func (api *API) activeBackgroundWorkOrder(ctx context.Context, projectID string) (*feature.Feature, error) {
	features, err := api.features.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for index := range features {
		if features[index].Background && !features[index].State.IsTerminal() {
			return &features[index], nil
		}
	}
	return nil, nil
}

// previewFixesSinceSetup counts the background fixes after the most recent
// background setup.
func previewFixesSinceSetup(features []feature.Feature) int {
	background := make([]feature.Feature, 0)
	for _, stored := range features {
		if stored.Background {
			background = append(background, stored)
		}
	}
	sort.Slice(background, func(i, j int) bool { return background[i].CreatedAt.After(background[j].CreatedAt) })
	fixes := 0
	for _, stored := range background {
		if stored.Title != previewFixTitle {
			break
		}
		fixes++
	}
	return fixes
}

var composeFileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

func repositoryHasCompose(overview project.RepositoryOverview) bool {
	for _, entry := range overview.Tree {
		for _, name := range composeFileNames {
			if entry.Path == name {
				return true
			}
		}
	}
	return false
}

// startBackgroundWorkOrder creates and starts a quick change Commitarium runs
// by itself and leaves out of the user's lists.
func (api *API) startBackgroundWorkOrder(
	ctx context.Context,
	projectID string,
	title string,
	description string,
) (feature.Feature, error) {
	return api.startQuickChange(ctx, projectID, title, description, true)
}

// startQuickChange creates and starts a quick change Commitarium starts
// itself: no clarification, plan, or reviewer, and an automatic merge once
// validation passes.
func (api *API) startQuickChange(
	ctx context.Context,
	projectID string,
	title string,
	description string,
	background bool,
) (feature.Feature, error) {
	merge := project.MergePolicyAutoAfterGates
	autonomy := project.AutonomyPolicyRunToCompletion
	// It runs on the assistant's agent and model (ADR-027); without a
	// reviewer, the lead is named for both roles.
	providers, model, err := api.assistantAgent(ctx)
	if err != nil {
		return feature.Feature{}, err
	}
	models := project.AgentModels{Lead: model, Reviewer: model}
	if err := api.checkWorkChoice(ctx, providers, models, true); err != nil {
		return feature.Feature{}, err
	}
	modelStarter, ok := api.starter.(ModelRunStarter)
	if !ok {
		return feature.Feature{}, errPreviewSetupUnavailable
	}
	// Commitarium's own work runs on the assistant's agent, and the assistant
	// may see it through (ADR-027).
	created, err := api.features.Create(ctx, projectID, title, description, feature.SettingsOverrides{
		Background: background, CreatedBy: feature.CreatedByAssistant, Steps: &project.WorkSteps{SkipPlan: true, SkipReviewer: true},
		MergePolicy: &merge, AutonomyPolicy: &autonomy, AgentProviders: &providers, AgentModels: &models,
	})
	if err != nil {
		return feature.Feature{}, err
	}
	key := "background-" + created.ID
	readied, err := api.readyWithoutClarification(ctx, created, key)
	if err != nil {
		return feature.Feature{}, err
	}
	goal := readied.Title + ": " + readied.Description
	_, _, err = modelStarter.StartWithModels(
		ctx, runIDForKey(key), projectID, readied.ID, goal, project.DefaultDialogueLimits(),
		readied.AgentProviders, readied.AgentModels, readied.MergePolicy, readied.AutonomyPolicy,
	)
	if err != nil {
		return feature.Feature{}, err
	}
	return api.features.GetByID(ctx, projectID, readied.ID)
}

type previewSetupResponse struct {
	// WorkOrder is the background work order setting the preview up, when
	// one runs.
	WorkOrder *featureResponse `json:"work_order,omitempty"`
}

// startPreviewSetupHandler sets the preview up in the background on request.
func (api *API) startPreviewSetupHandler(w http.ResponseWriter, r *http.Request) {
	started, err := api.ensurePreviewSetup(r.Context(), r.PathValue("id"))
	api.writePreviewSetup(w, r.PathValue("id"), started, err)
}

type previewFailureRequest struct {
	Error string `json:"error"`
}

// previewFailedHandler takes a preview that failed to start and starts a
// background fix, until the fixes are used up.
func (api *API) previewFailedHandler(w http.ResponseWriter, r *http.Request) {
	var body previewFailureRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	failure := body.Error
	if len(failure) > 16*1024 {
		failure = failure[len(failure)-16*1024:]
	}
	started, err := api.fixPreview(r.Context(), r.PathValue("id"), failure)
	api.writePreviewSetup(w, r.PathValue("id"), started, err)
}

func (api *API) writePreviewSetup(w http.ResponseWriter, projectID string, started *feature.Feature, err error) {
	switch {
	case err == nil:
		response := previewSetupResponse{}
		if started != nil {
			item := newFeatureResponse(*started)
			response.WorkOrder = &item
		}
		writeJSON(w, http.StatusOK, response, "preview setup")
	case writeChoiceError(w, err):
	case errors.Is(err, errPreviewHasCompose):
		writeError(w, http.StatusConflict, "preview_has_compose", err.Error())
	case errors.Is(err, errPreviewFixesExhausted):
		writeError(w, http.StatusConflict, "preview_fixes_exhausted", err.Error())
	case errors.Is(err, errPreviewSetupUnavailable):
		writeError(w, http.StatusServiceUnavailable, "preview_setup_unavailable", err.Error())
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "project not found")
	default:
		log.Printf("preview setup for project %q: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
