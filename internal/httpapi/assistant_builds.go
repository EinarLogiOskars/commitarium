package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// Building everything (ADR-027), the assistant owns the project but acts
// only when woken. What the user does there wakes it: switching the project
// to building everything hands it over, and a work order the user creates by
// hand is the assistant's to take over. The watcher's gap check
// (conversations/gaps.go) wakes it for what is still missing.

// assistantWaker wakes a project's assistant for what the user did.
type assistantWaker interface {
	Wake(ctx context.Context, scope, note, text string) error
	HandOver(ctx context.Context, scope string) error
}

// ownSetup marks the requests Commitarium sends its own routes while it sets
// a new app up: its launch moves it along, so they wake no one.
type ownSetup struct{}

func inOwnSetup(ctx context.Context) bool {
	marked, _ := ctx.Value(ownSetup{}).(bool)
	return marked
}

// handOverToAssistant wakes the assistant of a project the user just
// switched to building everything.
func (api *API) handOverToAssistant(ctx context.Context, projectID string) {
	waker, ok := api.conversations.(assistantWaker)
	if !ok || inOwnSetup(ctx) {
		return
	}
	if err := waker.HandOver(ctx, projectID); err != nil {
		log.Printf("hand project %q over to the assistant: %v", projectID, err)
	}
}

// wakeForUsersWorkOrder wakes the assistant of a project it builds
// everything in for a work order the user created by hand.
func (api *API) wakeForUsersWorkOrder(ctx context.Context, created feature.Feature) {
	waker, ok := api.conversations.(assistantWaker)
	if !ok || api.projects == nil || created.Kind == feature.KindSession || created.Background {
		return
	}
	owner, err := api.projects.GetByID(ctx, created.ProjectID)
	if err != nil || !owner.AssistantBuilds() {
		return
	}
	note := fmt.Sprintf("You added “%s”; the assistant takes it from here.", created.Title)
	text := fmt.Sprintf("The user created the work order “%s” (%s) by hand. You build everything here, so it is "+
		"yours: start it with the agents and models you choose, or queue it with after when it changes the same "+
		"part of the app as running work. Unless the user started it already.", created.Title, created.ID)
	if err := waker.Wake(ctx, created.ProjectID, note, text); err != nil {
		log.Printf("tell the assistant of project %q about work order %q: %v", created.ProjectID, created.ID, err)
	}
}

// applyWriteVision has the vision written on the assistant's agent from the
// action's description, as a new app's is. The vision writer is the
// assistant's helper: its questions go to the assistant (ADR-035).
func (api *API) applyWriteVision(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, *conversations.Helper, bool) {
	ctx := r.Context()
	if api.visions == nil {
		writeError(w, http.StatusServiceUnavailable, "vision_unavailable", "the vision is unavailable")
		return "", nil, false
	}
	if _, err := api.visions.Get(ctx, projectID); err == nil {
		writeError(w, http.StatusConflict, "vision_exists",
			"the project has a vision already; propose add_vision_item to add to it")
		return "", nil, false
	} else if !errors.Is(err, vision.ErrNotFound) {
		writeVisionError(w, projectID, err)
		return "", nil, false
	}
	var session vision.Session
	err := api.callOwn(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(projectID)+"/vision/assistant",
		"assistant-"+action.ID, map[string]string{"mode": string(vision.ModeDescribe), "message": action.Description}, &session)
	if err != nil {
		writeError(w, http.StatusConflict, "vision_not_started", "the vision could not be started: "+err.Error())
		return "", nil, false
	}
	return "Writing the vision.", &conversations.Helper{ID: session.ID, Kind: conversations.HelperVision}, true
}

// helperFinder finds a helper the assistant started.
type helperFinder interface {
	LookUpHelper(ctx context.Context, scope, id string) (conversations.Helper, error)
}

// applyAnswerHelper sends the assistant's answer to a helper it started,
// through the helper's own reply endpoint, as the helper's page would.
func (api *API) applyAnswerHelper(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	ctx := r.Context()
	finder, ok := api.conversations.(helperFinder)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "helpers are unavailable")
		return "", false
	}
	helper, err := finder.LookUpHelper(ctx, projectID, action.Helper)
	if errors.Is(err, conversations.ErrNotFound) {
		writeError(w, http.StatusNotFound, "helper_not_found",
			"no helper you started goes by that name; answer only the helpers Commitarium reports")
		return "", false
	} else if err != nil {
		writeConversationError(w, err)
		return "", false
	}
	if err := api.callOwn(ctx, http.MethodPost, helperPath(projectID, helper)+"/messages", "assistant-"+action.ID,
		map[string]string{"message": action.Guidance}, nil); err != nil {
		writeError(w, http.StatusConflict, "helper_not_answered", "the helper could not be answered: "+err.Error())
		return "", false
	}
	return "Answered " + helper.Name() + ".", true
}

// helperPath is the route of a helper's session.
func helperPath(projectID string, helper conversations.Helper) string {
	base := "/api/v1/projects/" + url.PathEscape(projectID)
	switch helper.Kind {
	case conversations.HelperBrief:
		return base + "/features/" + url.PathEscape(helper.WorkOrderID) + "/assistant"
	case conversations.HelperStack:
		return base + "/toolchain/assistant-sessions/" + url.PathEscape(helper.ID)
	default:
		return base + "/vision/assistant/" + url.PathEscape(helper.ID)
	}
}

// HelperReader reads the helpers the assistant started through handler, as
// their own pages do: how the watcher hears their questions and results
// (ADR-035). A helper whose session is gone is finished.
func HelperReader(handler http.Handler) conversations.HelperReader {
	return func(ctx context.Context, projectID string, helper conversations.Helper) (conversations.HelperState, error) {
		switch helper.Kind {
		case conversations.HelperVision:
			var session vision.Session
			if found, err := readOwn(ctx, handler, helperPath(projectID, helper), &session); err != nil || !found {
				return conversations.HelperState{Status: conversations.HelperFinished}, err
			}
			state := conversations.HelperState{Turn: len(session.Messages)}
			switch session.Status {
			case vision.StatusWaitingForUser:
				state.Status, state.Text = conversations.HelperAsks, lastReply(session.Messages)
			case vision.StatusWritten:
				state.Status = conversations.HelperFinished
			case vision.StatusFailed:
				state.Status = conversations.HelperFailed
			default:
				state.Status = conversations.HelperWorking
			}
			return state, nil
		}
		return conversations.HelperState{}, fmt.Errorf("unknown helper kind %q", helper.Kind)
	}
}

func lastReply(messages []vision.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "assistant" {
			return strings.TrimSpace(messages[index].Text)
		}
	}
	return ""
}

// readOwn reads one of Commitarium's own routes through handler; found is
// false when it answers 404.
func readOwn(ctx context.Context, handler http.Handler, path string, into any) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, err
	}
	response := &recordedResponse{header: http.Header{}, status: http.StatusOK}
	handler.ServeHTTP(response, request)
	switch {
	case response.status == http.StatusNotFound:
		return false, nil
	case response.status >= 300:
		return false, fmt.Errorf("HTTP %d: %s", response.status, strings.TrimSpace(response.body.String()))
	}
	return true, json.Unmarshal(response.body.Bytes(), into)
}

// applySetUpStack sets the project's stack up: detected from its code when
// it has some, else the stack its vision recommends, set up by a work order.
func (api *API) applySetUpStack(
	w http.ResponseWriter, r *http.Request, projectID string,
) (string, string, bool) {
	ctx := r.Context()
	if api.toolchains == nil {
		writeError(w, http.StatusServiceUnavailable, "project_setup_unavailable", "setting a project up needs real agents")
		return "", "", false
	}
	manifest, err := api.projectToolchain(ctx, projectID)
	if err != nil {
		api.writeToolchainError(w, projectID, "set the stack up for the assistant", err)
		return "", "", false
	}
	if manifest.Status == toolchain.StatusConfigured {
		tools := make([]string, 0, len(manifest.Tools))
		for tool, version := range manifest.Tools {
			tools = append(tools, tool+" "+version)
		}
		sort.Strings(tools)
		return "The stack is set up: " + strings.Join(tools, ", ") + ".", "", true
	}
	var presets []string
	frameworks := ""
	if api.visions != nil {
		if current, err := api.visions.Get(ctx, projectID); err == nil && current.Plan.Stack != nil {
			presets, frameworks = current.Plan.Stack.Presets, current.Plan.Stack.Frameworks
		}
	}
	if len(presets) == 0 {
		writeError(w, http.StatusConflict, "stack_unknown", "the stack is not known yet: write the vision first, "+
			"which recommends one, or ask the user to choose it on the Stack page")
		return "", "", false
	}
	started, _, _, err := api.setUpProject(ctx, projectID, presets, frameworks)
	if err != nil {
		writeProjectSetupError(w, api, projectID, err)
		return "", "", false
	}
	return fmt.Sprintf("Setting the project up on %s in “%s”.", strings.Join(presets, " and "), started.Title),
		started.ID, true
}
