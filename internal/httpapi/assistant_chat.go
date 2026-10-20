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
	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// ConversationService is the assistant (ASSISTANT_PLAN.md): conversations at
// the top level and per project, its settings, and the actions it proposes.
type ConversationService interface {
	Settings(ctx context.Context) (conversations.Settings, error)
	RequiresApproval(ctx context.Context, model string) bool
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

// autopilotApproval marks an approval autopilot makes for the assistant,
// not the user: it may not act on the user's own work orders (ADR-027).
type autopilotApproval struct{}

func approvedByAutopilot(ctx context.Context) bool {
	marked, _ := ctx.Value(autopilotApproval{}).(bool)
	return marked
}

// AssistantActionApplier approves assistant actions through handler exactly
// as the user's Approve button does, with a new work order started: how
// autopilot takes over an action. Unlike the user's approval, it is refused
// on a work order the user created. The error carries the API's message.
func AssistantActionApplier(handler http.Handler) func(ctx context.Context, scope, actionID string) error {
	return func(ctx context.Context, scope, actionID string) error {
		ctx = context.WithValue(ctx, autopilotApproval{}, true)
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

// conversationMessageRequest is the user's message, with whether the app
// shows Developer mode, which decides how far a new app may reach (ADR-035).
type conversationMessageRequest struct {
	Message       string `json:"message"`
	DeveloperMode *bool  `json:"developer_mode,omitempty"`
}

// developerModeSetter records whether the user uses Developer mode.
type developerModeSetter interface {
	SetDeveloperMode(ctx context.Context, scope string, on bool) error
}

func (api *API) sendConversationHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	var body conversationMessageRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	if setter, ok := api.conversations.(developerModeSetter); ok && body.DeveloperMode != nil {
		if err := setter.SetDeveloperMode(r.Context(), r.PathValue("scope"), *body.DeveloperMode); err != nil {
			writeConversationError(w, err)
			return
		}
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

// focusSetter focuses a project's conversation on an item (ADR-035).
type focusSetter interface {
	SetFocus(ctx context.Context, scope string, focus *conversations.Focus) (conversations.Conversation, error)
}

// setConversationFocusHandler focuses the conversation on what the user
// opened the assistant from; DELETE clears it.
func (api *API) setConversationFocusHandler(w http.ResponseWriter, r *http.Request) {
	setter, ok := api.conversations.(focusSetter)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "the assistant's focus is unavailable")
		return
	}
	var focus *conversations.Focus
	if r.Method != http.MethodDelete {
		focus = &conversations.Focus{}
		if !decodeSingleJSON(w, r, focus) {
			return
		}
	}
	conversation, err := setter.SetFocus(r.Context(), r.PathValue("scope"), focus)
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
	// LeadAgent, LeadModel, ReviewerAgent, and ReviewerModel replace the
	// agents and models a new or started work order runs on: the user's
	// choice over the assistant's, refused like any choice that cannot run
	// (ADR-027).
	LeadAgent     string `json:"lead_agent,omitempty"`
	LeadModel     string `json:"lead_model,omitempty"`
	ReviewerAgent string `json:"reviewer_agent,omitempty"`
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
	// At the top level the assistant only creates apps and projects to plan;
	// everything else acts in a project, except research, which needs none.
	topLevel := action.Type == featureartifact.AssistantCreateApp || action.Type == featureartifact.AssistantPlanProject
	if action.Type != featureartifact.AssistantResearch && (scope == conversations.TopLevel) != topLevel {
		writeError(w, http.StatusConflict, "action_needs_project", "this action runs in a project's conversation, and a new app at the top level")
		return
	}
	// A project is planned only in Developer mode; without it, an app stays
	// self-contained (ADR-035).
	if action.Type == featureartifact.AssistantPlanProject {
		conversation, err := api.conversations.Get(r.Context(), scope)
		if err != nil {
			writeConversationError(w, err)
			return
		}
		if !conversation.Developer {
			writeError(w, http.StatusConflict, "developer_mode_required",
				"planning a project needs Developer mode, in Settings; without it the assistant builds a self-contained app")
			return
		}
	}
	// The user's approval may act on any work order; autopilot's only on the
	// assistant's own (ADR-027).
	if approvedByAutopilot(r.Context()) {
		refusal, err := api.usersWorkOrder(r.Context(), scope, action)
		if err != nil {
			writeConversationError(w, err)
			return
		}
		if refusal != "" {
			writeError(w, http.StatusConflict, "work_order_is_users", refusal)
			return
		}
	}
	chosen := make([]string, 0, 4)
	if action.Type == featureartifact.AssistantCreateWorkOrder || action.Type == featureartifact.AssistantStartWorkOrder {
		for _, choice := range []struct {
			name           string
			value, current *string
		}{
			{"lead agent ", &body.LeadAgent, &action.LeadAgent},
			{"lead ", &body.LeadModel, &action.LeadModel},
			{"reviewer agent ", &body.ReviewerAgent, &action.ReviewerAgent},
			{"reviewer ", &body.ReviewerModel, &action.ReviewerModel},
		} {
			if value := strings.TrimSpace(*choice.value); value != "" && value != *choice.current {
				*choice.current = value
				chosen = append(chosen, choice.name+value)
			}
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
	// A helper the action started, whose questions and results come back to
	// the assistant (ADR-035).
	var helper *conversations.Helper
	switch action.Type {
	case featureartifact.AssistantCreateApp:
		result, projectID, ok = api.applyCreateApp(w, r, action)
	case featureartifact.AssistantPlanProject:
		result, projectID, ok = api.applyPlanProject(w, r, action)
	case featureartifact.AssistantCreateWorkOrder:
		// A work order queued behind other work is created now and started
		// later (HoldStart).
		// One whose brief is written first is created as a draft (ADR-035).
		result, workOrderID, ok = api.applyCreateWorkOrder(w, r, scope, action, body.Start && !action.HoldStart && !action.Brief)
		if ok && action.Brief {
			result, helper, ok = api.applyWriteBrief(w, r, scope, workOrderID, action.Description, "assistant-"+action.ID)
		}
	case featureartifact.AssistantWriteBrief:
		result, helper, ok = api.applyWriteBrief(w, r, scope, action.WorkOrderID, action.Description, "assistant-"+action.ID)
	case featureartifact.AssistantAcceptBrief:
		result, ok = api.applyAcceptBrief(w, r, scope, action)
	case featureartifact.AssistantDesignStack:
		result, helper, ok = api.applyDesignStack(w, r, scope, action)
	case featureartifact.AssistantApplyStack:
		result, ok = api.applyStack(w, r, scope, action)
	case featureartifact.AssistantStartWorkOrder:
		result, ok = api.applyStartWorkOrder(w, r, scope, action)
	case featureartifact.AssistantChangeModels:
		result, ok = api.applyChangeModels(w, r, scope, action)
	case featureartifact.AssistantAddVisionItem:
		result, ok = api.applyAddVisionItem(w, r, scope, action)
	case featureartifact.AssistantContinueWorkOrder:
		action.How = "continue"
		result, ok = api.applyRecoverWorkOrder(w, r, scope, action)
	case featureartifact.AssistantRecoverWorkOrder:
		result, ok = api.applyRecoverWorkOrder(w, r, scope, action)
	case featureartifact.AssistantSetUpPreview:
		result, workOrderID, ok = api.applySetUpPreview(w, r, scope, action)
	case featureartifact.AssistantWriteVision:
		result, helper, ok = api.applyWriteVision(w, r, scope, action)
	case featureartifact.AssistantAnswerHelper:
		result, ok = api.applyAnswerHelper(w, r, scope, action)
	case featureartifact.AssistantResearch:
		result, helper, ok = api.applyResearch(w, r, scope, action)
	case featureartifact.AssistantSetUpStack:
		result, workOrderID, ok = api.applySetUpStack(w, r, scope)
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
	conversation, err := api.conversations.Resolve(r.Context(), scope, actionID, conversations.ActionApplied, result, conversations.Created{WorkOrderID: workOrderID, ProjectID: projectID, Helper: helper})
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conversation, "assistant conversation")
}

// usersWorkOrder says why the assistant may not act on its own on the work
// order an action names, when the user created it by hand and did not hand
// it over with an autopilot goal; empty when it may.
func (api *API) usersWorkOrder(ctx context.Context, scope string, action conversations.Action) (string, error) {
	switch action.Type {
	case featureartifact.AssistantStartWorkOrder, featureartifact.AssistantChangeModels,
		featureartifact.AssistantContinueWorkOrder, featureartifact.AssistantRecoverWorkOrder,
		featureartifact.AssistantCloseWorkOrder, featureartifact.AssistantWriteBrief, featureartifact.AssistantAcceptBrief,
		featureartifact.AssistantAddVisionItem:
	default:
		return "", nil
	}
	if action.WorkOrderID == "" {
		return "", nil
	}
	stored, err := api.features.GetByID(ctx, scope, action.WorkOrderID)
	if errors.Is(err, feature.ErrNotFound) {
		// The action's own path answers a missing work order.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if stored.CreatedBy == feature.CreatedByAssistant {
		return "", nil
	}
	// Building everything, the assistant takes over the user's work orders.
	if owner, err := api.projects.GetByID(ctx, scope); err == nil && owner.AssistantBuilds() {
		return "", nil
	}
	conversation, err := api.conversations.Get(ctx, scope)
	if err != nil {
		return "", err
	}
	if conversation.Goal.Hands(stored.ID) {
		return "", nil
	}
	return fmt.Sprintf("“%s” is the user's work order: Commitarium does not start, continue, change, or close it "+
		"on its own. Tell the user it is waiting for them; your card stays for their approval.", stored.Title), nil
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
	if _, err := api.projects.GetByID(ctx, projectID); err != nil {
		writeConversationError(w, err)
		return "", "", false
	}
	overrides, err := api.assistantSettings(ctx, action.AssistantAction)
	if err != nil {
		if !writeChoiceError(w, err) {
			writeConversationError(w, err)
		}
		return "", "", false
	}
	if start {
		// A work order the action starts must run: refuse before creating it.
		if overrides.AgentProviders == nil {
			writeChoiceError(w, errIncompleteAssistantChoice)
			return "", "", false
		}
		skipReviewer := overrides.Steps != nil && overrides.Steps.SkipReviewer
		if err := api.checkWorkChoice(ctx, *overrides.AgentProviders, *overrides.AgentModels, skipReviewer); err != nil {
			if !writeChoiceError(w, err) {
				writeConversationError(w, err)
			}
			return "", "", false
		}
	}
	description := action.Description
	if action.VisionItemID != "" && api.visions != nil {
		if current, err := api.visions.Get(ctx, projectID); err == nil {
			if _, label, found := vision.Find(current.Plan, action.VisionItemID); found {
				description = strings.TrimSpace(description + "\n\nPart of the project's vision as " + label + ".")
			}
		}
	}
	// The assistant's work order is its own to drive (ADR-027).
	overrides.CreatedBy = feature.CreatedByAssistant
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
	request := &startRunRequest{}
	clarification := false
	request.Steps = &workStepsRequest{Clarification: &clarification}
	if action.Steps != "" {
		plan := action.Steps != "quick"
		reviewer := action.Steps == "full" || action.Steps == "reviewed_plan"
		codeReview := action.Steps == "full"
		request.Steps.DocumentedPlan, request.Steps.Reviewer, request.Steps.CodeReview = &plan, &reviewer, &codeReview
	}
	// The action's agents and models replace the work order's own; a role the
	// action leaves out keeps the work order's. Nothing comes from the
	// project (ADR-027); a reviewer the steps skip is the lead.
	skipReviewer := stored.Steps.SkipReviewer
	if action.Steps != "" {
		skipReviewer = action.Steps != "full" && action.Steps != "reviewed_plan"
	}
	if action.LeadAgent != "" || action.ReviewerAgent != "" {
		lead := orDefault(action.LeadAgent, stored.AgentProviders.LeadAgent)
		request.AgentProviders = &agentProvidersRequest{LeadAgent: &lead}
		if reviewer := orDefault(action.ReviewerAgent, stored.AgentProviders.ReviewerAgent); reviewer != "" && !skipReviewer {
			request.AgentProviders.ReviewerAgent = &reviewer
		}
	}
	if action.LeadModel != "" || action.ReviewerModel != "" {
		lead := orDefault(action.LeadModel, stored.AgentModels.Lead)
		request.AgentModels = &agentModelsRequest{Lead: &lead}
		if reviewer := orDefault(action.ReviewerModel, stored.AgentModels.Reviewer); reviewer != "" && !skipReviewer {
			request.AgentModels.Reviewer = &reviewer
		}
	}
	request.PhaseChoices = action.PhaseChoices()
	if err := api.checkEffortCeiling(ctx, request.PhaseChoices); err != nil {
		if !writeChoiceError(w, err) {
			writeConversationError(w, err)
		}
		return "", false
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
	api.dropPlanModels(ctx, stored.ID, models)
	return fmt.Sprintf("Switched the models of “%s”.", stored.Title), true
}

// premiumModels lists the models an action would run on that need the
// user's explicit approval.
func (api *API) premiumModels(ctx context.Context, projectID string, action conversations.Action) ([]string, error) {
	models := []string{action.LeadModel, action.ReviewerModel, action.LeadPlanModel, action.ReviewerPlanModel}
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
		if api.conversations.RequiresApproval(ctx, model) {
			premium = append(premium, model)
		}
	}
	return premium, nil
}

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

// assistantSettings turns an action's steps, agents, models, and tests into
// a work order's settings. The agents and models are the assistant's choice
// (ADR-027): an action names them all, or none for a draft whose agents are
// chosen at Start; nothing is filled from the project.
func (api *API) assistantSettings(
	ctx context.Context, action featureartifact.AssistantAction,
) (feature.SettingsOverrides, error) {
	var overrides feature.SettingsOverrides
	switch action.Steps {
	case "quick":
		overrides.Steps = &project.WorkSteps{SkipPlan: true, SkipReviewer: true}
	case "planned":
		overrides.Steps = &project.WorkSteps{SkipReviewer: true}
	case "reviewed_plan":
		overrides.Steps = &project.WorkSteps{SkipCodeReview: true}
	case "full":
		overrides.Steps = &project.WorkSteps{}
	}
	if action.LeadAgent == "" && action.ReviewerAgent == "" && action.LeadModel == "" && action.ReviewerModel == "" {
		return overrides, nil
	}
	skipReviewer := overrides.Steps != nil && overrides.Steps.SkipReviewer
	if action.LeadAgent == "" || action.LeadModel == "" ||
		(!skipReviewer && (action.ReviewerAgent == "" || action.ReviewerModel == "")) {
		return feature.SettingsOverrides{}, errIncompleteAssistantChoice
	}
	request := &agentProvidersRequest{LeadAgent: &action.LeadAgent}
	models := &agentModelsRequest{Lead: &action.LeadModel}
	if !skipReviewer {
		request.ReviewerAgent, models.Reviewer = &action.ReviewerAgent, &action.ReviewerModel
	}
	fillSkippedReviewer(request, models, skipReviewer)
	providers, err := api.decodeAgentProviders(ctx, request, false)
	if err != nil {
		return feature.SettingsOverrides{}, &choiceError{status: http.StatusBadRequest, code: "agent_not_found",
			message: "the action names an agent that does not exist; use agent IDs exactly as the state lists them"}
	}
	chosen := project.AgentModels{Lead: *models.Lead, Reviewer: *models.Reviewer}
	if err := api.modelOffered(ctx, providers.LeadAgent, providers.Lead, modelcatalog.RoleLead, chosen.Lead); err != nil {
		return feature.SettingsOverrides{}, err
	}
	if !skipReviewer {
		if err := api.modelOffered(ctx, providers.ReviewerAgent, providers.Reviewer, modelcatalog.RoleReviewer, chosen.Reviewer); err != nil {
			return feature.SettingsOverrides{}, err
		}
	}
	overrides.AgentProviders, overrides.AgentModels = &providers, &chosen
	if choices := action.PhaseChoices(); choices != nil {
		if err := api.checkEffortCeiling(ctx, choices); err != nil {
			return feature.SettingsOverrides{}, err
		}
		if err := api.checkPhaseChoices(ctx, providers, chosen, *choices, skipReviewer); err != nil {
			return feature.SettingsOverrides{}, err
		}
		overrides.PhaseChoices = choices
	}
	return overrides, nil
}

var errIncompleteAssistantChoice = &choiceError{status: http.StatusBadRequest, code: "agents_not_chosen",
	message: "name lead_agent and lead_model, and reviewer_agent and reviewer_model when the steps have a reviewer"}

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
	case errors.Is(err, conversations.ErrNoAgents):
		writeError(w, http.StatusConflict, "no_agents", err.Error())
	case errors.Is(err, conversations.ErrNotSetUp):
		writeError(w, http.StatusConflict, "assistant_not_set_up", err.Error())
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
