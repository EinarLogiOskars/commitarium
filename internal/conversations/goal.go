package conversations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// Autopilot (ASSISTANT_PLAN.md phase 3): the user hands the assistant a
// goal, the open items of one vision priority. While it is on, the
// assistant's actions for those items run without approval cards, except
// those using a premium model; the coordinator closes the goal when its
// last item is done. A project whose assistant builds everything is always
// on autopilot, goal or not (ADR-027).
//
// Either way the assistant acts on its own only on its own work: the work
// orders it created, and those the user handed it with a goal. One the user
// created by hand is the user's; an action on it waits for the user's
// approval as a card.

var ErrGoalActive = errors.New("autopilot is already on")

// AllPriorities is the level of a goal that finishes the whole vision.
const AllPriorities = -1

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
	Title  string     `json:"title"`
	Level  int        `json:"level"`
	Items  []GoalItem `json:"items"`
	Status GoalStatus `json:"status"`
	// Handed are the work orders the goal's items had when it started: the
	// user handed them to the assistant with the goal, whoever created them.
	Handed    []string   `json:"handed,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// maxAutoContinues bounds how often autopilot recovers one work order across
// its blockers, so a failure it cannot fix comes back to the user; the
// coordinator also bounds recoveries of each failure (ADR-032).
const maxAutoContinues = 8

func (goal *Goal) active() bool {
	return goal != nil && goal.Status == GoalActive
}

// Hands reports whether the goal, while it is on, hands the assistant a
// work order the user created.
func (goal *Goal) Hands(workOrderID string) bool {
	return goal.active() && workOrderID != "" && slices.Contains(goal.Handed, workOrderID)
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
	name := fmt.Sprintf("P%d", level)
	if level == AllPriorities {
		name = "the vision"
	}
	if len(items) == 0 {
		return Conversation{}, fmt.Errorf("%w: %s has nothing left to do", ErrInvalid, name)
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
	handed := make([]string, 0)
	for _, item := range items {
		if item.WorkOrderID != "" {
			handed = append(handed, item.WorkOrderID)
		}
	}
	stored.Goal = &Goal{Title: "Finish " + name, Level: level, Items: items, Status: GoalActive, Handed: handed, StartedAt: now}
	text := fmt.Sprintf("Autopilot is on: finish %s (%d %s).", name, len(items), plural(len(items), "item", "items"))
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
// returns those it may apply: actions for the goal's items, or any of the
// assistant's actions where it builds everything, without a premium model
// and only on the assistant's own work. One with a premium model, or on a
// work order the user created, is left for the user, with a note.
func (service *Service) autopilotActions(ctx context.Context, scope string) ([]Action, error) {
	builds := service.assistantBuilds(ctx, scope)
	ours, titles, states := service.workOrders(ctx, scope)
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil || (!stored.Goal.active() && !builds) {
		return nil, err
	}
	var goalItems []GoalItem
	if stored.Goal.active() {
		goalItems = stored.Goal.Items
	}
	items, orders := map[string]bool{}, map[string]bool{}
	for _, item := range goalItems {
		items[item.ID] = true
		if item.WorkOrderID != "" {
			orders[item.WorkOrderID] = true
		}
	}
	// One work order per item: an item that has one, or whose create autopilot
	// already applied, takes no other.
	claimed := map[string]bool{}
	for _, item := range goalItems {
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
		// target is the existing work order the action acts on, if any.
		inGoal, target := false, ""
		switch action.Type {
		case featureartifact.AssistantCreateWorkOrder:
			inGoal = items[action.VisionItemID] || builds
		case featureartifact.AssistantStartWorkOrder, featureartifact.AssistantChangeModels,
			featureartifact.AssistantContinueWorkOrder, featureartifact.AssistantRecoverWorkOrder:
			inGoal, target = orders[action.WorkOrderID] || builds, action.WorkOrderID
		case featureartifact.AssistantCloseWorkOrder:
			inGoal, target = (created[action.WorkOrderID] || builds) && !orders[action.WorkOrderID], action.WorkOrderID
		case featureartifact.AssistantAddVisionItem:
			inGoal, target = builds, action.WorkOrderID
		case featureartifact.AssistantStartAutopilot:
			// Building everything, the assistant may finish the vision.
			inGoal = builds && !stored.Goal.active()
		case featureartifact.AssistantSetUpPreview, featureartifact.AssistantWriteVision,
			featureartifact.AssistantSetUpStack:
			// An app that does not run, a project without a vision or a stack,
			// is the assistant's to fix when it builds everything (ADR-024,
			// ADR-027, ADR-028).
			inGoal = builds
		}
		// A queued start the user did themselves is done: the work order no
		// longer waits.
		if action.Type == featureartifact.AssistantStartWorkOrder && action.Queued && target != "" {
			if state, known := states[target]; known && state != feature.StateDraft && state != feature.StateReady {
				action.Status, action.Queued, action.UpdatedAt = ActionApplied, false, now
				action.Result = "The user started it."
				stored.addEvent(fmt.Sprintf("You started “%s”, so it no longer waits for “%s”.",
					titles[target], followsName(action.After, titles)), now)
				noted = true
				continue
			}
		}
		if !inGoal {
			continue
		}
		// Work that follows other work waits until that has merged, so two
		// changes to the same part of the app do not run at once. A new work
		// order is created at once, so it shows as a draft; only its start
		// waits.
		holdStart := false
		if follows, waiting := stored.waitsFor(*action, titles, states); waiting {
			if action.Type == featureartifact.AssistantCreateWorkOrder {
				holdStart = true
				stored.addEvent(fmt.Sprintf("Autopilot created %s and queued its start: it starts once “%s” has merged.",
					actionName(action.AssistantAction), follows), now)
				noted = true
			} else {
				if !action.Queued {
					action.Queued = true
					stored.addEvent(fmt.Sprintf("Autopilot queued %s: it starts once “%s” has merged.",
						actionName(action.AssistantAction), follows), now)
					noted = true
				}
				continue
			}
		}
		action.Queued = false
		action.AutoTried = true
		if target != "" && !stored.mayAct(target, ours) {
			title := titles[target]
			if title == "" {
				title = target
			}
			stored.addEvent(fmt.Sprintf("Autopilot left %s for you: “%s” is your work order, so it waits for you.",
				actionName(action.AssistantAction), title), now)
			noted = true
			continue
		}
		if action.Type == featureartifact.AssistantRecoverWorkOrder && action.How == "back_to_ready" {
			// Back to Ready throws work away: always the user's call.
			stored.addEvent(fmt.Sprintf("Autopilot left %s for you: sending work back to Ready needs your approval.",
				actionName(action.AssistantAction)), now)
			noted = true
			continue
		}
		if action.Type == featureartifact.AssistantContinueWorkOrder || action.Type == featureartifact.AssistantRecoverWorkOrder {
			if stored.AutoContinues[action.WorkOrderID] >= maxAutoContinues {
				stored.addEvent(fmt.Sprintf("Autopilot has continued %s %d times; the next try is yours to approve.",
					action.WorkOrderID, maxAutoContinues), now)
				noted = true
				continue
			}
			if stored.AutoContinues == nil {
				stored.AutoContinues = map[string]int{}
			}
			stored.AutoContinues[action.WorkOrderID]++
		}
		if action.Type == featureartifact.AssistantCreateWorkOrder && action.VisionItemID != "" && claimed[action.VisionItemID] {
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
		action.HoldStart = holdStart
		apply = append(apply, *action)
	}
	if len(apply) == 0 && !noted {
		return nil, nil
	}
	stored.UpdatedAt = now
	return apply, service.write(stored)
}

// workOrders lists a project's work orders the assistant created, as stored
// with each (ADR-027), and every work order's title and state.
func (service *Service) workOrders(
	ctx context.Context, scope string,
) (map[string]bool, map[string]string, map[string]feature.State) {
	ours, titles, states := map[string]bool{}, map[string]string{}, map[string]feature.State{}
	if service.config.Features == nil || scope == TopLevel {
		return ours, titles, states
	}
	features, err := service.config.Features.List(ctx, scope)
	if err != nil {
		return ours, titles, states
	}
	// Building everything, the assistant owns every work order, the user's
	// too.
	builds := service.assistantBuilds(ctx, scope)
	for _, item := range features {
		titles[item.ID], states[item.ID] = item.Title, item.State
		if builds || item.CreatedBy == feature.CreatedByAssistant {
			ours[item.ID] = true
		}
	}
	return ours, titles, states
}

// waitsFor reports whether an action must wait for the work it follows
// (After): a work order, named by its ID or title, that has not merged or
// closed yet, or another of the assistant's actions not yet applied. It
// returns the title of what it waits for. Work it cannot find does not hold
// it back.
func (stored record) waitsFor(
	action Action, titles map[string]string, states map[string]feature.State,
) (string, bool) {
	after := strings.TrimSpace(action.After)
	if after == "" || (action.Type != featureartifact.AssistantCreateWorkOrder &&
		action.Type != featureartifact.AssistantStartWorkOrder) {
		return "", false
	}
	unfinished := func(id string) (string, bool) {
		state, known := states[id]
		return titles[id], known && !state.IsTerminal()
	}
	if _, known := states[after]; known {
		return unfinished(after)
	}
	for _, other := range stored.Actions {
		if other.ID == action.ID || !strings.EqualFold(strings.TrimSpace(other.Title), after) {
			continue
		}
		switch {
		case other.Status == ActionApplied && other.WorkOrderID != "":
			return unfinished(other.WorkOrderID)
		case other.Status == ActionProposed:
			return other.Title, true
		}
	}
	for id, title := range titles {
		if strings.EqualFold(title, after) {
			return unfinished(id)
		}
	}
	return "", false
}

// QueuedStarts names, by work order, the work each of a project's work
// orders waits for before autopilot starts it: a queued start action. The
// user may still start one; it then no longer waits.
func (service *Service) QueuedStarts(ctx context.Context, scope string) (map[string]string, error) {
	service.mu.Lock()
	stored, err := service.read(scope)
	service.mu.Unlock()
	if err != nil {
		return nil, err
	}
	queued := map[string]string{}
	var titles map[string]string
	for _, action := range stored.Actions {
		if action.Type != featureartifact.AssistantStartWorkOrder || action.Status != ActionProposed ||
			!action.Queued || action.WorkOrderID == "" {
			continue
		}
		if titles == nil {
			_, titles, _ = service.workOrders(ctx, scope)
		}
		queued[action.WorkOrderID] = followsName(action.After, titles)
	}
	return queued, nil
}

// followsName is the title of the work an action follows, as After names
// it by ID or title.
func followsName(after string, titles map[string]string) string {
	if title := titles[strings.TrimSpace(after)]; title != "" {
		return title
	}
	return strings.TrimSpace(after)
}

// CreatorMarker records that the assistant created work orders.
type CreatorMarker interface {
	MarkCreatedByAssistant(ctx context.Context, projectID string, featureIDs []string) error
}

// MarkCreatedWork records as the assistant's the work orders its
// conversations show it created (applied create_work_order actions), for
// work orders from before their creator was stored (ADR-027). Everything
// else stays the user's. It is safe to repeat: it only ever marks the
// assistant's.
func (service *Service) MarkCreatedWork(ctx context.Context, marker CreatorMarker) error {
	service.mu.Lock()
	created := map[string][]string{}
	for _, scope := range service.projectScopes() {
		stored, err := service.read(scope)
		if err != nil {
			service.mu.Unlock()
			return err
		}
		for _, action := range stored.Actions {
			if action.Type == featureartifact.AssistantCreateWorkOrder && action.Status == ActionApplied &&
				action.WorkOrderID != "" {
				created[scope] = append(created[scope], action.WorkOrderID)
			}
		}
	}
	service.mu.Unlock()
	for scope, ids := range created {
		if err := marker.MarkCreatedByAssistant(ctx, scope, ids); err != nil {
			return err
		}
	}
	return nil
}

// mayAct reports whether the assistant may act on a work order without the
// user's approval (ADR-027): one it created, as stored with the work order
// (ours) or as its applied create shows, or one the user handed it with the
// goal. Every other work order is the user's.
func (stored record) mayAct(workOrderID string, ours map[string]bool) bool {
	if ours[workOrderID] || stored.Goal.Hands(workOrderID) {
		return true
	}
	for _, action := range stored.Actions {
		if action.Type == featureartifact.AssistantCreateWorkOrder && action.Status == ActionApplied &&
			action.WorkOrderID == workOrderID {
			return true
		}
	}
	return false
}

// AutopilotHandles reports whether autopilot is still on a work order that
// blocked or failed at blockedAt, so it need not ask for the user yet. It is
// while the work order is autopilot's (a goal item, or any of the
// assistant's own work where it builds everything; never the user's own),
// has continues left, and the assistant has yet to answer the blocker or has
// proposed a continue that autopilot is about to apply. Once the assistant
// answered without one, or left one for the user, the work order needs the
// user.
func (service *Service) AutopilotHandles(ctx context.Context, scope, workOrderID string, blockedAt time.Time) bool {
	builds := service.assistantBuilds(ctx, scope)
	ours, _, _ := service.workOrders(ctx, scope)
	if _, err := service.Readiness(ctx); err != nil {
		return false
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil || stored.AutoContinues[workOrderID] >= maxAutoContinues {
		return false
	}
	if _, watched := stored.Watched[workOrderID]; !watched {
		return false
	}
	inGoal := builds
	if stored.Goal.active() {
		for _, item := range stored.Goal.Items {
			inGoal = inGoal || item.WorkOrderID == workOrderID
		}
	}
	if !inGoal || !stored.mayAct(workOrderID, ours) {
		return false
	}
	for _, action := range stored.Actions {
		if (action.Type == featureartifact.AssistantContinueWorkOrder || action.Type == featureartifact.AssistantRecoverWorkOrder) &&
			action.WorkOrderID == workOrderID &&
			action.Status == ActionProposed {
			// Not yet tried, autopilot applies it; tried, it waits for the user.
			return !action.AutoTried
		}
	}
	if stored.Status == StatusRunning || len(stored.PendingEvents) > 0 {
		return true
	}
	for _, message := range stored.Messages {
		if message.Role == "assistant" && !message.OccurredAt.Before(blockedAt) {
			return false
		}
	}
	return true
}

// isApp reports whether a project is used as an app (APPS_PLAN.md): how the
// assistant speaks to its user, not how freely it acts.
func (service *Service) isApp(ctx context.Context, scope string) bool {
	if scope == TopLevel {
		return false
	}
	stored, err := service.config.Projects.GetByID(ctx, scope)
	return err == nil && stored.App
}

// assistantBuilds reports whether the assistant builds everything in a
// project (ADR-027): its actions apply without cards, as on autopilot.
func (service *Service) assistantBuilds(ctx context.Context, scope string) bool {
	if scope == TopLevel {
		return false
	}
	stored, err := service.config.Projects.GetByID(ctx, scope)
	return err == nil && stored.AssistantBuilds()
}

// note adds an event line to a scope's conversation.
func (service *Service) note(scope, text string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return err
	}
	// A step the watcher retries each look (a launch whose setup is refused
	// until an agent has usage again, say) is noted once, not every time.
	if last := len(stored.Messages) - 1; last >= 0 && stored.Messages[last].Role == "event" && stored.Messages[last].Text == text {
		return nil
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
		"merged; at most two of the goal's work orders run at a time, and only when they touch different parts of " +
		"the app (two that both change the same screens, navigation, or shared files run one after the other, or " +
		"the second cannot merge). Restart a goal item that is back in Ready. When one is blocked or failed, find out " +
		"why (its reason, its pull request, the code) and propose continue_work_order with guidance that fixes " +
		"it; Commitarium continues a work order on its own at most twice. When a work order needs the user (a " +
		"question about the product, a merge approval, a blocker you cannot fix), tell the user exactly what it " +
		"needs. When autopilot made a duplicate work order for an item, propose close_work_order for the one that " +
		"does not deliver it; autopilot closes its own duplicates. Propose nothing outside the goal unless the " +
		"user asks; such actions still wait for approval. A work order the user created by hand after the goal " +
		"started is theirs, even on a goal item: actions on it wait for their approval.")
	return builder.String()
}
