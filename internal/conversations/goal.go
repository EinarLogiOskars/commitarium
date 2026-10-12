package conversations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// Autopilot (ASSISTANT_PLAN.md phase 3): the user hands the assistant a
// goal, the open items of one vision priority. While it is on, the
// assistant's actions for those items run without approval cards, except
// those using a premium model; the coordinator closes the goal when its
// last item is done.

var ErrGoalActive = errors.New("autopilot is already on")

type GoalStatus string

const (
	GoalActive  GoalStatus = "active"
	GoalDone    GoalStatus = "done"
	GoalStopped GoalStatus = "stopped"
)

// GoalItem is a vision item the goal finishes.
type GoalItem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Title       string `json:"title"`
	WorkOrderID string `json:"work_order_id,omitempty"`
	Done        bool   `json:"done"`
}

type Goal struct {
	Title     string     `json:"title"`
	Level     int        `json:"level"`
	Items     []GoalItem `json:"items"`
	Status    GoalStatus `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	// Continues counts the blocked work orders autopilot continued, by
	// work order; past maxAutoContinues the next one waits for the user.
	Continues map[string]int `json:"continues,omitempty"`
}

// maxAutoContinues bounds how often autopilot continues one work order, so
// a failure it cannot fix comes back to the user.
const maxAutoContinues = 2

func (goal *Goal) active() bool {
	return goal != nil && goal.Status == GoalActive
}

// StartGoal turns autopilot on for the open items of a vision priority.
func (service *Service) StartGoal(ctx context.Context, scope string, level int) (Conversation, error) {
	if scope == TopLevel {
		return Conversation{}, fmt.Errorf("%w: autopilot runs in a project", ErrInvalid)
	}
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	if service.config.Visions == nil || service.config.Features == nil {
		return Conversation{}, ErrUnavailable
	}
	current, err := service.config.Visions.Get(ctx, scope)
	if err != nil {
		return Conversation{}, fmt.Errorf("%w: the project has no vision yet", ErrInvalid)
	}
	features, err := service.config.Features.List(ctx, scope)
	if err != nil {
		return Conversation{}, err
	}
	completed := map[string]bool{}
	for _, item := range features {
		completed[item.ID] = item.State == feature.StateCompleted
	}
	items := make([]GoalItem, 0)
	for _, item := range priorityItems(current.Plan, level) {
		if !item.Done && !completed[item.WorkOrderID] {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return Conversation{}, fmt.Errorf("%w: P%d has nothing left to do", ErrInvalid, level)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return Conversation{}, err
	}
	if stored.Goal.active() {
		return Conversation{}, ErrGoalActive
	}
	now := service.now()
	stored.Goal = &Goal{Title: fmt.Sprintf("Finish P%d", level), Level: level, Items: items, Status: GoalActive, StartedAt: now}
	text := fmt.Sprintf("Autopilot is on: finish P%d (%d %s).", level, len(items), plural(len(items), "item", "items"))
	stored.addEvent(text, now)
	stored.PendingEvents = append(stored.PendingEvents, text+" Start driving its items to merged.")
	stored.UpdatedAt = now
	if err := service.write(stored); err != nil {
		return Conversation{}, err
	}
	service.nudgeWatcher()
	return stored.Conversation, nil
}

// ActiveGoals lists the autopilot goals that are on, by project.
func (service *Service) ActiveGoals(_ context.Context) (map[string]Goal, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	goals := map[string]Goal{}
	for _, scope := range service.projectScopes() {
		stored, err := service.read(scope)
		if err != nil {
			return nil, err
		}
		if stored.Goal.active() {
			goals[scope] = *stored.Goal
		}
	}
	return goals, nil
}

// StopGoal turns autopilot off; the work it started carries on.
func (service *Service) StopGoal(ctx context.Context, scope string) (Conversation, error) {
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return Conversation{}, err
	}
	if !stored.Goal.active() {
		return stored.Conversation, nil
	}
	now := service.now()
	stored.Goal.Status, stored.Goal.EndedAt = GoalStopped, &now
	stored.addEvent("Autopilot stopped. Work orders it started carry on; actions wait for your approval again.", now)
	stored.UpdatedAt = now
	return stored.Conversation, service.write(stored)
}

// progress updates the goal's items from the vision and the work, and
// reports whether the goal is finished. An item gone from the vision counts
// as done.
func (goal *Goal) progress(plan featureartifact.VisionPlan, work map[string]work) bool {
	byID := map[string]GoalItem{}
	for _, item := range priorityItems(plan, -1) {
		byID[item.ID] = item
	}
	finished := true
	for index := range goal.Items {
		item := &goal.Items[index]
		current, found := byID[item.ID]
		if !found {
			item.Done = true
			continue
		}
		item.WorkOrderID = current.WorkOrderID
		item.Done = current.Done || work[current.WorkOrderID].State == feature.StateCompleted
		finished = finished && item.Done
	}
	return finished
}

// priorityItems lists the leaf items of a priority (every priority for
// level -1), labelled like P0.2.1, with their linked work order and whether
// the vision marks them done.
func priorityItems(plan featureartifact.VisionPlan, level int) []GoalItem {
	items := make([]GoalItem, 0)
	var walk func([]featureartifact.VisionItem, string)
	walk = func(children []featureartifact.VisionItem, prefix string) {
		for index, item := range children {
			label := fmt.Sprintf("%s.%d", prefix, index+1)
			if len(item.Items) > 0 {
				walk(item.Items, label)
				continue
			}
			items = append(items, GoalItem{
				ID: item.ID, Label: label, Title: item.Title, WorkOrderID: item.FeatureID, Done: item.Done,
			})
		}
	}
	for _, priority := range plan.Priorities {
		if level < 0 || priority.Level == level {
			walk(priority.Items, fmt.Sprintf("P%d", priority.Level))
		}
	}
	return items
}

// autopilotActions marks the proposed actions autopilot takes over and
// returns those it may apply: actions for the goal's items without a
// premium model. One with a premium model is left for the user, with a note.
func (service *Service) autopilotActions(scope string) ([]Action, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil || !stored.Goal.active() {
		return nil, err
	}
	items, orders := map[string]bool{}, map[string]bool{}
	for _, item := range stored.Goal.Items {
		items[item.ID] = true
		if item.WorkOrderID != "" {
			orders[item.WorkOrderID] = true
		}
	}
	// One work order per item: an item that has one, or whose create autopilot
	// already applied, takes no other.
	claimed := map[string]bool{}
	for _, item := range stored.Goal.Items {
		claimed[item.ID] = item.WorkOrderID != ""
	}
	// Work orders autopilot created: those not delivering an item (a
	// duplicate) are its own to close.
	created := map[string]bool{}
	for _, action := range stored.Actions {
		if action.Type == featureartifact.AssistantCreateWorkOrder && action.Status == ActionApplied {
			claimed[action.VisionItemID] = true
			if action.AutoTried && action.WorkOrderID != "" {
				created[action.WorkOrderID] = true
			}
		}
	}
	now := service.now()
	apply, noted := make([]Action, 0), false
	for index := range stored.Actions {
		action := &stored.Actions[index]
		if action.Status != ActionProposed || action.AutoTried {
			continue
		}
		inGoal := false
		switch action.Type {
		case featureartifact.AssistantCreateWorkOrder:
			inGoal = items[action.VisionItemID]
		case featureartifact.AssistantStartWorkOrder, featureartifact.AssistantChangeModels,
			featureartifact.AssistantContinueWorkOrder:
			inGoal = orders[action.WorkOrderID]
		case featureartifact.AssistantCloseWorkOrder:
			inGoal = created[action.WorkOrderID] && !orders[action.WorkOrderID]
		}
		if !inGoal {
			continue
		}
		action.AutoTried = true
		if action.Type == featureartifact.AssistantContinueWorkOrder {
			if stored.Goal.Continues[action.WorkOrderID] >= maxAutoContinues {
				stored.addEvent(fmt.Sprintf("Autopilot has continued %s %d times; the next try is yours to approve.",
					action.WorkOrderID, maxAutoContinues), now)
				noted = true
				continue
			}
			if stored.Goal.Continues == nil {
				stored.Goal.Continues = map[string]int{}
			}
			stored.Goal.Continues[action.WorkOrderID]++
		}
		if action.Type == featureartifact.AssistantCreateWorkOrder && claimed[action.VisionItemID] {
			stored.addEvent(fmt.Sprintf("Autopilot left %s for you: its vision item already has a work order.",
				actionName(action.AssistantAction)), now)
			noted = true
			continue
		}
		if len(action.Premium) > 0 {
			stored.addEvent(fmt.Sprintf("Autopilot left %s for you: it uses %s, which needs your approval.",
				actionName(action.AssistantAction), strings.Join(action.Premium, " and ")), now)
			noted = true
			continue
		}
		if action.Type == featureartifact.AssistantCreateWorkOrder {
			claimed[action.VisionItemID] = true
		}
		apply = append(apply, *action)
	}
	if len(apply) == 0 && !noted {
		return nil, nil
	}
	stored.UpdatedAt = now
	return apply, service.write(stored)
}

// note adds an event line to a scope's conversation.
func (service *Service) note(scope, text string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return err
	}
	now := service.now()
	stored.addEvent(text, now)
	stored.UpdatedAt = now
	return service.write(stored)
}

func (stored *record) addEvent(text string, at time.Time) {
	stored.Messages = append(stored.Messages, Message{
		ID: messageID(*stored, len(stored.Messages)), Role: "event", Text: text, OccurredAt: at,
	})
}

func actionName(action featureartifact.AssistantAction) string {
	if action.Title != "" {
		return fmt.Sprintf("%q", action.Title)
	}
	return "the " + strings.ReplaceAll(string(action.Type), "_", " ") + " for " + action.WorkOrderID
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

// goalInstructions tell the assistant about an active goal.
func goalInstructions(goal *Goal) string {
	if !goal.active() {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("\n\nAutopilot is on. The user's goal: " + goal.Title + ". Its items:\n")
	for _, item := range goal.Items {
		state := "open"
		switch {
		case item.Done:
			state = "done"
		case item.WorkOrderID != "":
			state = "work order " + item.WorkOrderID
		}
		builder.WriteString(fmt.Sprintf("- %s %s (item %s): %s\n", item.Label, item.Title, item.ID, state))
	}
	builder.WriteString("Drive these items to merged. Propose create_work_order with the item's vision_item_id for " +
		"an item without a work order and without a create card already waiting (autopilot applies a waiting one; " +
		"never propose a second for the same item), start_work_order for one that is drafted, and change_models when a run " +
		"needs a different model. Commitarium applies your actions for these items at once, without the user's " +
		"approval, except those using a premium model. Start an item only once the items it builds on have " +
		"merged; at most two of the goal's work orders run at a time. When one is blocked or failed, find out " +
		"why (its reason, its pull request, the code) and propose continue_work_order with guidance that fixes " +
		"it; Commitarium continues a work order on its own at most twice. When a work order needs the user (a " +
		"question about the product, a merge approval, a blocker you cannot fix), tell the user exactly what it " +
		"needs. When autopilot made a duplicate work order for an item, propose close_work_order for the one that " +
		"does not deliver it; autopilot closes its own duplicates. Propose nothing outside the goal unless the " +
		"user asks; such actions still wait for approval.")
	return builder.String()
}
