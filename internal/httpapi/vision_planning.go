package httpapi

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// Planning mode (ONE_CONVERSATION_PLAN.md phase 2): in Developer mode a
// project's vision is planned with the assistant before anything is built.
// The assistant updates the plan as they talk; Build it finishes it and
// hands it to autopilot or to the user's own project flow.

// visionPlanner plans a project's vision with the assistant.
type visionPlanner interface {
	StartPlanning(ctx context.Context, projectID, goal, chat string) (vision.Vision, bool, error)
	UpdatePlanning(ctx context.Context, projectID string, update vision.PlanningUpdate) (vision.Vision, error)
	PausePlanning(ctx context.Context, projectID string) (vision.Vision, error)
	DiscardPlanning(ctx context.Context, projectID string) (vision.Vision, bool, error)
	FinishPlanning(ctx context.Context, projectID string) (vision.Vision, error)
}

func (api *API) planner(w http.ResponseWriter) (visionPlanner, bool) {
	planner, ok := api.visions.(visionPlanner)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "vision_unavailable", "planning is unavailable")
	}
	return planner, ok
}

type startPlanningRequest struct {
	// Goal is what the project is for; it starts the vision when there is
	// none yet.
	Goal string `json:"goal"`
	// Transcript is the chat the project grew from, which its assistant
	// hears as planning starts.
	Transcript string `json:"transcript,omitempty"`
	// Chat is the assistant chat to plan in (ADR-039); empty plans in the
	// project's most recently used chat. Planning open elsewhere moves to
	// it.
	Chat string `json:"chat,omitempty"`
	// Idea is what the user wants to plan, when planning starts with one:
	// the start_planning card's goal, or a new project's. Without one (and
	// without WithMessage), the assistant opens by asking what to plan.
	Idea string `json:"idea,omitempty"`
	// WithMessage says the user's message with what to plan follows at once
	// (/plan and a message): the planning note waits for it, and its turn
	// hears both.
	WithMessage bool `json:"with_message,omitempty"`
}

// planningChats names the chat planning goes to, and wakes the assistant
// there.
type planningChats interface {
	ChatOrRecent(ctx context.Context, scope, chat string) (string, error)
	WakeChat(ctx context.Context, scope, chat, note, text string) error
}

// chatTeller notes something in a chat for the assistant's next turn there
// without starting one.
type chatTeller interface {
	TellChat(ctx context.Context, scope, chat, note, text string) error
}

// startPlanningHandler puts the project's vision in planning in a chat, or
// resumes paused planning, or moves it to that chat, and wakes its
// assistant there to plan it with the user.
func (api *API) startPlanningHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	var body startPlanningRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	planner, ok := api.planner(w)
	if !ok {
		return
	}
	chats, inChats := api.conversations.(planningChats)
	chat := body.Chat
	if inChats {
		var err error
		if chat, err = chats.ChatOrRecent(r.Context(), projectID, body.Chat); err != nil {
			writeConversationError(w, err)
			return
		}
	}
	stored, started, err := planner.StartPlanning(r.Context(), projectID, body.Goal, chat)
	if err != nil {
		writeVisionError(w, projectID, err)
		return
	}
	if !started {
		writeJSON(w, http.StatusOK, stored, "vision")
		return
	}
	round := stored.Planning != nil && stored.Planning.Round
	note, text := planningWake(round, body.Idea, body.WithMessage)
	text += grewFrom(body.Transcript)
	teller, tells := api.conversations.(chatTeller)
	switch {
	case body.WithMessage && tells && inChats && chat != "":
		// The user's message follows at once, and its turn hears the note.
		err = teller.TellChat(r.Context(), projectID, chat, note, text)
	case inChats && chat != "":
		err = chats.WakeChat(r.Context(), projectID, chat, note, text)
	default:
		if waker, ok := api.conversations.(assistantWaker); ok {
			err = waker.Wake(r.Context(), projectID, note, text)
		}
	}
	if err != nil {
		log.Printf("wake the assistant of project %q to plan it: %v", projectID, err)
	}
	writeJSON(w, http.StatusOK, stored, "vision")
}

// planningWake is the note in the chat and what the assistant hears as
// planning starts in it: with an idea (or the user's message with one), it
// goes straight into it; without, it opens by asking what to plan, as the
// planning instructions' opening says, unless the chat already asks for
// something.
func planningWake(round bool, idea string, withMessage bool) (note, text string) {
	note, text = "Planning the project with Mayka.",
		"The user switched plan mode on to plan this project with you before anything is built."
	if round {
		note, text = "Planning the project's next round with Mayka.",
			"The user switched plan mode on to plan the next round of this project with you: what is built stays, "+
				"and nothing new is built until they press Build it."
	}
	resumed := " If the plan already has decisions, planning was stopped and is resumed: pick up where you left off instead."
	idea = strings.Join(strings.Fields(idea), " ")
	switch {
	case withMessage:
		text += " They came with something to plan: their message with this note says what."
	case idea != "":
		text += fmt.Sprintf(" They came with something to plan: “%s”.", boundedText(idea, 500))
	default:
		return note, text + " They did not say what to plan. If their recent messages in this chat clearly ask " +
			"for something, plan that as if they had said it. Otherwise open as your planning instructions' " +
			"opening says: one short line and its one question, with no plan update yet." + resumed
	}
	if round {
		return note, text + " Say in a line what is built so far and what you took from their idea, start the " +
			"round's plan, and ask what you need first." + resumed
	}
	return note, text + " Say in a line or two what you understood (your notes may hold what was settled " +
		"before), start the plan with the goal, and ask what you need first." + resumed
}

// updatePlanningHandler applies a change to the vision being planned, as
// the assistant's update_plan does.
func (api *API) updatePlanningHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	var body vision.PlanningUpdate
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	planner, ok := api.planner(w)
	if !ok {
		return
	}
	stored, err := planner.UpdatePlanning(r.Context(), projectID, body)
	if err != nil {
		writeVisionError(w, projectID, err)
		return
	}
	writeJSON(w, http.StatusOK, stored, "vision")
}

// pausePlanningHandler stops planning the vision: the plan is kept as it
// is, nothing is built, and starting planning again resumes it.
func (api *API) pausePlanningHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	planner, ok := api.planner(w)
	if !ok {
		return
	}
	stored, err := planner.PausePlanning(r.Context(), projectID)
	if err != nil {
		writeVisionError(w, projectID, err)
		return
	}
	writeJSON(w, http.StatusOK, stored, "vision")
}

// discardPlanningResponse is the vision as it was before the discarded
// session, or removed, with no vision, when the session had created it.
type discardPlanningResponse struct {
	Vision  *vision.Vision `json:"vision"`
	Removed bool           `json:"removed"`
}

// discardPlanningHandler switches plan mode off without keeping the plan:
// the session's draft, notes, and unbuilt plan changes are dropped and the
// vision is put back as it was before it, built work kept. The planning
// chat's assistant hears it, so it does not carry the dropped plan on.
func (api *API) discardPlanningHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	planner, ok := api.planner(w)
	if !ok {
		return
	}
	chat := ""
	if api.visions != nil {
		if current, err := api.visions.Get(r.Context(), projectID); err == nil && current.Planning != nil {
			chat = current.Planning.Chat
		}
	}
	stored, removed, err := planner.DiscardPlanning(r.Context(), projectID)
	if err != nil {
		writeVisionError(w, projectID, err)
		return
	}
	if teller, ok := api.conversations.(chatTeller); ok && chat != "" {
		if err := teller.TellChat(r.Context(), projectID, chat, "Discarded the plan.",
			"The user switched plan mode off and discarded this planning session: its draft, decisions, open "+
				"questions, research, and the plan changes it made are gone, and the vision is as it was before "+
				"the session. Do not carry that plan on unless they ask for it again."); err != nil {
			log.Printf("tell the planning chat of project %q that its plan was discarded: %v", projectID, err)
		}
	}
	response := discardPlanningResponse{Removed: removed}
	if !removed {
		response.Vision = &stored
	}
	writeJSON(w, http.StatusOK, response, "discarded plan")
}

type finishPlanningRequest struct {
	// Autopilot hands the finished vision to the assistant, which then
	// builds everything; otherwise the user takes it from there.
	Autopilot bool `json:"autopilot"`
}

// finishPlanningHandler is Build it: the vision is the plan as it stands,
// and with autopilot the project is launched as a new app is: its stack is
// set up and the whole vision built.
func (api *API) finishPlanningHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	var body finishPlanningRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	planner, ok := api.planner(w)
	if !ok {
		return
	}
	stored, err := planner.FinishPlanning(r.Context(), projectID)
	if err != nil {
		writeVisionError(w, projectID, err)
		return
	}
	if body.Autopilot {
		// Its launch moves the project along, so handing it over wakes no one.
		ctx := context.WithValue(r.Context(), ownSetup{}, true)
		for _, step := range []struct {
			method, path string
			body         any
		}{
			{http.MethodPut, "/api/v1/projects/" + url.PathEscape(projectID) + "/assistant-mode",
				map[string]project.AssistantMode{"assistant_mode": project.AssistantModeBuilds}},
			{http.MethodPost, "/api/v1/assistant/conversations/" + url.PathEscape(projectID) + "/launch", nil},
		} {
			if err := api.callOwn(ctx, step.method, step.path, "", step.body, nil); err != nil {
				writeError(w, http.StatusBadGateway, "autopilot_not_started",
					"the plan is finished, but autopilot could not start: "+err.Error())
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, stored, "vision")
}

// applyUpdatePlan applies the assistant's update to the vision being
// planned through its route.
func (api *API) applyUpdatePlan(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	update := vision.PlanningUpdate{
		Goal: action.Description, Step: action.PlanStep, Covered: action.PlanCovered,
		Decisions: action.Decisions, Questions: action.OpenQuestions,
		Items: action.PlanItems, Document: action.PlanDocument,
	}
	if update.Decisions == nil {
		update.Decisions = []string{}
	}
	if update.Questions == nil {
		update.Questions = []string{}
	}
	if len(action.StackPresets) > 0 {
		update.Stack = &featureartifact.VisionStack{Presets: action.StackPresets, Frameworks: action.Frameworks}
	}
	if err := api.callOwn(r.Context(), http.MethodPut, "/api/v1/projects/"+url.PathEscape(projectID)+"/vision/planning",
		"", update, nil); err != nil {
		writeError(w, http.StatusConflict, "plan_not_updated", "the plan could not be updated: "+err.Error())
		return "", false
	}
	if action.PlanDocument != "" {
		return "Wrote the plan.", true
	}
	return "Updated the plan.", true
}

// applyBuildPlan is Build it, from the assistant's card: the vision is
// finished and autopilot builds it.
func (api *API) applyBuildPlan(w http.ResponseWriter, r *http.Request, projectID string) (string, bool) {
	if err := api.callOwn(r.Context(), http.MethodPost, "/api/v1/projects/"+url.PathEscape(projectID)+"/vision/planning/finish",
		"", map[string]bool{"autopilot": true}, nil); err != nil {
		writeError(w, http.StatusConflict, "plan_not_built", "the plan could not be built: "+err.Error())
		return "", false
	}
	return "Finished the plan. Autopilot sets the project up and builds it.", true
}

// applyStartPlanning turns plan mode on in the chat that proposed it, as the
// chat's plan switch does: planning starts, resumes, or moves there, and the
// assistant there is woken to plan with the user. Planning already open in
// that chat leaves it as it is.
func (api *API) applyStartPlanning(
	w http.ResponseWriter, r *http.Request, projectID string, action conversations.Action,
) (string, bool) {
	if api.visions != nil {
		current, err := api.visions.Get(r.Context(), projectID)
		if err == nil && current.Planning.Open() && (current.Planning.Chat == "" || current.Planning.Chat == action.Chat) {
			return "Plan mode was already on in this chat.", true
		}
	}
	goal := action.Description
	if goal == "" {
		if owner, err := api.projects.GetByID(r.Context(), projectID); err == nil {
			goal = owner.Name
		}
	}
	if err := api.callOwn(r.Context(), http.MethodPost, "/api/v1/projects/"+url.PathEscape(projectID)+"/vision/planning",
		"", startPlanningRequest{Goal: goal, Chat: action.Chat, Idea: action.Description}, nil); err != nil {
		writeError(w, http.StatusConflict, "planning_not_started", "plan mode could not start: "+err.Error())
		return "", false
	}
	return "Started plan mode in this chat.", true
}
