package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// applyCreateApp creates the app the user and the assistant settled on
// (APPS_PLAN.md): a project used as an app that runs to its merge gate and
// merges on its own; its launch; and its vision, written from the agreed
// description and the brief the assistant gathered, in full (ADR-035). The
// brief also becomes the new app's assistant notes, so the assistant there
// knows what was settled. No template project lends it agents or models (ADR-027): the
// assistant writes the vision and sets the stack up on its own agent, and
// chooses each work order's agents and models as autopilot builds the app.
// Each step goes through the API's own route, so it keeps that route's
// checks; a retry repeats them with the same keys. Its launch and vision
// start once the card is applied (startApp), after the chat that proposed
// it has moved in, in Developer mode (ADR-038).
func (api *API) applyCreateApp(w http.ResponseWriter, r *http.Request, action conversations.Action) (string, string, bool) {
	ctx := r.Context()
	key := "assistant-" + action.ID
	// Nothing is created unless the assistant can do the app's work.
	if _, _, err := api.assistantAgent(ctx); err != nil {
		if !writeChoiceError(w, err) {
			writeConversationError(w, err)
		}
		return "", "", false
	}
	body := map[string]any{
		"name":            action.Title,
		"merge_policy":    project.MergePolicyAutoAfterGates,
		"autonomy_policy": project.AutonomyPolicyRunToCompletion,
	}
	var created projectResponse
	if err := api.callOwn(ctx, http.MethodPost, "/api/v1/projects", key+":project", body, &created); err != nil {
		writeError(w, http.StatusBadGateway, "app_not_created", "the app could not be created: "+err.Error())
		return "", "", false
	}
	base := "/api/v1/projects/" + url.PathEscape(created.ID)
	// Setting it to build everything hands nothing over yet.
	ctx = context.WithValue(ctx, ownSetup{}, true)
	steps := []appStep{
		// The user asked the assistant to create it, so the assistant builds
		// everything (ADR-027).
		{http.MethodPut, base + "/assistant-mode", "", map[string]project.AssistantMode{"assistant_mode": project.AssistantModeBuilds}},
		{http.MethodPut, base + "/app", "", map[string]bool{"app": true}},
	}
	if !api.runAppSteps(w, ctx, action, steps) {
		return "", "", false
	}
	return fmt.Sprintf("Created the app “%s”. It is being planned, set up, and built.", action.Title), created.ID, true
}

// startApp starts the app applyCreateApp created: its assistant keeps the
// brief in its notes, its launch moves it along, and its vision is written
// from the brief.
func (api *API) startApp(w http.ResponseWriter, r *http.Request, action conversations.Action, projectID string) bool {
	ctx := context.WithValue(r.Context(), ownSetup{}, true)
	notes := ""
	if action.AppBrief != "" {
		notes = "The app's brief, settled with the user before it was created:\n\n" + action.AppBrief
	}
	conversation := "/api/v1/assistant/conversations/" + url.PathEscape(projectID)
	return api.runAppSteps(w, ctx, action, []appStep{
		{http.MethodPut, conversation + "/notes", "", map[string]string{"notes": notes}},
		{http.MethodPost, conversation + "/launch", "", nil},
		{http.MethodPost, "/api/v1/projects/" + url.PathEscape(projectID) + "/vision/assistant",
			"assistant-" + action.ID + ":vision", map[string]string{"mode": "describe", "message": appBrief(action)}},
	})
}

// appStep is one request to the API's own routes while a new app starts.
type appStep struct {
	method, path, key string
	body              any
}

func (api *API) runAppSteps(w http.ResponseWriter, ctx context.Context, action conversations.Action, steps []appStep) bool {
	for _, step := range steps {
		if err := api.callOwn(ctx, step.method, step.path, step.key, step.body, nil); err != nil {
			writeError(w, http.StatusBadGateway, "app_not_started",
				fmt.Sprintf("the app %q was created, but starting it failed: %v", action.Title, err))
			return false
		}
	}
	return true
}

// applyPlanProject creates the project the user and the assistant agreed to
// plan, in Developer mode (ADR-035): a project like one from New project.
// Once the card is applied, planProject moves the chat into it and starts
// its planning. Nothing is built until the user says so.
func (api *API) applyPlanProject(w http.ResponseWriter, r *http.Request, action conversations.Action) (string, string, bool) {
	projectID, ok := api.createPlannedProject(w, r, action.Title, "assistant-"+action.ID+":project")
	if !ok {
		return "", "", false
	}
	return fmt.Sprintf("Created the project “%s”. Plan it with the assistant, then build it.", action.Title), projectID, true
}

// createPlannedProject creates a project to plan, once the assistant can do
// its planning. key makes a retry find the same project.
func (api *API) createPlannedProject(w http.ResponseWriter, r *http.Request, name, key string) (string, bool) {
	ctx := r.Context()
	if _, _, err := api.assistantAgent(ctx); err != nil {
		if !writeChoiceError(w, err) {
			writeConversationError(w, err)
		}
		return "", false
	}
	var created projectResponse
	if err := api.callOwn(ctx, http.MethodPost, "/api/v1/projects", key, map[string]any{"name": name}, &created); err != nil {
		writeError(w, http.StatusBadGateway, "project_not_created", "the project could not be created: "+err.Error())
		return "", false
	}
	return created.ID, true
}

// planProject starts planning a new project (ADR-038): the top-level chat
// it came from moves into it as one of its chats, its assistant keeps notes
// in its own notes, and its vision is planned from goal with the user. The
// assistant hears the chat's transcript as planning starts, so nothing said
// there is asked again. It answers the moved chat, or an empty conversation
// when the chat could not move (an assistant without chats).
func (api *API) planProject(
	w http.ResponseWriter, r *http.Request, chat, projectID, name, notes, goal string,
) (conversations.Conversation, bool) {
	ctx := r.Context()
	failed := func(err error) (conversations.Conversation, bool) {
		writeError(w, http.StatusBadGateway, "project_not_started",
			fmt.Sprintf("the project %q was created, but its planning could not start: %v", name, err))
		return conversations.Conversation{}, false
	}
	var moved conversations.Conversation
	if mover, ok := api.conversations.(chatMover); ok && chat != "" {
		var err error
		if moved, err = mover.MoveChat(ctx, conversations.TopLevel, chat, projectID); err != nil {
			return failed(err)
		}
	}
	conversation := "/api/v1/assistant/conversations/" + url.PathEscape(projectID)
	if notes != "" {
		if err := api.callOwn(ctx, http.MethodPut, conversation+"/notes", "", map[string]string{"notes": notes}, nil); err != nil {
			return failed(err)
		}
	}
	// Starting it wakes the project's assistant to plan it with the user.
	if err := api.callOwn(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(projectID)+"/vision/planning", "",
		startPlanningRequest{Goal: goal, Transcript: transcript(moved.Messages), Chat: moved.Chat, Idea: goal}, nil); err != nil {
		return failed(err)
	}
	if moved.Scope == "" {
		return moved, true
	}
	current, err := api.conversations.Get(conversations.InChat(ctx, moved.Chat), projectID)
	if err != nil {
		return moved, true
	}
	return current, true
}

// appBrief is what the vision writer hears of a new app: the summary, the
// brief the assistant gathered, and what it assumed.
func appBrief(action conversations.Action) string {
	text := action.Description
	if action.AppBrief != "" {
		text += "\n\nWhat the user and the assistant settled:\n" + action.AppBrief
	}
	if len(action.Assumptions) > 0 {
		text += "\n\nAssumed where the user did not say:\n- " + strings.Join(action.Assumptions, "\n- ")
	}
	return text
}

// callOwn sends a request to the API's own routes and decodes the answer.
func (api *API) callOwn(ctx context.Context, method, path, key string, body, into any) error {
	if api.mux == nil {
		return errors.New("the API is not ready")
	}
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(ctx, method, path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := &recordedResponse{header: http.Header{}, status: http.StatusOK}
	api.mux.ServeHTTP(response, request)
	if response.status >= 300 {
		var failure errorResponse
		if json.Unmarshal(response.body.Bytes(), &failure) == nil && failure.Error.Message != "" {
			return errors.New(failure.Error.Message)
		}
		return fmt.Errorf("HTTP %d", response.status)
	}
	if into != nil {
		return json.Unmarshal(response.body.Bytes(), into)
	}
	return nil
}
