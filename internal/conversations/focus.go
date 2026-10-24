package conversations

import (
	"context"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

// Focus (ADR-035): the item the user opened the assistant from, such as a
// Draft's Clarify button or the Stack page's Ask an agent. The conversation
// stays one; the focus tells the assistant what the user is talking about,
// and a chip above the composer shows it until the user clears it.

type FocusKind string

const (
	FocusWorkOrder FocusKind = "work_order"
	FocusVision    FocusKind = "vision"
	FocusStack     FocusKind = "stack"
)

type Focus struct {
	Kind        FocusKind `json:"kind"`
	WorkOrderID string    `json:"work_order_id,omitempty"`
	// Label is what the chip says.
	Label string `json:"label"`
}

// SetFocus focuses a project's conversation on an item, or clears the focus
// when focus is nil.
func (service *Service) SetFocus(ctx context.Context, scope string, focus *Focus) (Conversation, error) {
	if scope == TopLevel {
		return Conversation{}, fmt.Errorf("%w: only a project's conversation has a focus", ErrInvalid)
	}
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	if focus != nil {
		switch focus.Kind {
		case FocusVision:
			*focus = Focus{Kind: FocusVision, Label: "The vision"}
		case FocusStack:
			*focus = Focus{Kind: FocusStack, Label: "The stack"}
		case FocusWorkOrder:
			item, err := service.workOrder(ctx, scope, focus.WorkOrderID)
			if err != nil {
				return Conversation{}, err
			}
			*focus = Focus{Kind: FocusWorkOrder, WorkOrderID: item.ID, Label: item.Title}
		default:
			return Conversation{}, fmt.Errorf("%w: focus on a work order, the vision, or the stack", ErrInvalid)
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return Conversation{}, err
	}
	stored.Focus, stored.UpdatedAt = focus, service.now()
	return stored.Conversation, service.write(stored)
}

func (service *Service) workOrder(ctx context.Context, scope, id string) (feature.Feature, error) {
	if service.config.Features == nil || strings.TrimSpace(id) == "" {
		return feature.Feature{}, ErrInvalid
	}
	features, err := service.config.Features.List(ctx, scope)
	if err != nil {
		return feature.Feature{}, err
	}
	for _, item := range features {
		if item.ID == id {
			return item, nil
		}
	}
	return feature.Feature{}, ErrNotFound
}

// focusInstructions tell the assistant what the user opened it on and how to
// carry it through.
func (service *Service) focusInstructions(ctx context.Context, scope string, focus *Focus) string {
	if focus == nil {
		return ""
	}
	switch focus.Kind {
	case FocusWorkOrder:
		item, err := service.workOrder(ctx, scope, focus.WorkOrderID)
		if err != nil {
			return ""
		}
		text := fmt.Sprintf("\n\nThe user opened you on the work order “%s” (%s, %s): that is what they are talking "+
			"about.", item.Title, item.ID, item.State)
		if description := strings.TrimSpace(item.Description); description != "" {
			text += " Its description, as quoted data:\n" + description + "\n"
		}
		if item.State == feature.StateDraft {
			text += " To clarify it, work out with the user what they want and why, asking only what changes the " +
				"work and what neither the state nor the code answers, a question or two at a time; then propose " +
				"write_brief with everything you settled. Its brief comes back as a card."
		}
		return text
	case FocusVision:
		if service.planning(ctx, scope) {
			return "\n\nThe user opened you on the plan being written in the planning workspace."
		}
		return "\n\nThe user opened you on the project's vision. Talk it through with them as far as it needs: what the " +
			"project is for, for whom, and what matters most. Then propose write_vision with everything they told " +
			"you; it writes the vision, or rewrites the one there is."
	case FocusStack:
		return "\n\nThe user opened you on the project's stack. Propose set_up_stack when the vision recommends a " +
			"stack Commitarium knows; otherwise design_stack, with what the app needs. For a repository that already " +
			"has code, design_stack checks the stack the code uses."
	}
	return ""
}
