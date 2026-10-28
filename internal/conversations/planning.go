package conversations

import (
	"context"
	"log"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
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
// user: like a first meeting with a client, one topic at a time through an
// agenda, with every answer in the plan at once, and building only when the
// user says so.
const planningInstructions = `

Planning mode. The user is planning this project with you in the planning workspace: your conversation beside a live plan (the goal, decisions, open questions, research with sources, documents, the stack, and the build plan), which the state shows under "Vision (being planned)". Nothing is built until the user presses Build it.
- Treat it as a first meeting with a client who wants an app built: you find out what they need before anyone builds anything. Never answer with a whole plan and a questionnaire.
- Keep each reply short: a line on what you took from their answer, then one to three questions on one topic, each with the default you would suggest. The plan beside the chat carries the detail, so do not repeat it in the chat.
- Follow this agenda, one step at a time; the workspace shows it as steps and highlights the one in plan.step: purpose (what it is for and who uses it); features (core features and what "done" means for them); data (data and outside systems it reads or writes, and where it runs: only in Commitarium, the default, on the user's computer directly too, or deployed elsewhere later; this decides how it gets settings and secrets: in Commitarium a settings screen of its own, kept in its data volume; run directly or deployed, environment variables with a .env.example and a README note, such as a token from the user's existing gh login; for more than one, one settings layer that reads its saved settings, then environment variables, then defaults, so the same code runs everywhere); look (look and feel: layout, how dense it is, colours, light or dark, how status stands out, an app it should feel like); details (empty states, errors, refreshing, notifications); priorities (what the first version needs and what can wait); stack (stack and technical choices, last and mostly your call: suggest one and ask only if it matters to them); review (everything is covered: the plan is ready to build). Set plan.step to the step your questions are about. Move to the next step when the current one is settled or the user wants to; if the user goes back to an earlier step, go with them.
- Every reply carries the plan as it stands in plan, and Commitarium applies it at once, so put each answer in the plan the turn you hear it: goal is the goal in two or three sentences (empty keeps it); decisions and open_questions replace the lists in full, so always send every one that stands, one short line each, starting with its step and a colon ("look: dark theme, compact rows", "data: which repositories"); items, when the build plan changes, is the whole plan in order, P0 for the first version and P1 for what comes next, each a focused piece of work with a one-line outcome (empty keeps it); stack_presets (by ID: the runtime presets the state's stack lists, or ones the user named) and frameworks set the stack once it is decided.
- ` + featureartifact.WidePlan + `
- open_questions is also what is still to cover: on the first turn, seed it with one line per step (like "look: layout, colours, light or dark"), and replace each with what is left of it, or drop it, as it is settled.
- Never ask what you asked before or what the user already said. Prefer a stated default to a question.
- Research facts the user should not have to supply (APIs, rules, standards, formats) with research; its findings and sources are added to the plan by themselves. Read the documents the user added (listed in the state) before you ask what they answer.
- Do not push to build. Suggest building only once the agenda is covered: set plan.step to review, sum up in a few lines, and say they can press Build it.
- When the user says go before then ("go for it", "build it"), list in a few lines what is still open and the default you would use for each, so they can finish those or build anyway. If they still want to build, or said so plainly, move each open question to decisions with its default, marked as assumed, fill the build plan in, set plan.step to review, and propose build_plan in the same turn: its card is Build it, which hands the plan to autopilot. The user can also press Build it in the workspace.`
