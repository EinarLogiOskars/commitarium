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
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
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
// start once the card is applied (startCreated), after the chat that
// proposed it has moved in, in Developer mode (ADR-038).
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
// from the brief. Each step repeats safely, so a retry runs them all again.
func (api *API) startApp(ctx context.Context, action conversations.Action, projectID string) error {
	ctx = context.WithValue(ctx, ownSetup{}, true)
	notes := ""
	if action.AppBrief != "" {
		notes = "The app's brief, settled with the user before it was created:\n\n" + action.AppBrief
	}
	conversation := "/api/v1/assistant/conversations/" + url.PathEscape(projectID)
	for _, step := range []appStep{
		{http.MethodPut, conversation + "/notes", "", map[string]string{"notes": notes}},
		{http.MethodPost, conversation + "/launch", "", nil},
		{http.MethodPost, "/api/v1/projects/" + url.PathEscape(projectID) + "/vision/assistant",
			"assistant-" + action.ID + ":vision", map[string]string{"mode": "describe", "message": appBrief(action)}},
	} {
		if err := api.callOwn(ctx, step.method, step.path, step.key, step.body, nil); err != nil {
			return err
		}
	}
	return nil
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
// Once the card is applied, startCreated moves the chat into it and starts
// its planning. Nothing is built until the user says so.
func (api *API) applyPlanProject(w http.ResponseWriter, r *http.Request, action conversations.Action) (string, string, bool) {
	projectID, ok := api.createPlannedProject(w, r, action.Title, "assistant-"+action.ID+":project")
	if !ok {
		return "", "", false
	}
	return fmt.Sprintf("Created the project “%s”. Plan it with Mayka, then build it.", action.Title), projectID, true
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
// it came from moves into it as one of its chats, and startPlanning starts
// its planning there. It answers the moved chat, or an empty conversation
// when the chat could not move (an assistant without chats).
func (api *API) planProject(
	w http.ResponseWriter, r *http.Request, chat, projectID, name, notes, goal string,
) (conversations.Conversation, bool) {
	// The request may act on the top-level chat it came from, which the
	// project does not have: its calls act on the moved chat, or on the
	// project's most recently used one.
	ctx := conversations.InChat(r.Context(), "")
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
	if err := api.startPlanning(ctx, projectID, moved, notes, goal); err != nil {
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

// startPlanning starts a new project's planning in the chat that moved into
// it (moved, or none): its assistant keeps notes in its own notes, and its
// vision is planned from goal with the user. The assistant hears the chat's
// transcript as planning starts, so nothing said there is asked again.
func (api *API) startPlanning(ctx context.Context, projectID string, moved conversations.Conversation, notes, goal string) error {
	ctx = conversations.InChat(ctx, moved.Chat)
	conversation := "/api/v1/assistant/conversations/" + url.PathEscape(projectID)
	if notes != "" {
		if err := api.callOwn(ctx, http.MethodPut, conversation+"/notes", "", map[string]string{"notes": notes}, nil); err != nil {
			return err
		}
	}
	// Starting it wakes the project's assistant to plan it with the user.
	return api.callOwn(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(projectID)+"/vision/planning", "",
		startPlanningRequest{Goal: goal, Transcript: transcript(moved.Messages), Chat: moved.Chat, Idea: goal}, nil)
}

// startCreated runs what follows an applied create_app or plan_project card
// (ADR-038): the loose chat that proposed it moves into the new project
// with the card (always for a project to plan, in Developer mode for an
// app), then the app starts or the project's planning does. A step that
// fails is recorded on the card rather than answered as an error, so the
// app follows the chat to where the card now is; approving the card again
// (retry, from scope, where the card is) runs these steps once more on the
// same project. A chat that already moved is not moved again. It answers
// the conversation that holds the card.
func (api *API) startCreated(
	ctx context.Context, scope string, action conversations.Action, projectID string, developer, retry bool,
) (conversations.Conversation, error) {
	ctx = conversations.InChat(ctx, "")
	where, chat := scope, action.Chat
	var moved conversations.Conversation
	err := func() error {
		if retry && scope != conversations.TopLevel {
			// The chat moved on an earlier try; the card is in it.
			current, err := api.conversations.Get(conversations.InChat(ctx, chat), projectID)
			if err != nil {
				return err
			}
			moved = current
		} else if mover, ok := api.conversations.(chatMover); ok && chat != "" &&
			(developer || action.Type == featureartifact.AssistantPlanProject) {
			current, err := api.moveChatInto(ctx, mover, chat, action.ID, projectID)
			if err != nil {
				return fmt.Errorf("this chat could not move into it: %w", err)
			}
			moved, where, chat = current, projectID, current.Chat
		}
		if action.Type == featureartifact.AssistantCreateApp {
			// The app's calls act on the moved chat, or on its most recently
			// used one: it does not have the top-level chat.
			return api.startApp(conversations.InChat(ctx, moved.Chat), action, projectID)
		}
		notes := "The project's brief, settled with the user before it was created:\n\n" + appBrief(action)
		return api.startPlanning(ctx, projectID, moved, notes, action.Description)
	}()
	failure := ""
	if err != nil && action.Type == featureartifact.AssistantCreateApp {
		failure = fmt.Sprintf("The app “%s” was created, but starting it failed: %v", action.Title, err)
	} else if err != nil {
		failure = fmt.Sprintf("The project “%s” was created, but its planning could not start: %v", action.Title, err)
	}
	if recorder, ok := api.conversations.(startRecorder); ok && (err != nil || action.StartFailed != "") {
		if _, recordErr := recorder.RecordStart(ctx, where, action.ID, failure); recordErr != nil {
			return conversations.Conversation{}, recordErr
		}
	} else if err != nil {
		return conversations.Conversation{}, errors.New(failure)
	}
	return api.conversations.Get(conversations.InChat(ctx, chat), where)
}

// startRecorder records how what follows a created app or project went.
type startRecorder interface {
	RecordStart(ctx context.Context, scope, actionID, failure string) (conversations.Conversation, error)
}

// moveChatInto moves a top-level chat into a project. A chat an earlier,
// partly failed try already moved is found in the project by the card that
// moved it (actionID), so a retry carries on there instead of failing.
func (api *API) moveChatInto(
	ctx context.Context, mover chatMover, chat, actionID, projectID string,
) (conversations.Conversation, error) {
	moved, err := mover.MoveChat(ctx, conversations.TopLevel, chat, projectID)
	if !errors.Is(err, conversations.ErrNotFound) || actionID == "" {
		return moved, err
	}
	action, findErr := api.conversations.Action(conversations.InChat(ctx, ""), projectID, actionID)
	if findErr != nil || action.Chat == "" {
		return conversations.Conversation{}, err
	}
	return api.conversations.Get(conversations.InChat(ctx, action.Chat), projectID)
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
