package featureartifact

import (
	"fmt"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"strings"
)

// AssistantTurn is what the assistant returns besides its reply: the actions
// it proposes, which run only once the user approves each one, and the notes
// it keeps for itself.
type AssistantTurn struct {
	Actions []AssistantAction `json:"actions"`
	// Notes replaces the assistant's notes for its scope when non-empty:
	// decisions, preferences, and a catalog of reusable work.
	Notes string `json:"notes"`
}

type AssistantActionType string

const (
	// AssistantCreateWorkOrder drafts a work order, optionally for a vision
	// item, with its steps, agents, models, and whether to write tests.
	AssistantCreateWorkOrder AssistantActionType = "create_work_order"
	// AssistantStartWorkOrder starts a draft or ready work order.
	AssistantStartWorkOrder AssistantActionType = "start_work_order"
	// AssistantChangeModels switches a working run's models.
	AssistantChangeModels AssistantActionType = "change_models"
	// AssistantAddVisionItem adds an item to the vision at a priority,
	// optionally linking a work order done outside the plan.
	AssistantAddVisionItem AssistantActionType = "add_vision_item"
	// AssistantStartAutopilot hands the assistant a goal: the open items of
	// one vision priority, which it then drives to merged on its own.
	AssistantStartAutopilot AssistantActionType = "start_autopilot"
	// AssistantContinueWorkOrder continues a work order whose run waits on a
	// blocker, with optional guidance for its agents.
	AssistantContinueWorkOrder AssistantActionType = "continue_work_order"
	// AssistantRecoverWorkOrder recovers a work order whose run waits on a
	// blocker, in the way How names (ADR-032).
	AssistantRecoverWorkOrder AssistantActionType = "recover_work_order"
	// AssistantCloseWorkOrder ends a work order without merging it, keeping
	// its history: a duplicate, or work the user no longer wants.
	AssistantCloseWorkOrder AssistantActionType = "close_work_order"
	// AssistantCreateApp creates a new app at the top level: a project used
	// as an app, named by Title and built from Description (APPS_PLAN.md).
	AssistantCreateApp AssistantActionType = "create_app"
	// AssistantSetUpPreview sets the app's preview up, or fixes one that
	// fails to start, in a work order that merges only once Commitarium's
	// preview check of its commit passes (ADR-028). Guidance says what is
	// failing, for its agent.
	AssistantSetUpPreview AssistantActionType = "set_up_preview"
	// AssistantWriteVision has Commitarium write the project's vision on the
	// assistant's agent from Description: what the project is for, in the
	// user's words (ADR-020), where the assistant builds everything.
	AssistantWriteVision AssistantActionType = "write_vision"
	// AssistantSetUpStack sets the project up on the stack its vision
	// recommends, or detects the stack of the code it has.
	AssistantSetUpStack AssistantActionType = "set_up_stack"
	// AssistantAnswerHelper answers a helper the assistant started (ADR-035):
	// Guidance is the answer to the question Helper asked.
	AssistantAnswerHelper AssistantActionType = "answer_helper"
)

// AssistantAction is one proposed action. Fields that do not apply to its
// type are empty.
type AssistantAction struct {
	Type   AssistantActionType `json:"type"`
	Reason string              `json:"reason"`
	// create_work_order
	Title        string `json:"title"`
	Description  string `json:"description"`
	VisionItemID string `json:"vision_item_id"`
	// Steps is "full", "reviewed_plan", "planned", or "quick" (ADR-018,
	// ADR-031).
	Steps string `json:"steps"`
	// IndependentTests is ignored: independent acceptance tests are no
	// longer written (ADR-031). Kept so older stored actions still decode.
	IndependentTests bool `json:"independent_tests,omitempty"`
	// Agents and models, for create, start, and change: the assistant's
	// choice (ADR-027). Empty keeps the work order's own choice or the run's
	// current one; a project has no defaults.
	LeadAgent     string `json:"lead_agent"`
	LeadModel     string `json:"lead_model"`
	ReviewerAgent string `json:"reviewer_agent"`
	ReviewerModel string `json:"reviewer_model"`
	// Planning models and efforts by phase, for create and start (ADR-031).
	// Empty keeps the role's model and the model's default effort.
	LeadPlanModel      string `json:"lead_plan_model"`
	LeadPlanEffort     string `json:"lead_plan_effort"`
	LeadEffort         string `json:"lead_effort"`
	ReviewerPlanModel  string `json:"reviewer_plan_model"`
	ReviewerPlanEffort string `json:"reviewer_plan_effort"`
	ReviewerEffort     string `json:"reviewer_effort"`
	// start_work_order, change_models, and add_vision_item (optional)
	WorkOrderID string `json:"work_order_id"`
	// Priority is the level of a new vision item, or of the priority an
	// autopilot goal finishes: 0 for P0, 1 for P1, and so on.
	Priority int `json:"priority"`
	// When is "now" or "next_turn", for change_models.
	When string `json:"when"`
	// How, for recover_work_order: "recheck", "continue", "message",
	// "fresh_conversation", or "back_to_ready" (ADR-032).
	How string `json:"how"`
	// Guidance is what continue_work_order tells the agents; empty retries.
	// For set_up_preview it is what fails, if the assistant knows.
	Guidance string `json:"guidance"`
	// After, for a work order to create or start, names work it must follow:
	// a work order's ID, or the title of another of the assistant's actions.
	// Autopilot starts it only once that work has merged, so two changes to
	// the same part of the app do not run at once.
	After string `json:"after"`
	// Helper, for answer_helper, names the helper that asked.
	Helper string `json:"helper"`
}

// PhaseChoices are the action's planning models and efforts (ADR-031); nil
// when it names none.
func (action AssistantAction) PhaseChoices() *project.PhaseChoices {
	choices := project.PhaseChoices{
		LeadPlanModel: action.LeadPlanModel, LeadPlanEffort: action.LeadPlanEffort, LeadEffort: action.LeadEffort,
		ReviewerPlanModel: action.ReviewerPlanModel, ReviewerPlanEffort: action.ReviewerPlanEffort,
		ReviewerEffort: action.ReviewerEffort,
	}
	if choices == (project.PhaseChoices{}) {
		return nil
	}
	return &choices
}

const (
	maxAssistantActions   = 8
	MaxAssistantNoteBytes = 16 * 1024
)

// Normalize trims the turn and checks its actions.
func (turn AssistantTurn) Normalize() (AssistantTurn, error) {
	turn.Notes = strings.TrimSpace(turn.Notes)
	if len(turn.Notes) > MaxAssistantNoteBytes {
		return AssistantTurn{}, fmt.Errorf("%w: the assistant's notes are too long", ErrInvalidArtifact)
	}
	if len(turn.Actions) > maxAssistantActions {
		return AssistantTurn{}, fmt.Errorf("%w: at most %d actions per turn", ErrInvalidArtifact, maxAssistantActions)
	}
	for index := range turn.Actions {
		action := &turn.Actions[index]
		for _, field := range []*string{
			&action.Reason, &action.Title, &action.Description, &action.VisionItemID, &action.Steps,
			&action.LeadAgent, &action.LeadModel, &action.ReviewerAgent, &action.ReviewerModel,
			&action.LeadPlanModel, &action.LeadPlanEffort, &action.LeadEffort,
			&action.ReviewerPlanModel, &action.ReviewerPlanEffort, &action.ReviewerEffort,
			&action.WorkOrderID, &action.When, &action.How, &action.Guidance, &action.After, &action.Helper,
		} {
			*field = strings.TrimSpace(*field)
		}
		if err := action.validate(); err != nil {
			return AssistantTurn{}, fmt.Errorf("%w: action %d: %v", ErrInvalidArtifact, index+1, err)
		}
	}
	return turn, nil
}

func (action AssistantAction) validate() error {
	if action.Reason == "" || len(action.Reason) > MaxStepTextBytes {
		return fmt.Errorf("a reason is required")
	}
	if len(action.After) > MaxStepTextBytes {
		return fmt.Errorf("after is too long")
	}
	switch action.Steps {
	case "", "full", "reviewed_plan", "planned", "quick":
	default:
		return fmt.Errorf("steps must be full, planned, or quick")
	}
	switch action.Type {
	case AssistantCreateWorkOrder:
		if action.Title == "" || len(action.Title) > MaxStepTextBytes || len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("a work order needs a title and a bounded description")
		}
	case AssistantStartWorkOrder:
		if action.WorkOrderID == "" {
			return fmt.Errorf("start needs a work order")
		}
	case AssistantAddVisionItem:
		if action.Title == "" || len(action.Title) > MaxStepTextBytes || len(action.Description) > MaxStepTextBytes {
			return fmt.Errorf("a vision item needs a title and a short description")
		}
		if action.Priority < 0 || action.Priority > maxVisionPriority {
			return fmt.Errorf("priority runs from 0 to %d", maxVisionPriority)
		}
	case AssistantCreateApp:
		if action.Title == "" || len(action.Title) > 80 || action.Description == "" || len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("an app needs a short name and a description")
		}
	case AssistantCloseWorkOrder:
		if action.WorkOrderID == "" {
			return fmt.Errorf("closing needs a work order")
		}
	case AssistantContinueWorkOrder:
		if action.WorkOrderID == "" || len(action.Guidance) > MaxGoalBytes {
			return fmt.Errorf("continuing needs a work order and bounded guidance")
		}
	case AssistantRecoverWorkOrder:
		if action.WorkOrderID == "" || len(action.Guidance) > MaxGoalBytes {
			return fmt.Errorf("recovering needs a work order and bounded guidance")
		}
		switch action.How {
		case "recheck", "continue", "fresh_conversation", "back_to_ready":
		case "message":
			if action.Guidance == "" {
				return fmt.Errorf("a message to the agents needs its text in guidance")
			}
		default:
			return fmt.Errorf("how must be recheck, continue, message, fresh_conversation, or back_to_ready")
		}
	case AssistantSetUpPreview:
		if len(action.Guidance) > MaxGoalBytes {
			return fmt.Errorf("the preview's guidance is too long")
		}
	case AssistantWriteVision:
		if action.Description == "" || len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("writing the vision needs a bounded description of the project")
		}
	case AssistantSetUpStack:
	case AssistantAnswerHelper:
		if action.Helper == "" || action.Guidance == "" || len(action.Guidance) > MaxGoalBytes {
			return fmt.Errorf("answering a helper needs the helper and a bounded answer in guidance")
		}
	case AssistantStartAutopilot:
		if action.Priority < 0 || action.Priority > maxVisionPriority {
			return fmt.Errorf("priority runs from 0 to %d", maxVisionPriority)
		}
	case AssistantChangeModels:
		if action.WorkOrderID == "" || (action.LeadModel == "" && action.ReviewerModel == "") {
			return fmt.Errorf("changing models needs a work order and a model")
		}
		if action.When != "now" && action.When != "next_turn" {
			return fmt.Errorf("when must be now or next_turn")
		}
	default:
		return fmt.Errorf("unknown action type %q", action.Type)
	}
	return nil
}
