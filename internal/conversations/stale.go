package conversations

import (
	"context"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// Stale cards: a proposed action whose work order already got where the
// action leads (started, closed, or no longer blocked, by the user or by the
// work itself) is not needed any more. It settles itself, in every mode,
// rather than wait for an approval that would only fail.

// StaleResult says why an action on a work order in state is no longer
// needed, or "" while it still is. blocked reports whether the work order's
// run waits on a blocker, when canTell; create_work_order and create_app
// never go stale.
func StaleResult(action featureartifact.AssistantAction, state feature.State, blocked, canTell bool) string {
	switch action.Type {
	case featureartifact.AssistantStartWorkOrder:
		if state != feature.StateDraft && state != feature.StateReady {
			return "Already started."
		}
	case featureartifact.AssistantCloseWorkOrder:
		if state.IsTerminal() {
			return "Already closed."
		}
	case featureartifact.AssistantContinueWorkOrder, featureartifact.AssistantRecoverWorkOrder:
		// A message to the lead may be sent while it works.
		if action.How != "message" && canTell && !blocked {
			return "No longer needed: the work order is not waiting on that blocker any more."
		}
	}
	return ""
}

// settleStale settles each proposed action that is no longer needed, and
// reports whether it settled any. A queued start the user did themselves is
// noted, since autopilot told the user it waited.
func (stored *record) settleStale(
	titles map[string]string, states map[string]feature.State, blocked func(string) bool, canTell bool, at time.Time,
) bool {
	settled := false
	for index := range stored.Actions {
		action := &stored.Actions[index]
		if action.Status != ActionProposed || action.WorkOrderID == "" {
			continue
		}
		state, known := states[action.WorkOrderID]
		if !known {
			continue
		}
		isBlocked := false
		if canTell && (action.Type == featureartifact.AssistantContinueWorkOrder ||
			action.Type == featureartifact.AssistantRecoverWorkOrder) {
			isBlocked = blocked(action.WorkOrderID)
		}
		result := StaleResult(action.AssistantAction, state, isBlocked, canTell)
		if result == "" {
			continue
		}
		if action.Queued {
			stored.addEvent(fmt.Sprintf("You started “%s”, so it no longer waits for “%s”.",
				titles[action.WorkOrderID], followsName(action.After, titles)), at)
		}
		action.Status, action.Settled, action.Queued, action.Result, action.UpdatedAt = ActionApplied, true, false, result, at
		settled = true
	}
	return settled
}

// Settle records that a proposed action was no longer needed, with why: the
// user pressed its button, or autopilot tried it, after its work order got
// there anyway.
func (service *Service) Settle(ctx context.Context, scope, actionID, result string) (Conversation, error) {
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	chat, err := service.chatOfAction(ctx, scope, actionID)
	if err != nil {
		return Conversation{}, err
	}
	stored, done, err := service.open(scope, chat)
	if err != nil {
		return Conversation{}, err
	}
	defer done()
	for index := range stored.Actions {
		action := &stored.Actions[index]
		if action.ID != actionID {
			continue
		}
		if action.Status != ActionProposed {
			return Conversation{}, ErrNotPending
		}
		now := service.now()
		action.Status, action.Settled, action.Queued, action.Result, action.UpdatedAt = ActionApplied, true, false, result, now
		stored.UpdatedAt = now
		return stored.Conversation, service.write(stored)
	}
	return Conversation{}, ErrNotFound
}

// settleIfStale settles an action that is no longer needed, as its work
// order is now, and reports whether it did.
func (service *Service) settleIfStale(ctx context.Context, scope string, action Action) bool {
	_, _, states, _ := service.workOrders(ctx, scope)
	state, known := states[action.WorkOrderID]
	if action.WorkOrderID == "" || !known {
		return false
	}
	canTell := service.config.Blocked != nil
	blocked := canTell && service.config.Blocked(ctx, action.WorkOrderID)
	result := StaleResult(action.AssistantAction, state, blocked, canTell)
	if result == "" {
		return false
	}
	_, err := service.Settle(ctx, scope, action.ID, result)
	return err == nil
}
