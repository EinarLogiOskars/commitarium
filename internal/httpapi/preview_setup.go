package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

// Project setup without friction: a project's stack is detected when it has
// none, and a project without a compose file gets one from a background quick
// change that Commitarium starts, merges, and retries by itself. Each such
// work order merges only once Commitarium's preview check of its commit
// passed (ADR-028): the desktop builds and starts the app for real, and a
// failure goes back to the agent with what each container did.

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
const previewRules = `- Put one compose file at the repository root (compose.yaml), with one service per part the app needs (for example frontend, API, database). Servers listen on 0.0.0.0. The app opens over plain http://127.0.0.1 in a WebKit window that frames it: keep sign-in cookies on the framework's defaults for http (SameSite=Lax, not Secure, no __Host- or __Secure- names).
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

func previewFixDescription(failure string, evidence *previewcheck.Evidence) string {
	description := "The preview failed to start. Fix the compose setup or the app so it starts, keeping the preview rules:\n\n" +
		previewRules
	if failure = strings.TrimSpace(failure); failure != "" {
		description += "\n\nError and recent output:\n\n```\n" + failure + "\n```"
	}
	if evidence != nil && len(evidence.Services) > 0 {
		description += "\n\nWhat each container did:\n\n" + boundedText(evidence.Technical(), 24*1024)
	}
	return description
}

// boundedText keeps the start of text, at most limit bytes.
func boundedText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "\n[cut]"
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
	return api.ensurePreviewSetupLocked(ctx, projectID)
}

func (api *API) ensurePreviewSetupLocked(ctx context.Context, projectID string) (*feature.Feature, error) {
	if active, err := api.activePreviewWorkOrder(ctx, projectID); err != nil || active != nil {
		return active, err
	}
	overview, err := api.projects.GetRepositoryOverview(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if overview.HasCompose() {
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
// unless one is running. A fix Commitarium asks for by itself stops once the
// fixes since the last setup are used up; one the user or the assistant
// asks for (chosen) always starts.
func (api *API) fixPreview(
	ctx context.Context, projectID string, failure string, evidence *previewcheck.Evidence, chosen bool,
) (*feature.Feature, error) {
	if !api.canSetUpPreviews() {
		return nil, errPreviewSetupUnavailable
	}
	api.previewSetup.Lock()
	defer api.previewSetup.Unlock()
	return api.fixPreviewLocked(ctx, projectID, failure, evidence, chosen)
}

func (api *API) fixPreviewLocked(
	ctx context.Context, projectID string, failure string, evidence *previewcheck.Evidence, chosen bool,
) (*feature.Feature, error) {
	if active, err := api.activePreviewWorkOrder(ctx, projectID); err != nil || active != nil {
		return active, err
	}
	features, err := api.features.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if !chosen && previewFixesSinceSetup(features) >= maxPreviewFixAttempts {
		return nil, errPreviewFixesExhausted
	}
	started, err := api.startBackgroundWorkOrder(ctx, projectID, previewFixTitle, previewFixDescription(failure, evidence))
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

// setUpPreview is the "Set up preview" flow the user or the assistant
// starts: a fix of a preview that fails, when the repository has a compose
// file, or its setup when it has none.
func (api *API) setUpPreview(
	ctx context.Context, projectID string, failure string, evidence *previewcheck.Evidence,
) (*feature.Feature, error) {
	if !api.canSetUpPreviews() {
		return nil, errPreviewSetupUnavailable
	}
	api.previewSetup.Lock()
	defer api.previewSetup.Unlock()
	overview, err := api.projects.GetRepositoryOverview(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if overview.HasCompose() {
		return api.fixPreviewLocked(ctx, projectID, failure, evidence, true)
	}
	return api.ensurePreviewSetupLocked(ctx, projectID)
}

// activePreviewWorkOrder is the unfinished work order setting the preview
// up: Commitarium's background setup or fix, or a project's setup, which
// writes the compose file too.
func (api *API) activePreviewWorkOrder(ctx context.Context, projectID string) (*feature.Feature, error) {
	features, err := api.features.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for index := range features {
		if (features[index].Background || features[index].PreviewCheck) && !features[index].State.IsTerminal() {
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
// its preview check and validation pass. Every such change writes the
// app's compose setup, so each is checked (ADR-028).
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
		PreviewCheck: true,
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
	// Evidence is what each container did, when the preview started its
	// containers before it failed.
	Evidence *previewcheck.Evidence `json:"evidence,omitempty"`
	// Chosen says the user asked for the fix ("Set up preview"), so it
	// starts even when the automatic fixes are used up.
	Chosen bool `json:"chosen,omitempty"`
}

func (body *previewFailureRequest) bound() {
	if len(body.Error) > 16*1024 {
		body.Error = body.Error[len(body.Error)-16*1024:]
	}
	if body.Evidence != nil {
		bounded := body.Evidence.Bounded()
		body.Evidence = &bounded
	}
}

// previewFailedHandler takes a preview that failed to start and starts a
// background fix, until the automatic fixes are used up.
func (api *API) previewFailedHandler(w http.ResponseWriter, r *http.Request) {
	var body previewFailureRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	body.bound()
	projectID := r.PathValue("id")
	var started *feature.Feature
	var err error
	if body.Chosen {
		started, err = api.setUpPreview(r.Context(), projectID, body.Error, body.Evidence)
	} else {
		started, err = api.fixPreview(r.Context(), projectID, body.Error, body.Evidence, false)
	}
	api.writePreviewSetup(w, projectID, started, err)
}

// previewFailureReporter tells a project's assistant about something
// outside its work orders.
type previewFailureReporter interface {
	Report(ctx context.Context, scope, text string) error
}

// reportPreviewFailureHandler tells the project's assistant that its app's
// preview failed to start, with what each container did, so it can start
// the preview setup itself. It starts nothing.
func (api *API) reportPreviewFailureHandler(w http.ResponseWriter, r *http.Request) {
	var body previewFailureRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	body.bound()
	projectID := r.PathValue("id")
	if _, err := api.projects.GetByID(r.Context(), projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		log.Printf("report the preview failure of project %q: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if reporter, ok := api.conversations.(previewFailureReporter); ok {
		if err := reporter.Report(r.Context(), projectID, previewFailureEvent(body.Error, body.Evidence)); err != nil {
			log.Printf("tell the assistant of project %q about its preview: %v", projectID, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// previewFailureEvent is what the assistant hears about a preview that
// failed to start.
func previewFailureEvent(failure string, evidence *previewcheck.Evidence) string {
	text := conversations.PreviewFailure
	detail := ""
	if evidence != nil && len(evidence.Services) > 0 {
		text += ": " + evidence.Plain()
		detail = evidence.Technical()
	} else if line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(failure), "\n", 2)[0]); line != "" {
		text += ": " + line
		detail = strings.TrimSpace(failure)
	}
	text += ". Propose set_up_preview to have an agent fix it and Commitarium check it for real."
	if detail != "" {
		text += "\n" + boundedText(detail, 4*1024)
	}
	return text
}

func (api *API) writePreviewSetup(w http.ResponseWriter, projectID string, started *feature.Feature, err error) {
	if err != nil {
		writePreviewSetupError(w, projectID, err)
		return
	}
	response := previewSetupResponse{}
	if started != nil {
		item := newFeatureResponse(*started)
		response.WorkOrder = &item
	}
	writeJSON(w, http.StatusOK, response, "preview setup")
}

func writePreviewSetupError(w http.ResponseWriter, projectID string, err error) {
	switch {
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
