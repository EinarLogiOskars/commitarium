package conversations

import (
	"context"
	"log"
)

// Planning mode (ONE_CONVERSATION_PLAN.md phase 2): in Developer mode, a
// project's vision can be planned with the assistant before anything is
// built, in a workspace that shows the plan beside the conversation. The
// assistant's every turn carries the plan as they talk, and research it
// starts is recorded on the plan with its sources.

// ResearchNoter records research on a vision being planned.
type ResearchNoter interface {
	AddResearch(ctx context.Context, projectID, title, findings string) error
}

// planning reports whether the project's vision is being planned.
func (service *Service) planning(ctx context.Context, scope string) bool {
	if service.config.Visions == nil || scope == TopLevel {
		return false
	}
	stored, err := service.config.Visions.Get(ctx, scope)
	return err == nil && stored.Planning.Open()
}

// noteResearch records the research helper's findings on the project's
// vision when it is being planned.
func (service *Service) noteResearch(ctx context.Context, scope, title, findings string) {
	noter, ok := service.config.Visions.(ResearchNoter)
	if !ok || scope == TopLevel {
		return
	}
	if err := noter.AddResearch(ctx, scope, title, findings); err != nil {
		log.Printf("note the research on %q for %s: %v", title, scope, err)
	}
}

// planningInstructions tell the assistant how to plan a project with the
// user: the plan fills in as they talk, with the same rules as settling a
// new app, and the user builds it when they say so.
const planningInstructions = `

Planning mode. The user is planning this project with you in the planning workspace: your conversation beside a live plan (the goal, decisions, open questions, research with sources, documents, the stack, and the build plan), which the state shows under "Vision (being planned)". Nothing is built until the user presses Build it.
- Every reply carries the plan as it stands in plan, and Commitarium applies it at once: goal is the goal in two or three sentences (empty keeps it); decisions and open_questions replace the lists in full, so always send every one that stands, one short line each; items, when the build plan changes, is the whole plan in order, P0 for what is needed first and P1 for what comes next, each a focused piece of work with a one-line outcome (empty keeps it); stack_presets (by ID: the runtime presets the state's stack lists, or ones the user named) and frameworks set the stack once it is decided.
- Ask in short rounds: at most three questions a round, only ones whose answer changes what gets built, each with the default you would pick. Never ask what you asked before or what the user already said; open questions are on the plan, so ask only those still open. Prefer a stated default to a question.
- Research facts the user should not have to supply (APIs, rules, standards, formats) with research; its findings and sources are added to the plan by themselves. Read the documents the user added (listed in the state) before you ask what they answer.
- Every third round, step back: sum up what is settled, name what is still open, and either suggest building with the open points as assumptions, or say in one line why one more answer matters.
- When the user says go at any point ("go for it", "build it"), move each open question to decisions with the default you would pick, marked as assumed, fill the build plan in, and propose build_plan in the same turn: its card is Build it, which hands the plan to autopilot. Building on fewer answers is their call. The user can also press Build it in the workspace.`
