package conversations

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// Planning mode (ONE_CONVERSATION_PLAN.md phase 2): in Developer mode, a
// project's vision can be planned with the assistant before anything is
// built, in plan mode in the chat, with the plan on its card beside it. The
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

// chatPlanning is how a chat takes part in its project's planning
// (ADR-039): the planning chat plans, and the others hear where it is.
type chatPlanning struct {
	here bool
	// planning is the vision's planning, when this chat plans it.
	planning *vision.Planning
	// elsewhere is the title of the chat that plans, when another does.
	elsewhere string
}

// planningIn tells how stored's chat takes part in its project's planning.
// Planning with no chat, or in a chat the project no longer has open, is
// every chat's, as before planning belonged to a chat.
func (service *Service) planningIn(ctx context.Context, stored record) chatPlanning {
	if service.config.Visions == nil || stored.Scope == TopLevel {
		return chatPlanning{}
	}
	current, err := service.config.Visions.Get(ctx, stored.Scope)
	if err != nil || !current.Planning.Open() {
		return chatPlanning{}
	}
	planning := current.Planning
	if planning.Chat == "" || planning.Chat == stored.Chat || !chatPattern.MatchString(planning.Chat) {
		return chatPlanning{here: true, planning: planning}
	}
	if _, err := os.Stat(service.chatPath(stored.Scope, planning.Chat)); err != nil {
		return chatPlanning{here: true, planning: planning}
	}
	other, err := service.readChatFile(stored.Scope, planning.Chat)
	if err != nil || other.Archived {
		return chatPlanning{here: true, planning: planning}
	}
	title := other.Title
	if title == "" {
		title = "Untitled"
	}
	return chatPlanning{elsewhere: title}
}

// instructions are what a turn in the chat hears of the planning.
func (planning chatPlanning) instructions() string {
	switch {
	case planning.here && planning.planning.Round:
		return roundInstructions(planning.planning.RoundPriority)
	case planning.here:
		return planningInstructions
	case planning.elsewhere != "":
		return fmt.Sprintf("\n\nThe project is being planned with the user in their chat “%s”, not this one: "+
			"talk the plan through there. Nothing new is built from the plan until the user presses Build it; "+
			"work already running carries on.", planning.elsewhere)
	}
	return ""
}

// maySuggestPlanning reports whether stored's chat may suggest plan mode
// with start_planning: a project's chat in Developer mode, which is not
// planning the project already.
func (service *Service) maySuggestPlanning(ctx context.Context, stored record) bool {
	return stored.Scope != TopLevel && stored.Developer && !service.planningIn(ctx, stored).here
}

// suggestPlanningInstructions tell the assistant, in a Developer mode
// project chat that is not planning, when to suggest plan mode rather than
// start work (ADR-039).
const suggestPlanningInstructions = `

Plan mode: the user can plan a change with you in this chat before anything is built (the plan switch). When a request is big or vague enough that guessing would build the wrong thing (several screens or parts, an outside service, or open choices that change what gets built), propose start_planning instead of work orders: reason says why in one line, and description is the goal of the planning round in a sentence (empty for the project's own). Applying it turns plan mode on in this chat, and you plan it with the user there. Build small, clear requests as usual. If the user declined plan mode for a request, do not propose it again for that request.`

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

// The planning instructions are a brief for a capable model, not a script
// (ADR-039): what planning is for, what a good plan covers, and what
// Commitarium reads from the plan. How to talk it through is the
// assistant's call.

// planningInstructions brief the assistant on planning a new project with
// the user, building only when the user says so.
const planningInstructions = `

Planning mode. You're planning this new project with the user before anything is built. The plan is in the state under "Vision (being planned)" and on the Plan card beside the chat; nothing is built until the user presses Build it.
- Help the user think it through, and ask whatever you need to make a good plan. ` + talkAndAsk + `
- ` + openingInstructions + `
- A good plan covers: purpose (what it is for and who uses it), features (and what done means for each), data (what it reads and writes, outside systems, and where it runs), look (layout, density, colours, light or dark, an app it should feel like), details (empty states, errors, refreshing, notifications), priorities (what the first version needs and what can wait), and stack (mostly your call: suggest one, and ask only if it matters to them). ` + coverage + `
- Write the plan as you go: every reply carries it in plan, Commitarium applies it at once, and the Plan card and Build it read it. goal is what the project is for in two or three sentences (empty keeps it). ` + decisionLines + ` items, when the build plan changes, is the whole build plan in order, P0 for the first version and P1 for what comes next, each a focused piece of work with a one-line outcome (empty keeps it); once the plan is written, they are its slices, an item each. stack_presets (by ID, from the state's stack or ones the user named) and frameworks set the stack once it is decided.
- ` + writtenPlan + `
- ` + featureartifact.WidePlan + `
- ` + settingsBySite + `
- ` + knowledge + `
- ` + buildIt

// roundInstructions brief the assistant on planning the next round of a
// project with built work (ADR-039): what is built stays, and the round's
// items go in from priority on.
func roundInstructions(priority int) string {
	return fmt.Sprintf(`

Planning mode, next round. You're planning the next round of this project with the user: what is built stays as it is (items marked done or with a work order in the state's "Vision (being planned)"), and this round adds to it. Nothing new is built from the plan until the user presses Build it; work already running carries on.
- Help the user think it through, and ask whatever you need to make a good plan for the round. `+talkAndAsk+`
- `+openingInstructions+`
- A good round plan covers: round (what this round is for: the change, and the problem it solves), features (what it adds, and what done means for each), priorities (what the round needs first and what can wait), and, only where the round changes them, data (new data, outside systems, settings or secrets), look (new or changed screens, in the look the app already has), and details (empty states, errors, refreshing, notifications for what it adds). A part the round leaves alone is covered once you've said so in a line. The stack is settled. `+coverage+`
- Ask about other people (who else uses the app, sharing, roles) only when the round's idea itself brings them in, such as a team view or sharing, and then concretely, naming the app.
- Write the plan as you go: every reply carries it in plan, Commitarium applies it at once, and the Plan card and Build it read it. goal stays empty unless the round changes what the project is for. `+decisionLines+` items, when the build plan changes, are this round's items in order, from P%[1]d (what the round needs first) to P%[2]d and after (what can wait), each a focused piece of work with a one-line outcome (empty keeps them). Once the plan is written, they are its slices: a large change has an item per slice, a smaller one is one item with its slices in that item's items. The built items stay whatever you send, so leave them out; an item placed before P%[1]d goes in at P%[1]d. Leave stack_presets empty unless the round needs a runtime the project does not have.
- `+writtenPlan+`
- Order the round's items so most can be built at the same time: shared new groundwork first, otherwise each item needs only what is built. When an item truly uses another's behaviour, end its description with "Needs: <that item's title>."
- `+settingsBySite+`
- `+knowledge+`
- `+buildIt, priority, min(priority+1, 9))
}

// talkAndAsk is how a planning turn talks with the user: normally, with
// question cards where a crisp choice is the clearest way to ask.
const talkAndAsk = `Talk normally, and keep the detail in the plan rather than repeating it in the chat. When a crisp choice is the clearest way to ask, put it in questions as a card: two to four short options and the one you would pick in suggested; the card always offers the user's own answer, so leave out "Something else" or "Other". Cards are a tool, not a questionnaire, and a whole plan with a list of questions is not a plan. Name the app in what you ask, so nothing reads as being about Commitarium.`

// coverage is how the assistant marks what the plan covers: a checklist,
// not an order.
const coverage = `This is a checklist, not an agenda: follow the conversation wherever it goes, and list in plan.covered the keys the plan now covers well enough to build. plan.step is the key you're mostly on, or review once everything is covered.`

// decisionLines are how decisions and open questions are written: the Plan
// card files each under its checklist key.
const decisionLines = `decisions and open_questions replace the lists in full, so send every one that stands, a short line each starting with its checklist key and a colon ("look: dark theme, compact rows"); open_questions is what is still to settle.`

// settingsBySite is how where an app runs decides its settings and secrets.
const settingsBySite = `Where the app runs decides how it gets settings and secrets: only in Commitarium (the default), a settings screen of its own, kept in its data volume; run directly or deployed elsewhere, environment variables with a .env.example and a README note (such as a token from the user's gh login); for more than one, one settings layer that reads saved settings, then environment variables, then defaults, so the same code runs everywhere.`

// knowledge is what the assistant knows or finds out rather than asks.
const knowledge = `Never ask what the user, the vision, or earlier rounds already settled, and prefer a stated default to a question. Look facts the user should not have to supply (APIs, rules, standards, formats) up with research; its findings and sources are added to the plan by themselves. Read the documents the user added (listed in the state) before asking what they answer.`

// writtenPlan is planning's one firm rule (PLAN_MODE_PLAN.md PM6): it
// ends with a written plan in commit-sized slices, drafted when the
// assistant has enough or the user asks, and revised while they review it.
const writtenPlan = `Planning ends with a written plan in commit-sized slices; Build it needs it. While you talk, keep the decisions and open questions; the written plan can wait. Once you have enough, offer to write the first draft (a card or a line: "I think I have this — want me to write the first draft of the plan?"); the user may also ask for it any time ("this is enough, plan it", or their Write the plan button). The draft is plan.document, in markdown: a # title, the goal, the decisions with their reasons, design notes, and the slices as a numbered checklist ("- [ ] 1. <title> — <what changes>; done when <...>"), each slice one commit that leaves the app working; send the same slices as items, same titles and order. After the draft the user reviews it: revise on request ("change slice 3", "merge these") and send the document in full with the items, so they stay in step, and say in a line what changed.`

// buildIt is when to suggest Build it, and how to respect "go for it".
const buildIt = `Once the plan covers what it needs and its draft is written, suggest building by proposing build_plan in that same turn, as its card is the Build it button the user presses; never ask them to press Build it without it, and don't push before then. When the user says go earlier ("go for it", "build it"), list in a few lines what is still open and the default you would use for each, so they can settle those or build anyway. If they still want to build, or said so plainly, move each open question to decisions with its default, marked as assumed, write the draft in that same update if there is none, set plan.step to review, and propose build_plan in the same turn: its card is Build it, which locks the plan and hands its slices to autopilot, a work order each. build_plan before a draft is refused.`

// openingInstructions are how planning opens: straight into what the user
// came with, or, without it, one short line that asks what to plan.
const openingInstructions = `Opening: Commitarium's note as planning starts says whether the user came with something to plan. With something, or when the chat already asks for something, go straight into it. Without, and while the plan has no decisions, open with one short line and no plan update, and ask one question, "What would you like to plan?", with the options "Suggest some ideas" (suggested) and "I have something in mind". Ideas, when asked for, are three or four concrete directions drawn from the project (what is built, the vision's guesses and open items, gaps you know of), an option each.`
