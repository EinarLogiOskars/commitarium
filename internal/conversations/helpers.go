package conversations

import (
	"context"
	"fmt"
	"log"
	"unicode"
	"unicode/utf8"
)

// Helpers (ADR-035): the brief writer, the vision writer, and the stack
// designer, run in the background for the assistant. The user talks to the
// assistant, and the assistant to them: a helper's question wakes the
// assistant, which answers it with answer_helper, and a helper's result comes
// back as a card. Only helpers the assistant started are followed here; those
// the user starts on the Vision or Stack page keep their own chat.

type HelperKind string

const (
	HelperBrief  HelperKind = "brief"
	HelperVision HelperKind = "vision"
	HelperStack  HelperKind = "stack"
)

// Helper is a helper session the assistant started.
type Helper struct {
	// ID is the helper's session, the name the assistant answers it by.
	ID   string     `json:"id"`
	Kind HelperKind `json:"kind"`
	// WorkOrderID and Title name the work order a brief writer writes for.
	WorkOrderID string `json:"work_order_id,omitempty"`
	Title       string `json:"title,omitempty"`
	// Seen is the helper's state as last followed; Done ends following it.
	Seen string `json:"seen,omitempty"`
	Done bool   `json:"done,omitempty"`
}

type HelperStatus string

const (
	HelperWorking  HelperStatus = "working"
	HelperAsks     HelperStatus = "asks"
	HelperProposes HelperStatus = "proposes"
	HelperFinished HelperStatus = "finished"
	HelperFailed   HelperStatus = "failed"
)

// HelperState is a helper as its service reports it. Turn counts its turns,
// so a new question or proposal differs from the last; Text is the question,
// the proposal in plain words, or why it stopped.
type HelperState struct {
	Status HelperStatus
	Turn   int
	Text   string
}

// HelperReader reads a helper's state through its service.
type HelperReader func(ctx context.Context, projectID string, helper Helper) (HelperState, error)

// Name is how the user and the assistant hear of the helper.
func (helper Helper) Name() string {
	switch helper.Kind {
	case HelperBrief:
		return fmt.Sprintf("the brief writer for “%s”", helper.Title)
	case HelperVision:
		return "the vision writer"
	default:
		return "the stack designer"
	}
}

// LookUpHelper returns a helper the assistant started in the scope, so only
// those are answered for it.
func (service *Service) LookUpHelper(_ context.Context, scope, id string) (Helper, error) {
	stored, err := service.readLocked(scope)
	if err != nil {
		return Helper{}, err
	}
	for _, helper := range stored.Helpers {
		if helper.ID == id {
			return helper, nil
		}
	}
	return Helper{}, ErrNotFound
}

// follow records a helper an applied action started, or follows it afresh
// when the action started it again.
func (stored *record) follow(helper Helper) {
	for index := range stored.Helpers {
		if stored.Helpers[index].ID == helper.ID {
			stored.Helpers[index].Done = false
			return
		}
	}
	stored.Helpers = append(stored.Helpers, helper)
}

// followHelpers looks at each helper the assistant started: a question wakes
// the assistant, a result becomes a card, and a helper that stopped short
// wakes it to decide what next.
func (watcher Watcher) followHelpers(ctx context.Context, scope string) {
	if watcher.Helpers == nil {
		return
	}
	stored, err := watcher.Service.readLocked(scope)
	if err != nil {
		return
	}
	states := map[string]HelperState{}
	for _, helper := range stored.Helpers {
		if helper.Done {
			continue
		}
		state, err := watcher.Helpers(ctx, scope, helper)
		if err != nil {
			log.Printf("follow helper %s of %s: %v", helper.ID, scope, err)
			continue
		}
		states[helper.ID] = state
	}
	if len(states) == 0 {
		return
	}
	if err := watcher.Service.helpersChanged(scope, states); err != nil {
		log.Printf("helpers of %s: %v", scope, err)
	}
}

func (service *Service) helpersChanged(scope string, states map[string]HelperState) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	stored, err := service.read(scope)
	if err != nil {
		return err
	}
	now, changed := service.now(), false
	for index := range stored.Helpers {
		helper := &stored.Helpers[index]
		state, ok := states[helper.ID]
		seen := fmt.Sprintf("%s:%d", state.Status, state.Turn)
		if !ok || helper.Done || helper.Seen == seen {
			continue
		}
		helper.Seen, changed = seen, true
		name := helper.Name()
		switch state.Status {
		case HelperAsks:
			stored.addEvent(capitalized(name)+" asks: "+state.Text, now)
			stored.PendingEvents = append(stored.PendingEvents, fmt.Sprintf(
				"%s (helper %s) asks: %s\nAnswer it with answer_helper (helper %s) from what you know of the project. "+
					"If only the user can answer, ask them in your message, and answer the helper on your next turn.",
				capitalized(name), helper.ID, state.Text, helper.ID))
		case HelperFinished:
			helper.Done = true
		case HelperFailed:
			helper.Done = true
			text := capitalized(name) + " stopped before it finished."
			if state.Text != "" {
				text += " " + state.Text
			}
			stored.addEvent(text, now)
			stored.PendingEvents = append(stored.PendingEvents,
				text+" Start it again if it is still needed, or tell the user plainly what is wrong.")
		}
	}
	if !changed {
		return nil
	}
	stored.UpdatedAt = now
	if len(stored.PendingEvents) > 0 {
		service.nudgeWatcher()
	}
	return service.write(stored)
}

func capitalized(text string) string {
	first, size := utf8.DecodeRuneInString(text)
	return string(unicode.ToUpper(first)) + text[size:]
}
