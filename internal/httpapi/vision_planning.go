package httpapi

import (
	"context"
	"log"
	"net/http"
	"net/url"

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
	StartPlanning(ctx context.Context, projectID, goal string) (vision.Vision, bool, error)
	UpdatePlanning(ctx context.Context, projectID string, update vision.PlanningUpdate) (vision.Vision, error)
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
}

// startPlanningHandler puts the project's vision in planning and wakes its
// assistant to plan it with the user.
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
	stored, started, err := planner.StartPlanning(r.Context(), projectID, body.Goal)
	if err != nil {
		writeVisionError(w, projectID, err)
		return
	}
	if waker, ok := api.conversations.(assistantWaker); ok && started {
		if err := waker.Wake(r.Context(), projectID, "Planning the project with the assistant.",
			"The user opened the planning workspace to plan this project with you before anything is built. "+
				"Open the conversation: sum up in a few lines what you know (your notes may hold what was settled "+
				"before), fill the plan in with update_plan, and ask the first round of what matters most."); err != nil {
			log.Printf("wake the assistant of project %q to plan it: %v", projectID, err)
		}
	}
	writeJSON(w, http.StatusOK, stored, "vision")
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
		Goal: action.Description, Decisions: action.Decisions, Questions: action.OpenQuestions, Items: action.PlanItems,
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
