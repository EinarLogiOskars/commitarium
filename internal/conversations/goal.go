package conversations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
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

// ErrNothingLeft is a goal whose items are all done: there is nothing for
// autopilot to finish, so a card that would start it is no longer needed.
var ErrNothingLeft = fmt.Errorf("%w: nothing left to do", ErrInvalid)

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
		return Conversation{}, fmt.Errorf("%w in %s", ErrNothingLeft, name)
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

// follow points a goal item at the work order created for it.
func (goal *Goal) follow(itemID, workOrderID string) {
	for index := range goal.Items {
		if itemID != "" && workOrderID != "" && goal.Items[index].ID == itemID {
			goal.Items[index].WorkOrderID = workOrderID
		}
	}
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
// work order the user created, is left for the user, with a note. A goal
// item's work order is closed only once its recovery ran out or, where the
// assistant builds everything, once it is blocked and the assistant proposes
// its replacement with the close; the replacement is then created in the
// same look.
func (service *Service) autopilotActions(ctx context.Context, scope string) ([]Action, error) {
	builds := service.assistantBuilds(ctx, scope)
	ours, titles, states, made := service.workOrders(ctx, scope)
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return nil, err
	}
	now := service.now()
	// A card whose work order already got where it leads settles itself, in
	// every mode.
	settled := stored.settleStale(titles, states, func(workOrderID string) bool {
		return service.config.Blocked != nil && service.config.Blocked(ctx, workOrderID)
	}, service.config.Blocked != nil, now)
	answers := slices.ContainsFunc(stored.Actions, func(action Action) bool {
		return sentAtOnce(action.Type) && action.Status == ActionProposed && !action.AutoTried
	})
	if !stored.Goal.active() && !builds && !answers {
		if settled {
			stored.UpdatedAt = now
			return nil, service.write(stored)
		}
		return nil, nil
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
	// One work order per item: an item whose work order is still open, or
	// whose create autopilot already applied for one still open, takes no
	// other. A closed work order, or one this look closes, frees its item for
	// a replacement.
	claims := map[string][]string{}
	for _, item := range goalItems {
		if item.WorkOrderID != "" {
			claims[item.ID] = append(claims[item.ID], item.WorkOrderID)
		}
	}
	// Work orders autopilot created: those not delivering an item (a
	// duplicate) are its own to close.
	created := map[string]bool{}
	for _, action := range stored.Actions {
		if action.Type == featureartifact.AssistantCreateWorkOrder && action.Status == ActionApplied {
			claims[action.VisionItemID] = append(claims[action.VisionItemID], action.WorkOrderID)
			if action.AutoTried && action.WorkOrderID != "" {
				created[action.WorkOrderID] = true
			}
		}
	}
	closing := map[string]bool{}
	claimed := func(itemID string) bool {
		return slices.ContainsFunc(claims[itemID], func(workOrderID string) bool {
			return workOrderID == "" || (!closing[workOrderID] && states[workOrderID] != feature.StateCancelled)
		})
	}
	exhausted := func(workOrderID string) bool {
		return service.config.RecoveryExhausted != nil && service.config.RecoveryExhausted(ctx, workOrderID)
	}
	blocked := func(workOrderID string) bool {
		return service.config.Blocked != nil && service.config.Blocked(ctx, workOrderID)
	}
	// replaced reports whether a create for a vision item a work order
	// claims waits to be applied: the work order's replacement.
	replaced := func(workOrderID string) bool {
		return slices.ContainsFunc(stored.Actions, func(action Action) bool {
			return action.Type == featureartifact.AssistantCreateWorkOrder && action.Status == ActionProposed &&
				!action.AutoTried && slices.Contains(claims[action.VisionItemID], workOrderID)
		})
	}
	// Closes go first, so a replacement proposed with one is not held back
	// by the work order it replaces.
	order := make([]int, len(stored.Actions))
	for index := range order {
		order[index] = index
	}
	slices.SortStableFunc(order, func(a, b int) int {
		isClose := func(index int) bool { return stored.Actions[index].Type == featureartifact.AssistantCloseWorkOrder }
		switch {
		case isClose(a) && !isClose(b):
			return -1
		case isClose(b) && !isClose(a):
			return 1
		}
		return 0
	})
	running, ceiling := service.running(ctx, scope, stored.earlyPlanners()), service.maxRunning(ctx)
	var steps map[string]project.WorkSteps
	planned := func(action Action) bool {
		if steps == nil {
			steps = service.workSteps(ctx, scope)
		}
		return plansFirst(action, steps)
	}
	apply, noted := make([]Action, 0), settled
	for _, index := range order {
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
			// A goal item's work order is closed only once its recovery ran
			// out, or, building everything, once it is blocked and its
			// replacement comes with the close: a fresh one replaces it. The
			// user's own work order stays theirs.
			target = action.WorkOrderID
			inGoal = ((created[target] || builds) && !orders[target]) || (orders[target] && (exhausted(target) ||
				(builds && stored.mayAct(target, made) && replaced(target) && blocked(target))))
		case featureartifact.AssistantAddVisionItem:
			inGoal, target = builds, action.WorkOrderID
		case featureartifact.AssistantStartAutopilot:
			// Building everything, the assistant may finish the vision.
			inGoal = builds && !stored.Goal.active()
		case featureartifact.AssistantSetUpPreview, featureartifact.AssistantWriteVision,
			featureartifact.AssistantSetUpStack, featureartifact.AssistantDesignStack, featureartifact.AssistantApplyStack:
			// An app that does not run, a project without a vision or a stack,
			// is the assistant's to fix when it builds everything (ADR-024,
			// ADR-027, ADR-028).
			inGoal = builds
		case featureartifact.AssistantWriteBrief, featureartifact.AssistantAcceptBrief:
			inGoal, target = builds, action.WorkOrderID
		case featureartifact.AssistantAnswerHelper, featureartifact.AssistantResearch, featureartifact.AssistantUpdatePlan:
			// Answering a helper the assistant started, or researching a
			// question, changes nothing in the project, and updating the plan
			// only fills in what the user watches being planned: each is sent
			// at once, assisting too (ADR-035).
			inGoal = true
		}
		if !inGoal {
			continue
		}
		// Work that needs other work's code or result waits until that has
		// merged. A new work order is created at once, so it shows as a
		// draft; only its start waits.
		holdStart := false
		if follows, followed, waiting := stored.waitsFor(*action, titles, states); waiting {
			if action.Type == featureartifact.AssistantCreateWorkOrder {
				holdStart = true
				stored.addEvent(fmt.Sprintf("Autopilot created %s and queued its start: it starts once “%s” has merged.",
					actionName(action.AssistantAction), follows), now)
				noted = true
			} else if implementing(states[followed]) && planned(*action) {
				// It plans while the work it follows is built, and
				// implements once that has merged (ADR-037).
				if !action.PlansEarly {
					action.PlansEarly = true
					stored.addEvent(fmt.Sprintf("Autopilot started %s to plan now: it implements once “%s” has merged.",
						actionName(action.AssistantAction), follows), now)
					noted = true
				}
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
		// Work orders run side by side up to the ceiling the user set; a
		// start beyond it waits for a slot (ADR-036).
		// Planning early takes no slot: it counts once it implements.
		if starts := (action.Type == featureartifact.AssistantStartWorkOrder && !action.PlansEarly) ||
			(action.Type == featureartifact.AssistantCreateWorkOrder && !holdStart && !action.Brief); starts {
			if running >= ceiling {
				if action.Type == featureartifact.AssistantCreateWorkOrder {
					holdStart = true
					stored.addEvent(fmt.Sprintf("Autopilot created %s and queued its start: %s", actionName(action.AssistantAction),
						ceilingNote(running)), now)
					noted = true
				} else {
					if !action.Queued {
						action.Queued = true
						stored.addEvent(fmt.Sprintf("Autopilot queued %s: %s", actionName(action.AssistantAction),
							ceilingNote(running)), now)
						noted = true
					}
					continue
				}
			} else {
				running++
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
		if action.Type == featureartifact.AssistantRecoverWorkOrder && action.How == "back_to_ready" && !builds {
			// Back to Ready throws work away: the user's call, unless the
			// assistant builds everything here, where it may start stuck work
			// over as it may close and replace it.
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
		if action.Type == featureartifact.AssistantCreateWorkOrder && action.VisionItemID != "" && claimed(action.VisionItemID) {
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
		switch action.Type {
		case featureartifact.AssistantCreateWorkOrder:
			claims[action.VisionItemID] = append(claims[action.VisionItemID], "")
		case featureartifact.AssistantCloseWorkOrder:
			closing[target] = true
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
// with each (ADR-027) and, building everything, every work order; every
// work order's title and state; and those the assistant created.
func (service *Service) workOrders(
	ctx context.Context, scope string,
) (map[string]bool, map[string]string, map[string]feature.State, map[string]bool) {
	ours, titles, states, made := map[string]bool{}, map[string]string{}, map[string]feature.State{}, map[string]bool{}
	if service.config.Features == nil || scope == TopLevel {
		return ours, titles, states, made
	}
	features, err := service.config.Features.List(ctx, scope)
	if err != nil {
		return ours, titles, states, made
	}
	// Building everything, the assistant owns every work order, the user's
	// too.
	builds := service.assistantBuilds(ctx, scope)
	for _, item := range features {
		titles[item.ID], states[item.ID] = item.Title, item.State
		made[item.ID] = item.CreatedBy == feature.CreatedByAssistant
		if builds || item.CreatedBy == feature.CreatedByAssistant {
			ours[item.ID] = true
		}
	}
	return ours, titles, states, made
}

// running counts a project's work orders that are being worked on: planned,
// implemented, or reviewed. Sessions are the user's, background work
// Commitarium's own, and work waiting to merge needs no agent. Work that
// plans early (early) counts only once it implements (ADR-037).
func (service *Service) running(ctx context.Context, scope string, early map[string]bool) int {
	if service.config.Features == nil || scope == TopLevel {
		return 0
	}
	features, err := service.config.Features.List(ctx, scope)
	if err != nil {
		return 0
	}
	running := 0
	for _, item := range features {
		if item.Background || item.Kind == feature.KindSession {
			continue
		}
		switch {
		case item.State == feature.StatePlanning && early[item.ID]:
		case item.State == feature.StatePlanning, item.State == feature.StateImplementing,
			item.State == feature.StateReviewing:
			running++
		}
	}
	return running
}

// earlyPlanners are the work orders autopilot started to plan early
// (ADR-037).
func (stored record) earlyPlanners() map[string]bool {
	early := map[string]bool{}
	for _, action := range stored.Actions {
		if action.PlansEarly && action.Status == ActionApplied && action.WorkOrderID != "" {
			early[action.WorkOrderID] = true
		}
	}
	return early
}

// implementing reports whether work is past its plan: it has a published,
// reviewed plan, and its code is being written, reviewed, or merged.
func implementing(state feature.State) bool {
	switch state {
	case feature.StateImplementing, feature.StateReviewing, feature.StateReadyToMerge:
		return true
	}
	return false
}

// plansFirst reports whether a start runs a documented plan before it
// implements, so it can plan early: its own steps, or else its work
// order's. Quick work has no plan to make early.
func plansFirst(action Action, steps map[string]project.WorkSteps) bool {
	chosen := action.Steps
	if chosen == "" {
		current, known := steps[action.WorkOrderID]
		if !known {
			return false
		}
		chosen = stepsName(current)
	}
	return chosen != "quick"
}

// workSteps are the steps of a project's work orders.
func (service *Service) workSteps(ctx context.Context, scope string) map[string]project.WorkSteps {
	steps := map[string]project.WorkSteps{}
	if service.config.Features == nil || scope == TopLevel {
		return steps
	}
	features, err := service.config.Features.List(ctx, scope)
	if err != nil {
		return steps
	}
	for _, item := range features {
		steps[item.ID] = item.Steps
	}
	return steps
}

// ImplementationHold says whether a work order autopilot started to plan
// early waits before it implements, and why in plain words: the work it
// follows has not merged, or as many work orders as may run at once already
// do (ADR-037). Any other work order is not held.
func (service *Service) ImplementationHold(ctx context.Context, scope, workOrderID string) (string, bool) {
	service.mu.Lock()
	stored, err := service.read(scope)
	service.mu.Unlock()
	if err != nil {
		return "", false
	}
	var start *Action
	for index := range stored.Actions {
		action := &stored.Actions[index]
		if action.PlansEarly && action.Status == ActionApplied && action.WorkOrderID == workOrderID &&
			(start == nil || action.UpdatedAt.After(start.UpdatedAt)) {
			start = action
		}
	}
	if start == nil {
		return "", false
	}
	_, titles, states, _ := service.workOrders(ctx, scope)
	if follows, _, waiting := stored.waitsFor(*start, titles, states); waiting {
		return execution.ImplementationHoldReason(follows), true
	}
	if running := service.running(ctx, scope, stored.earlyPlanners()); running >= service.maxRunning(ctx) {
		return execution.ImplementationSlotReason(running), true
	}
	return "", false
}

// maxRunning is the most work orders autopilot runs at once in a project.
func (service *Service) maxRunning(ctx context.Context) int {
	settings, err := service.storedSettings(ctx)
	if err != nil || settings.MaxRunning < 1 {
		return DefaultMaxRunning
	}
	return settings.MaxRunning
}

func ceilingNote(running int) string {
	return fmt.Sprintf("%d work orders already run here, the most at once (set in Settings). It starts when one "+
		"of them is done or waits to merge.", running)
}

// waitsFor reports whether an action must wait for the work it follows
// (After): a work order, named by its ID or title, that has not merged or
// closed yet, or another of the assistant's actions not yet applied. It
// returns the title of what it waits for, and its work order once it has
// one. Work it cannot find does not hold it back.
func (stored record) waitsFor(
	action Action, titles map[string]string, states map[string]feature.State,
) (string, string, bool) {
	after := strings.TrimSpace(action.After)
	if after == "" || (action.Type != featureartifact.AssistantCreateWorkOrder &&
		action.Type != featureartifact.AssistantStartWorkOrder) {
		return "", "", false
	}
	unfinished := func(id string) (string, string, bool) {
		state, known := states[id]
		return titles[id], id, known && !state.IsTerminal()
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
			return other.Title, "", true
		}
	}
	for id, title := range titles {
		if strings.EqualFold(title, after) {
			return unfinished(id)
		}
	}
	return "", "", false
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
			_, titles, _, _ = service.workOrders(ctx, scope)
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
	ours, _, _, _ := service.workOrders(ctx, scope)
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

// AutopilotActiveAt is when autopilot last moved a work order on: the
// latest change to one of the assistant's actions on it, or now while the
// assistant is in a turn or one waits to start. A blocker autopilot keeps
// working on does not stall.
func (service *Service) AutopilotActiveAt(_ context.Context, scope, workOrderID string) time.Time {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return time.Time{}
	}
	if stored.Status == StatusRunning || len(stored.PendingEvents) > 0 {
		return service.now()
	}
	var latest time.Time
	for _, action := range stored.Actions {
		if action.WorkOrderID == workOrderID && action.UpdatedAt.After(latest) {
			latest = action.UpdatedAt
		}
	}
	return latest
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

// sentAtOnce reports whether an action is sent without a card: it only
// talks to a helper, or updates the plan being written.
func sentAtOnce(kind featureartifact.AssistantActionType) bool {
	return kind == featureartifact.AssistantAnswerHelper || kind == featureartifact.AssistantResearch ||
		kind == featureartifact.AssistantUpdatePlan
}

func actionName(action featureartifact.AssistantAction) string {
	if action.Type == featureartifact.AssistantAnswerHelper {
		return "your answer to a helper's question"
	}
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
		"approval, except those using a premium model. Start every item whose real dependencies have merged, " +
		"all at once, like a team: work orders run side by side and merge one at a time, each lead bringing in " +
		"what merged before it and resolving any conflicts. Touching the same screens or files is fine. Set after " +
		"only when an item needs another's code or result, or both rewrite the same thing wholesale: an item " +
		"waits on the plan's foundation, or on an item whose behaviour it truly uses (its description says " +
		"\"Needs: …\"), never on a sibling that only adds to the same page. Work that waits still plans early, " +
		"once the work it follows is implementing. Where the " +
		"plan gives an order, plan in waves and start each wave together. Restart a goal item that is back in Ready. When one is blocked or failed, recover it " +
		"with recover_work_order the way its blocker calls for (see How you recover blocked work). When a work " +
		"order needs the user (a question about the product, a merge approval, a secret), tell the user exactly " +
		"what it needs. When autopilot made a duplicate work order for an item, propose close_work_order for the one that " +
		"does not deliver it; autopilot closes its own duplicates. When a goal item's work order has run out of " +
		"recovery, propose close_work_order for it together with create_work_order for the same vision_item_id; " +
		"autopilot replaces it. Propose nothing outside the goal unless the " +
		"user asks; such actions still wait for approval. A work order the user created by hand after the goal " +
		"started is theirs, even on a goal item: actions on it wait for their approval.")
	return builder.String()
}
