package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// ConversationService is the assistant (ASSISTANT_PLAN.md): conversations at
// the top level and per project, its settings, and the actions it proposes.
type ConversationService interface {
	Settings(ctx context.Context) (conversations.Settings, error)
	SaveSettings(ctx context.Context, settings conversations.Settings) (conversations.Settings, error)
	Get(ctx context.Context, scope string) (conversations.Conversation, error)
	Send(ctx context.Context, scope, message, key string) (conversations.Conversation, error)
	SaveNotes(ctx context.Context, scope, notes string) (conversations.Conversation, error)
	Action(ctx context.Context, scope, actionID string) (conversations.Action, error)
	Resolve(ctx context.Context, scope, actionID string, status conversations.ActionStatus, result string, created conversations.Created) (conversations.Conversation, error)
	StartGoal(ctx context.Context, scope string, level int) (conversations.Conversation, error)
	StopGoal(ctx context.Context, scope string) (conversations.Conversation, error)
	ActiveGoals(ctx context.Context) (map[string]conversations.Goal, error)
	StartLaunch(ctx context.Context, scope string) (conversations.Conversation, error)
	ActiveLaunches(ctx context.Context) (map[string]conversations.LaunchStage, error)
	AutopilotHandles(ctx context.Context, scope, workOrderID string, blockedAt time.Time) bool
}

// startLaunchHandler marks a new app's launch (APPS_PLAN.md): the
// coordinator then sets up the stack its vision recommends and builds the
// whole vision on autopilot.
func (api *API) startLaunchHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	conversation, err := api.conversations.StartLaunch(r.Context(), r.PathValue("scope"))
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation, "assistant conversation")
}

// ProjectSetUpper sets a new app up through handler exactly as the project's
// own setup request does: how a launch sets up the stack its vision
// recommends.
func ProjectSetUpper(handler http.Handler) func(ctx context.Context, projectID string, presets []string, frameworks string) error {
	return func(ctx context.Context, projectID string, presets []string, frameworks string) error {
		body, err := json.Marshal(projectSetupRequest{Presets: presets, Frameworks: frameworks})
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"/api/v1/projects/"+url.PathEscape(projectID)+"/setup", bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response := &recordedResponse{header: http.Header{}, status: http.StatusOK}
		handler.ServeHTTP(response, request)
		if response.status == http.StatusOK || response.status == http.StatusCreated {
			return nil
		}
		var failure errorResponse
		if json.Unmarshal(response.body.Bytes(), &failure) == nil && failure.Error.Message != "" {
			return errors.New(failure.Error.Message)
		}
		return fmt.Errorf("HTTP %d", response.status)
	}
}

// listAutopilotHandler answers the autopilot goals that are on, by project,
// for the project list.
func (api *API) listAutopilotHandler(w http.ResponseWriter, r *http.Request) {
	goals, err := api.conversations.ActiveGoals(r.Context())
	if err != nil {
		writeConversationError(w, err)
		return
	}
	launches, err := api.conversations.ActiveLaunches(r.Context())
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"goals": goals, "launches": launches}, "autopilot goals")
}

type startAutopilotRequest struct {
	Level int `json:"level"`
}

func (api *API) startAutopilotHandler(w http.ResponseWriter, r *http.Request) {
	var body startAutopilotRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	conversation, err := api.conversations.StartGoal(r.Context(), r.PathValue("scope"), body.Level)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation, "assistant conversation")
}

func (api *API) stopAutopilotHandler(w http.ResponseWriter, r *http.Request) {
	conversation, err := api.conversations.StopGoal(r.Context(), r.PathValue("scope"))
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation, "assistant conversation")
}

// AssistantActionApplier approves assistant actions through handler exactly
// as the user's Approve button does, with a new work order started: how
// autopilot takes over an action. The error carries the API's message.
func AssistantActionApplier(handler http.Handler) func(ctx context.Context, scope, actionID string) error {
	return func(ctx context.Context, scope, actionID string) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"/api/v1/assistant/conversations/"+url.PathEscape(scope)+"/actions/"+url.PathEscape(actionID)+"/approve",
			strings.NewReader(`{"start":true}`))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response := &recordedResponse{header: http.Header{}, status: http.StatusOK}
		handler.ServeHTTP(response, request)
		if response.status == http.StatusOK {
			return nil
		}
		var failure errorResponse
		if json.Unmarshal(response.body.Bytes(), &failure) == nil && failure.Error.Message != "" {
			return errors.New(failure.Error.Message)
		}
		return fmt.Errorf("HTTP %d", response.status)
	}
}

type recordedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (response *recordedResponse) Header() http.Header            { return response.header }
func (response *recordedResponse) Write(data []byte) (int, error) { return response.body.Write(data) }
func (response *recordedResponse) WriteHeader(status int)         { response.status = status }

func (api *API) getAssistantSettingsHandler(w http.ResponseWriter, r *http.Request) {
	settings, err := api.conversations.Settings(r.Context())
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings, "assistant settings")
}

func (api *API) saveAssistantSettingsHandler(w http.ResponseWriter, r *http.Request) {
	var body conversations.Settings
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	saved, err := api.conversations.SaveSettings(r.Context(), body)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved, "assistant settings")
}

func (api *API) getConversationHandler(w http.ResponseWriter, r *http.Request) {
	conversation, err := api.conversations.Get(r.Context(), r.PathValue("scope"))
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation, "assistant conversation")
}

func (api *API) sendConversationHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	var body sessionMessageRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	conversation, err := api.conversations.Send(r.Context(), r.PathValue("scope"), body.Message, key)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, conversation, "assistant conversation")
}

type conversationNotesRequest struct {
	Notes string `json:"notes"`
}

func (api *API) saveConversationNotesHandler(w http.ResponseWriter, r *http.Request) {
	var body conversationNotesRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	conversation, err := api.conversations.SaveNotes(r.Context(), r.PathValue("scope"), body.Notes)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation, "assistant conversation")
}

func (api *API) dismissConversationActionHandler(w http.ResponseWriter, r *http.Request) {
	conversation, err := api.conversations.Resolve(
		r.Context(), r.PathValue("scope"), r.PathValue("actionID"), conversations.ActionDismissed, "", conversations.Created{},
	)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation, "assistant conversation")
}

type approveActionRequest struct {
	// Start also starts a created work order.
	Start bool `json:"start"`
	// ConfirmPremium is the user's explicit approval of the premium models the
	// action uses; without it such an action is refused.
	ConfirmPremium bool `json:"confirm_premium"`
	// LeadModel and ReviewerModel replace the models a new or started work
	// order runs on: the user's choice over the assistant's.
	LeadModel     string `json:"lead_model,omitempty"`
	ReviewerModel string `json:"reviewer_model,omitempty"`
}

// approveConversationActionHandler runs one action the assistant proposed,
// through the same paths as the app's own buttons.
func (api *API) approveConversationActionHandler(w http.ResponseWriter, r *http.Request) {
	scope, actionID := r.PathValue("scope"), r.PathValue("actionID")
	var body approveActionRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	action, err := api.conversations.Action(r.Context(), scope, actionID)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	if action.Status != conversations.ActionProposed {
		writeError(w, http.StatusConflict, "action_not_pending", "the action is no longer waiting for approval")
		return
	}
	// At the top level the assistant only creates apps; everything else acts
	// in a project.
	if (scope == conversations.TopLevel) != (action.Type == featureartifact.AssistantCreateApp) {
		writeError(w, http.StatusConflict, "action_needs_project", "this action runs in a project's conversation, and a new app at the top level")
		return
	}
	chosen := make([]string, 0, 2)
	if action.Type == featureartifact.AssistantCreateWorkOrder || action.Type == featureartifact.AssistantStartWorkOrder {
		if model := strings.TrimSpace(body.LeadModel); model != "" && model != action.LeadModel {
			action.LeadModel = model
			chosen = append(chosen, "lead "+model)
		}
		if model := strings.TrimSpace(body.ReviewerModel); model != "" && model != action.ReviewerModel {
			action.ReviewerModel = model
			chosen = append(chosen, "reviewer "+model)
		}
	}
	// Premium is judged on the models that will run, not only those the card
	// names: a started work order keeps its own unless the card replaces them.
	premium, err := api.premiumModels(r.Context(), scope, action)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	if len(premium) > 0 && !body.ConfirmPremium {
		writeError(w, http.StatusConflict, "premium_approval_required",
			"this action uses "+strings.Join(premium, ", ")+", which needs your explicit approval")
		return
	}
	var result string
	var ok bool
	// The work order the action touched, for the app to open; a new one's ID
	// is known only once it exists.
	workOrderID := action.WorkOrderID
	projectID := ""
	switch action.Type {
	case featureartifact.AssistantCreateApp:
		result, projectID, ok = api.applyCreateApp(w, r, action)
	case featureartifact.AssistantCreateWorkOrder:
		result, workOrderID, ok = api.applyCreateWorkOrder(w, r, scope, action, body.Start)
	case featureartifact.AssistantStartWorkOrder:
		result, ok = api.applyStartWorkOrder(w, r, scope, action)
	case featureartifact.AssistantChangeModels:
		result, ok = api.applyChangeModels(w, r, scope, action)
	case featureartifact.AssistantAddVisionItem:
		result, ok = api.applyAddVisionItem(w, r, scope, action)
	case featureartifact.AssistantContinueWorkOrder:
		result, ok = api.applyContinueWorkOrder(w, r, scope, action)
	case featureartifact.AssistantSetUpPreview:
		result, workOrderID, ok = api.applySetUpPreview(w, r, scope, action)
	case featureartifact.AssistantCloseWorkOrder:
		closed, err := api.closeWorkOrder(r.Context(), scope, action.WorkOrderID, "assistant-"+action.ID)
		if err != nil {
			writeCloseError(w, scope, action.WorkOrderID, err)
			return
		}
		result, ok = fmt.Sprintf("Closed “%s”.", closed.Title), true
	case featureartifact.AssistantStartAutopilot:
		if _, err := api.conversations.StartGoal(r.Context(), scope, action.Priority); err != nil {
			writeConversationError(w, err)
			return
		}
		result, ok = fmt.Sprintf("Autopilot is on: finish P%d.", action.Priority), true
	default:
		writeError(w, http.StatusBadRequest, "unknown_action", "the action is not one Commitarium can run")
		return
	}
	if !ok {
		return
	}
	if len(chosen) > 0 {
		result += " The user chose " + strings.Join(chosen, " and ") + "."
	}
	conversation, err := api.conversations.Resolve(r.Context(), scope, actionID, conversations.ActionApplied, result, conversations.Created{WorkOrderID: workOrderID, ProjectID: projectID})
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation, "assistant conversation")
}

func (api *API) applyCreateWorkOrder(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action, start bool,
) (string, string, bool) {
	ctx := r.Context()
	if api.toolchains != nil {
		manifest, err := api.projectToolchain(ctx, projectID)
		if err != nil {
			api.writeToolchainError(w, projectID, "check before an assistant work order", err)
			return "", "", false
		}
		if manifest.Status != toolchain.StatusConfigured {
			writeError(w, http.StatusConflict, "project_toolchain_required", "choose or detect the project toolchain before creating a work order")
			return "", "", false
		}
	}
	stored, err := api.projects.GetByID(ctx, projectID)
	if err != nil {
		writeConversationError(w, err)
		return "", "", false
	}
	overrides, err := api.assistantSettings(ctx, stored, action.AssistantAction)
	if err != nil {
		writeAssistantSettingsError(w, err)
		return "", "", false
	}
	description := action.Description
	if action.VisionItemID != "" && api.visions != nil {
		if current, err := api.visions.Get(ctx, projectID); err == nil {
			if _, label, found := vision.Find(current.Plan, action.VisionItemID); found {
				description = strings.TrimSpace(description + "\n\nPart of the project's vision as " + label + ".")
			}
		}
	}
	created, err := api.features.Create(ctx, projectID, action.Title, description, overrides)
	if err != nil {
		log.Printf("create the assistant's work order %q: %v", action.Title, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return "", "", false
	}
	if action.VisionItemID != "" && api.visions != nil {
		if _, err := api.visions.LinkWorkOrder(ctx, projectID, action.VisionItemID, created.ID); err != nil {
			log.Printf("link work order %q to vision item %q: %v", created.ID, action.VisionItemID, err)
		}
	}
	result := "Created the work order “" + created.Title + "” (" + created.ID + ")."
	if !start {
		return result, created.ID, true
	}
	clarification := false
	if _, ok := api.startFeature(w, r, created, "assistant-"+action.ID, &startRunRequest{
		Steps: &workStepsRequest{Clarification: &clarification},
	}); !ok {
		return "", "", false
	}
	return "Created and started the work order “" + created.Title + "” (" + created.ID + ").", created.ID, true
}

func (api *API) applyStartWorkOrder(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	ctx := r.Context()
	stored, err := api.features.GetByID(ctx, projectID, action.WorkOrderID)
	if err != nil {
		writeConversationError(w, err)
		return "", false
	}
	current, err := api.projects.GetByID(ctx, projectID)
	if err != nil {
		writeConversationError(w, err)
		return "", false
	}
	request := &startRunRequest{}
	clarification := false
	request.Steps = &workStepsRequest{Clarification: &clarification}
	if action.Steps != "" {
		plan, reviewer := action.Steps != "quick", action.Steps == "full"
		request.Steps.DocumentedPlan, request.Steps.Reviewer = &plan, &reviewer
	}
	if action.IndependentTests {
		tests := true
		request.IndependentAcceptanceTests = &tests
	}
	if action.LeadAgent != "" || action.ReviewerAgent != "" {
		lead, reviewer := orDefault(action.LeadAgent, stored.AgentProviders.LeadAgent), orDefault(action.ReviewerAgent, stored.AgentProviders.ReviewerAgent)
		request.AgentProviders = &agentProvidersRequest{LeadAgent: &lead, ReviewerAgent: &reviewer}
	}
	if action.LeadModel != "" || action.ReviewerModel != "" {
		lead := orDefault(action.LeadModel, orDefault(stored.AgentModels.Lead, current.AgentModels.Lead))
		reviewer := orDefault(action.ReviewerModel, orDefault(stored.AgentModels.Reviewer, current.AgentModels.Reviewer))
		request.AgentModels = &agentModelsRequest{Lead: &lead, Reviewer: &reviewer}
	}
	if _, ok := api.startFeature(w, r, stored, "assistant-"+action.ID, request); !ok {
		return "", false
	}
	return "Started the work order “" + stored.Title + "”.", true
}

func (api *API) applyChangeModels(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	ctx := r.Context()
	changer, ok := api.realWorkflow.(runModelChanger)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "model_change_unavailable", "changing models needs real agents")
		return "", false
	}
	stored, err := api.features.GetByID(ctx, projectID, action.WorkOrderID)
	if err != nil {
		writeConversationError(w, err)
		return "", false
	}
	runs, err := api.execution.RunsForFeature(ctx, stored.ID)
	if err != nil || len(runs) == 0 {
		writeError(w, http.StatusConflict, "run_not_changeable", "the work order has no run to change")
		return "", false
	}
	latest := runs[0]
	for _, run := range runs[1:] {
		if run.StartedAt.After(latest.StartedAt) {
			latest = run
		}
	}
	if latest.Status.IsTerminal() {
		writeError(w, http.StatusConflict, "run_not_changeable", "the work order's run has ended")
		return "", false
	}
	models := project.AgentModels{Lead: action.LeadModel, Reviewer: action.ReviewerModel}
	if api.modelCatalog != nil {
		selection := latest.AgentModels
		if models.Lead != "" {
			selection.Lead = models.Lead
		}
		if models.Reviewer != "" {
			selection.Reviewer = models.Reviewer
		}
		if err := api.modelCatalog.ValidateSelection(ctx, latest.AgentProviders, selection); err != nil {
			writeModelSelectionError(w, err)
			return "", false
		}
	}
	if _, err := changer.ChangeModels(ctx, latest.ID, models, action.When == "now"); err != nil {
		writeRunModelsError(w, latest.ID, err)
		return "", false
	}
	return fmt.Sprintf("Switched the models of “%s”.", stored.Title), true
}

// premiumModels lists the models an action would run on that need the
// user's explicit approval.
func (api *API) premiumModels(ctx context.Context, projectID string, action conversations.Action) ([]string, error) {
	settings, err := api.conversations.Settings(ctx)
	if err != nil {
		return nil, err
	}
	models := []string{action.LeadModel, action.ReviewerModel}
	if action.Type == featureartifact.AssistantStartWorkOrder {
		stored, err := api.features.GetByID(ctx, projectID, action.WorkOrderID)
		if err != nil {
			return nil, err
		}
		if action.LeadModel == "" {
			models[0] = stored.AgentModels.Lead
		}
		if action.ReviewerModel == "" && !stored.Steps.SkipReviewer {
			models[1] = stored.AgentModels.Reviewer
		}
	}
	premium := make([]string, 0)
	for _, model := range models {
		if settings.RequiresApproval(model) {
			premium = append(premium, model)
		}
	}
	return premium, nil
}

// applyContinueWorkOrder continues a work order whose run waits on a
// blocker, as the Continue and Add guidance buttons do.
// applySetUpPreview starts the "Set up preview" flow for the assistant: a
// fix of the app's preview, or its setup when the repository has no compose
// file yet. One that already runs is answered as it is.
func (api *API) applySetUpPreview(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, string, bool) {
	if api.toolchains == nil {
		writePreviewSetupError(w, projectID, errPreviewSetupUnavailable)
		return "", "", false
	}
	started, err := api.setUpPreview(r.Context(), projectID, action.Guidance, nil)
	if err != nil {
		writePreviewSetupError(w, projectID, err)
		return "", "", false
	}
	if started == nil {
		writeError(w, http.StatusConflict, "preview_nothing_to_run", "the project has no code to preview yet")
		return "", "", false
	}
	return fmt.Sprintf("Setting the preview up in “%s” (%s); it merges once Commitarium's check shows the app runs.",
		started.Title, started.ID), started.ID, true
}

func (api *API) applyContinueWorkOrder(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	ctx := r.Context()
	directed, ok := api.realWorkflow.(directedRecoveryWorkflow)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "recovery_unavailable", "continuing a work order needs real agents")
		return "", false
	}
	stored, err := api.features.GetByID(ctx, projectID, action.WorkOrderID)
	if err != nil {
		writeConversationError(w, err)
		return "", false
	}
	runs, err := api.execution.RunsForFeature(ctx, stored.ID)
	if err != nil || len(runs) == 0 {
		writeError(w, http.StatusConflict, "recovery_not_allowed", "the work order has no run to continue")
		return "", false
	}
	latest := newestRun(runs)
	if _, _, err := directed.RecoverBlockerWithDirective(ctx, latest.ID, runActionIDForKey("assistant-"+action.ID),
		orchestration.RecoveryDirective{
			Action: orchestration.RecoveryActionContinue, Message: action.Guidance, FromAssistant: true,
		},
	); err != nil {
		switch {
		case errors.Is(err, orchestration.ErrRecoveryNotAllowed):
			writeError(w, http.StatusConflict, "recovery_not_allowed", "the work order is not waiting on a blocker")
		default:
			log.Printf("continue the assistant's work order %q: %v", stored.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return "", false
	}
	if action.Guidance != "" {
		return fmt.Sprintf("Continued “%s” with guidance.", stored.Title), true
	}
	return fmt.Sprintf("Continued “%s”.", stored.Title), true
}

// assistantSettings turns an action's steps, agents, models, and tests into
// a work order's settings, filling the rest from the project's defaults.
func (api *API) assistantSettings(
	ctx context.Context, stored project.Project, action featureartifact.AssistantAction,
) (feature.SettingsOverrides, error) {
	var overrides feature.SettingsOverrides
	switch action.Steps {
	case "quick":
		overrides.Steps = &project.WorkSteps{SkipPlan: true, SkipReviewer: true}
	case "planned":
		overrides.Steps = &project.WorkSteps{SkipReviewer: true}
	case "full":
		overrides.Steps = &project.WorkSteps{}
	}
	if action.IndependentTests {
		tests := true
		overrides.IndependentAcceptanceTests = &tests
	}
	providers := stored.AgentProviders
	if action.LeadAgent != "" || action.ReviewerAgent != "" {
		lead := orDefault(action.LeadAgent, stored.AgentProviders.LeadAgent)
		reviewer := orDefault(action.ReviewerAgent, stored.AgentProviders.ReviewerAgent)
		decoded, err := api.decodeAgentProviders(ctx, &agentProvidersRequest{LeadAgent: &lead, ReviewerAgent: &reviewer}, false)
		if err != nil {
			return feature.SettingsOverrides{}, errInvalidAssistantAgents
		}
		providers = decoded
		overrides.AgentProviders = &providers
	}
	if action.LeadModel != "" || action.ReviewerModel != "" {
		models := project.AgentModels{
			Lead:     orDefault(action.LeadModel, stored.AgentModels.Lead),
			Reviewer: orDefault(action.ReviewerModel, stored.AgentModels.Reviewer),
		}
		if api.modelCatalog != nil {
			if err := api.modelCatalog.ValidateSelection(ctx, providers, models); err != nil {
				return feature.SettingsOverrides{}, err
			}
		}
		overrides.AgentModels = &models
	}
	return overrides, nil
}

var errInvalidAssistantAgents = errors.New("the action names agents that are not connected")

func writeAssistantSettingsError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInvalidAssistantAgents) {
		writeError(w, http.StatusBadRequest, "invalid_agent_providers", err.Error())
		return
	}
	writeModelSelectionError(w, err)
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func writeConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conversations.ErrNotFound), errors.Is(err, project.ErrNotFound), errors.Is(err, feature.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, conversations.ErrGoalActive):
		writeError(w, http.StatusConflict, "autopilot_active", "autopilot is already on; stop it first")
	case errors.Is(err, conversations.ErrBusy):
		writeError(w, http.StatusConflict, "assistant_busy", err.Error())
	case errors.Is(err, conversations.ErrNotPending):
		writeError(w, http.StatusConflict, "action_not_pending", err.Error())
	case errors.Is(err, conversations.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_assistant_request", err.Error())
	case errors.Is(err, conversations.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", err.Error())
	case errors.Is(err, execution.ErrNotFound):
		writeError(w, http.StatusNotFound, "run_not_found", "run not found")
	default:
		log.Printf("assistant: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (api *API) applyAddVisionItem(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	if api.visions == nil {
		writeError(w, http.StatusServiceUnavailable, "vision_unavailable", "the vision is unavailable")
		return "", false
	}
	if action.WorkOrderID != "" {
		if _, err := api.features.GetByID(r.Context(), projectID, action.WorkOrderID); err != nil {
			writeConversationError(w, err)
			return "", false
		}
	}
	_, _, err := api.visions.AddItem(r.Context(), projectID, action.Priority, action.Title, action.Description, action.WorkOrderID, "assistant")
	if err != nil {
		writeVisionError(w, projectID, err)
		return "", false
	}
	return fmt.Sprintf("Added \u201c%s\u201d to the vision as P%d.", action.Title, action.Priority), true
}
