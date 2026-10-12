package featureartifact

import (
	"fmt"
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
	// Steps is "full", "planned", or "quick" (ADR-018).
	Steps            string `json:"steps"`
	IndependentTests bool   `json:"independent_tests"`
	// Agents and models, for create, start, and change; empty keeps the
	// project's defaults or the run's current choice.
	LeadAgent     string `json:"lead_agent"`
	LeadModel     string `json:"lead_model"`
	ReviewerAgent string `json:"reviewer_agent"`
	ReviewerModel string `json:"reviewer_model"`
	// start_work_order and change_models
	WorkOrderID string `json:"work_order_id"`
	// When is "now" or "next_turn", for change_models.
	When string `json:"when"`
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
			&action.WorkOrderID, &action.When,
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
	switch action.Steps {
	case "", "full", "planned", "quick":
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
