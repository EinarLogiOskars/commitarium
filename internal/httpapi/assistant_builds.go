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

	"github.com/EinarLogiOskars/commitarium/internal/assistant"
	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
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

// applyWriteVision has the vision written, or the one there is rewritten, on
// the assistant's agent from the action's description, as a new app's is. The vision writer is the
// assistant's helper: its questions go to the assistant (ADR-035).
func (api *API) applyWriteVision(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, *conversations.Helper, bool) {
	ctx := r.Context()
	if api.visions == nil {
		writeError(w, http.StatusServiceUnavailable, "vision_unavailable", "the vision is unavailable")
		return "", nil, false
	}
	result := "Writing the vision."
	if _, err := api.visions.Get(ctx, projectID); err == nil {
		// The writer revises the vision there is, keeping its work orders.
		result = "Rewriting the vision."
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
	return result, &conversations.Helper{ID: session.ID, Kind: conversations.HelperVision}, true
}

// applyWriteBrief has the brief writer write a draft work order's brief on
// the assistant's agent from what the assistant gathered (ADR-035). A brief
// writer that already stopped, asked, or proposed one hears the description
// as its next message, so it proposes the brief again.
func (api *API) applyWriteBrief(
	w http.ResponseWriter, r *http.Request, projectID, workOrderID, description, key string,
) (string, *conversations.Helper, bool) {
	ctx := r.Context()
	if api.assistant == nil {
		writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "writing briefs needs real agents")
		return "", nil, false
	}
	stored, err := api.features.GetByID(ctx, projectID, workOrderID)
	if err != nil {
		writeConversationError(w, err)
		return "", nil, false
	}
	if stored.State != feature.StateDraft {
		writeError(w, http.StatusConflict, "feature_not_draft", "only a draft work order gets a brief written")
		return "", nil, false
	}
	helper := conversations.Helper{Kind: conversations.HelperBrief, WorkOrderID: stored.ID, Title: stored.Title}
	path := helperPath(projectID, helper)
	var session assistantSessionResponse
	found, err := readOwn(ctx, api.mux, path, &session)
	switch {
	case err != nil:
	case found && session.Status == string(assistant.StatusRunning):
		writeError(w, http.StatusConflict, "brief_writer_busy", "the brief writer is still working on it")
		return "", nil, false
	case found:
		err = api.callOwn(ctx, http.MethodPost, path+"/messages", key, map[string]string{"message": description}, &session)
	default:
		providers, model, choiceErr := api.assistantAgent(ctx)
		if choiceErr != nil {
			if !writeChoiceError(w, choiceErr) {
				writeConversationError(w, choiceErr)
			}
			return "", nil, false
		}
		err = api.callOwn(ctx, http.MethodPost, path, key, startAssistantRequest{
			Agent: providers.LeadAgent, Model: model, Message: description,
		}, &session)
	}
	if err != nil {
		writeError(w, http.StatusConflict, "brief_not_started", "the brief could not be started: "+err.Error())
		return "", nil, false
	}
	helper.ID = session.ID
	return fmt.Sprintf("Writing the brief for “%s”.", stored.Title), &helper, true
}

// applyAcceptBrief accepts the brief the brief writer proposed, as the
// work order's own Accept does: it becomes Ready.
func (api *API) applyAcceptBrief(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	path := "/api/v1/projects/" + url.PathEscape(projectID) + "/features/" + url.PathEscape(action.WorkOrderID) + "/accept"
	if err := api.callOwn(r.Context(), http.MethodPost, path, "assistant-"+action.ID, nil, nil); err != nil {
		writeError(w, http.StatusConflict, "brief_not_accepted", "the brief could not be accepted: "+err.Error())
		return "", false
	}
	return fmt.Sprintf("“%s” is ready to start.", action.Title), true
}

// applyDesignStack has the stack designer suggest the project's stack on
// the assistant's agent, or check the stack of the code an imported
// repository has (ADR-035).
func (api *API) applyDesignStack(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, *conversations.Helper, bool) {
	ctx := r.Context()
	if api.toolchains == nil || api.toolchainAssistant == nil {
		writeError(w, http.StatusServiceUnavailable, "toolchain_assistant_unavailable", "designing a stack needs real agents")
		return "", nil, false
	}
	manifest, err := api.projectToolchain(ctx, projectID)
	if err != nil {
		api.writeToolchainError(w, projectID, "design the stack for the assistant", err)
		return "", nil, false
	}
	if manifest.Status == toolchain.StatusConfigured {
		writeError(w, http.StatusConflict, "stack_exists", "the stack is set up already")
		return "", nil, false
	}
	request := startToolchainAssistantRequest{Message: action.Description, Purpose: toolchain.AssistantPurposeDesignStack}
	result := "Designing the stack."
	if overview, err := api.projects.GetRepositoryOverview(ctx, projectID); err == nil && overview.HasCode() {
		request.Purpose, result = toolchain.AssistantPurposeVerifyRepository, "Checking the stack the code uses."
	} else if request.Message == "" {
		writeError(w, http.StatusBadRequest, "stack_needs_description",
			"describe in description what the app needs, so the stack designer can suggest a stack")
		return "", nil, false
	}
	providers, model, err := api.assistantAgent(ctx)
	if err != nil {
		if !writeChoiceError(w, err) {
			writeConversationError(w, err)
		}
		return "", nil, false
	}
	request.Agent, request.Model = providers.LeadAgent, model
	var session toolchain.AssistantSession
	if err := api.callOwn(ctx, http.MethodPost, "/api/v1/projects/"+url.PathEscape(projectID)+"/toolchain/assistant-sessions",
		"assistant-"+action.ID, request, &session); err != nil {
		writeError(w, http.StatusConflict, "stack_not_started", "the stack designer could not be started: "+err.Error())
		return "", nil, false
	}
	return result, &conversations.Helper{ID: session.ID, Kind: conversations.HelperStack}, true
}

// applyStack applies the stack the stack designer suggested, as the Stack
// page's Apply does.
func (api *API) applyStack(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	helper := conversations.Helper{ID: action.Helper, Kind: conversations.HelperStack}
	var manifest toolchain.Manifest
	if err := api.callOwn(r.Context(), http.MethodPost, helperPath(projectID, helper)+"/apply", "assistant-"+action.ID,
		nil, &manifest); err != nil {
		writeError(w, http.StatusConflict, "stack_not_applied", "the stack could not be applied: "+err.Error())
		return "", false
	}
	return "The stack is set up: " + toolList(manifest.Tools) + ".", true
}

func toolList(tools map[string]string) string {
	listed := make([]string, 0, len(tools))
	for tool, version := range tools {
		listed = append(listed, tool+" "+version)
	}
	sort.Strings(listed)
	return strings.Join(listed, ", ")
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

// researcher starts research for the assistant.
type researcher interface {
	StartResearch(ctx context.Context, scope, actionID, title, question string) (conversations.Helper, error)
}

// applyResearch starts a research helper on the action's question, on the
// assistant's agent with the web and no repository (ADR-035).
func (api *API) applyResearch(
	w http.ResponseWriter, r *http.Request, scope string, action conversations.Action,
) (string, *conversations.Helper, bool) {
	starter, ok := api.conversations.(researcher)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "assistant_unavailable", "research needs real agents")
		return "", nil, false
	}
	helper, err := starter.StartResearch(r.Context(), scope, action.ID, action.Title, action.Description)
	if err != nil {
		writeError(w, http.StatusConflict, "research_not_started", "the research could not be started: "+err.Error())
		return "", nil, false
	}
	return fmt.Sprintf("Looking up “%s”.", action.Title), &helper, true
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
			state := conversations.HelperState{Turn: len(session.Messages), Agent: session.Agent}
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
		case conversations.HelperBrief:
			var current featureResponse
			if found, err := readOwn(ctx, handler, "/api/v1/projects/"+url.PathEscape(projectID)+"/features/"+
				url.PathEscape(helper.WorkOrderID), &current); err != nil || !found || current.State != feature.StateDraft {
				// A work order that left Draft no longer needs its brief.
				return conversations.HelperState{Status: conversations.HelperFinished}, err
			}
			var session assistantSessionResponse
			if found, err := readOwn(ctx, handler, helperPath(projectID, helper), &session); err != nil || !found {
				return conversations.HelperState{Status: conversations.HelperFinished}, err
			}
			state := conversations.HelperState{Turn: len(session.Messages), Agent: session.Agent}
			switch assistant.Status(session.Status) {
			case assistant.StatusWaitingForUser:
				state.Status, state.Text = conversations.HelperAsks, session.Message
			case assistant.StatusProposalReady:
				var brief struct {
					Document featureartifact.HandoffBrief `json:"document"`
				}
				if _, err := readOwn(ctx, handler, "/api/v1/projects/"+url.PathEscape(projectID)+"/features/"+
					url.PathEscape(helper.WorkOrderID)+"/artifacts/handoff_brief", &brief); err != nil {
					return conversations.HelperState{}, err
				}
				state.Status, state.Text = conversations.HelperProposes, briefText(brief.Document)
			case assistant.StatusFailed:
				state.Status = conversations.HelperFailed
			default:
				state.Status = conversations.HelperWorking
			}
			return state, nil
		case conversations.HelperStack:
			var session toolchain.AssistantSession
			if found, err := readOwn(ctx, handler, helperPath(projectID, helper), &session); err != nil || !found {
				return conversations.HelperState{Status: conversations.HelperFinished}, err
			}
			state := conversations.HelperState{Turn: len(session.Messages), Agent: session.Agent}
			switch session.Status {
			case toolchain.AssistantStatusWaitingForUser:
				state.Status, state.Text = conversations.HelperAsks, session.Message
			case toolchain.AssistantStatusProposalReady:
				state.Status, state.Text = conversations.HelperProposes, stackText(session)
			case toolchain.AssistantStatusApplied:
				state.Status = conversations.HelperFinished
			case toolchain.AssistantStatusFailed:
				state.Status, state.Text = conversations.HelperFailed, session.Message
			default:
				state.Status = conversations.HelperWorking
			}
			return state, nil
		}
		return conversations.HelperState{}, fmt.Errorf("unknown helper kind %q", helper.Kind)
	}
}

// stackText is a suggested stack in plain words, for its card.
func stackText(session toolchain.AssistantSession) string {
	text := "**Tools:** " + toolList(session.Proposal.Tools)
	if len(session.Proposal.Services) > 0 {
		text += "\n\n**Services:** " + strings.Join(session.Proposal.Services, ", ")
	}
	if message := strings.TrimSpace(session.Message); message != "" {
		text += "\n\n" + message
	}
	return text
}

// briefText is a brief in plain words, for its card.
func briefText(brief featureartifact.HandoffBrief) string {
	text := brief.Goal
	for _, part := range []struct {
		heading string
		items   []string
	}{{"Where it changes", brief.Areas}, {"To keep in mind", brief.Considerations}, {"Left to settle", brief.OpenQuestions}} {
		if len(part.items) > 0 {
			text += "\n\n**" + part.heading + "**\n- " + strings.Join(part.items, "\n- ")
		}
	}
	return text
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
		return "The stack is set up: " + toolList(manifest.Tools) + ".", "", true
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
