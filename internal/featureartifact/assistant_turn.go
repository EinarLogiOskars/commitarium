package featureartifact

import (
	"fmt"
	"slices"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// AssistantTurn is what the assistant returns besides its reply: the actions
// it proposes, which run only once the user approves each one, and the notes
// it keeps for itself.
type AssistantTurn struct {
	Actions []AssistantAction `json:"actions"`
	// Notes replaces the assistant's notes for its scope when non-empty:
	// decisions, preferences, and a catalog of reusable work.
	Notes string `json:"notes"`
	// ChatTitle names the chat in a few words, while it has no name the user
	// or the assistant gave it; empty keeps its name.
	ChatTitle string `json:"chat_title,omitempty"`
	// Questions are what the assistant asks the user as cards with suggested
	// answers (ADR-039), at most MaxAssistantQuestions; the user answers each
	// once, with an option or in their own words.
	Questions []AssistantQuestion `json:"questions,omitempty"`
}

// AssistantQuestion is a question to the user with the answers the
// assistant suggests: Suggested indexes the one it would pick, or is -1 when
// the question asks a fact about the user rather than a choice.
type AssistantQuestion struct {
	Text      string   `json:"text"`
	Options   []string `json:"options"`
	Suggested int      `json:"suggested"`
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
	// AssistantAddVisionItem adds an item to the vision at a priority,
	// optionally linking a work order done outside the plan.
	AssistantAddVisionItem AssistantActionType = "add_vision_item"
	// AssistantStartAutopilot hands the assistant a goal: the open items of
	// one vision priority, which it then drives to merged on its own.
	AssistantStartAutopilot AssistantActionType = "start_autopilot"
	// AssistantContinueWorkOrder continues a work order whose run waits on a
	// blocker, with optional guidance for its agents.
	AssistantContinueWorkOrder AssistantActionType = "continue_work_order"
	// AssistantRecoverWorkOrder recovers a work order whose run waits on a
	// blocker, in the way How names (ADR-032).
	AssistantRecoverWorkOrder AssistantActionType = "recover_work_order"
	// AssistantCloseWorkOrder ends a work order without merging it, keeping
	// its history: a duplicate, or work the user no longer wants.
	AssistantCloseWorkOrder AssistantActionType = "close_work_order"
	// AssistantCreateApp creates a new app at the top level: a project used
	// as an app, named by Title and built from Description, the card's
	// summary, and AppBrief, all the assistant gathered (APPS_PLAN.md,
	// ADR-035).
	AssistantCreateApp AssistantActionType = "create_app"
	// AssistantPlanProject creates a project at the top level, in Developer
	// mode only, for a request that is a real project rather than a
	// self-contained app: named by Title, with Description its goal and
	// AppBrief what the assistant gathered so far, it is planned with the
	// user before anything is built (planning mode).
	AssistantPlanProject AssistantActionType = "plan_project"
	// AssistantSetUpPreview sets the app's preview up, or fixes one that
	// fails to start, in a work order that merges only once Commitarium's
	// preview check of its commit passes (ADR-028). Guidance says what is
	// failing, for its agent.
	AssistantSetUpPreview AssistantActionType = "set_up_preview"
	// AssistantDeployApp deploys the app's latest merged version on the
	// server, for visitors (PUBLISHING_PLAN.md). Commitarium records it and
	// the user's app runs the deploy once they approve it, so it is always a
	// card, never autopilot's. Domain, when set, proposes its address too:
	// Name under Domain (empty for the domain itself) and Aliases, its extra
	// addresses (MULTI_DOMAIN_PLAN.md).
	AssistantDeployApp AssistantActionType = "deploy_app"
	// AssistantSetAppsPage turns the server's apps page on with who may open
	// it (How: public, private or company) or off (How: off), as the user
	// asked (APPS_PAGE_PLAN.md, A4). The user's app makes the change once they
	// approve it, so it is always a card. A password is never chosen here:
	// private keeps the page's password, or sends the user to Settings.
	// Domain names whose page; empty is the first domain's.
	AssistantSetAppsPage AssistantActionType = "set_apps_page"
	// AssistantListOnAppsPage lists this app on the apps page (How: list) or
	// hides it there (How: hide). Always a card.
	AssistantListOnAppsPage AssistantActionType = "list_on_apps_page"
	// The server's setup cards (MULTI_DOMAIN_PLAN.md, M5): the user's app
	// makes each change through the host once they approve it, as Settings
	// does, and tells Commitarium what the host answered, which becomes the
	// card's result. Always cards; Mayka gets no new access.
	//
	// AssistantAddDomain adds Domain to the server, with one wildcard record
	// (How: wildcard, or empty) or a record for each address (How:
	// each_name). The host checks its DNS on the way.
	AssistantAddDomain AssistantActionType = "add_domain"
	// AssistantInstallAuthentik sets authentik up on the server for Domain
	// (empty: the first), or connects Domain to the authentik already there.
	AssistantInstallAuthentik AssistantActionType = "install_authentik"
	// AssistantSetAppAccess sets who may open a deployed app (How: public,
	// private or company), with Groups for company. App names it by its main
	// address; empty is this project's app. A password is never chosen
	// here: private keeps the app's own, or sends the user to its page.
	AssistantSetAppAccess AssistantActionType = "set_app_access"
	// AssistantSetHomelab turns Homelab mode on or off (How). Off warns, in
	// the card and before anything happens, when this computer would lose
	// the server.
	AssistantSetHomelab AssistantActionType = "set_homelab"
	// AssistantWriteVision has Commitarium write the project's vision on the
	// assistant's agent from Description: what the project is for, in the
	// user's words (ADR-020), where the assistant builds everything.
	AssistantWriteVision AssistantActionType = "write_vision"
	// AssistantSetUpStack sets the project up on the stack its vision
	// recommends, or detects the stack of the code it has.
	AssistantSetUpStack AssistantActionType = "set_up_stack"
	// AssistantAnswerHelper answers a helper the assistant started (ADR-035):
	// Guidance is the answer to the question Helper asked.
	AssistantAnswerHelper AssistantActionType = "answer_helper"
	// AssistantWriteBrief has the brief writer write a draft work order's
	// brief from Description, what the assistant gathered (ADR-035).
	AssistantWriteBrief AssistantActionType = "write_brief"
	// AssistantAcceptBrief accepts the brief the brief writer proposed, which
	// makes the work order Ready. Commitarium proposes it from the helper's
	// result; the assistant never does.
	AssistantAcceptBrief AssistantActionType = "accept_brief"
	// AssistantDesignStack has the stack designer suggest the project's
	// stack from Description, or check the stack of an imported repository's
	// code (ADR-035).
	AssistantDesignStack AssistantActionType = "design_stack"
	// AssistantApplyStack applies the stack the stack designer suggested.
	// Commitarium proposes it from the helper's result; the assistant never
	// does.
	AssistantApplyStack AssistantActionType = "apply_stack"
	// AssistantResearch has a research helper look up Description, a
	// question of fact, on the web (ADR-035); Title names it in a few
	// words. Its findings and sources come back to the assistant.
	AssistantResearch AssistantActionType = "research"
	// AssistantUpdatePlan updates the vision being planned (planning mode):
	// Description is the goal, and Decisions, OpenQuestions, PlanItems,
	// StackPresets, and Frameworks the plan's other sections. Like research,
	// it is applied at once: the user watches the plan fill in.
	AssistantUpdatePlan AssistantActionType = "update_plan"
	// AssistantBuildPlan finishes the vision being planned and hands it to
	// autopilot, as the planning workspace's Build it does.
	AssistantBuildPlan AssistantActionType = "build_plan"
	// AssistantStartPlanning suggests plan mode in the chat that proposed it
	// (ADR-039), for a request too big or vague to build without planning:
	// Reason says why in a line, and Description, when set, is the goal of
	// the planning round. Applying it starts, resumes, or moves the
	// project's planning to that chat, as the chat's plan switch does.
	AssistantStartPlanning AssistantActionType = "start_planning"
)

// PlanItem is one item of the build plan at a priority, 0 for P0. Items,
// when any, are its sub-items: the slices of a feature planned as one item
// (PLAN_MODE_PLAN.md PM6), each a work order of its own.
type PlanItem struct {
	Priority    int           `json:"priority"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Items       []PlanSubItem `json:"items,omitempty"`
}

// PlanSubItem is a sub-item of a build plan's item: one slice.
type PlanSubItem struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

const (
	// MaxPlanDocumentBytes bounds the written plan (PLAN_MODE_PLAN.md PM6),
	// and MaxPlanDocumentPathBytes the path it is committed at.
	MaxPlanDocumentBytes     = 32 * 1024
	MaxPlanDocumentPathBytes = 200
	maxPlanSubItems          = 20
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
	// Steps is "full", "reviewed_plan", "planned", or "quick" (ADR-018,
	// ADR-031).
	Steps string `json:"steps"`
	// IndependentTests is ignored: independent acceptance tests are no
	// longer written (ADR-031). Kept so older stored actions still decode.
	IndependentTests bool `json:"independent_tests,omitempty"`
	// Agents and models, for create, start, and change: the assistant's
	// choice (ADR-027). Empty keeps the work order's own choice or the run's
	// current one; a project has no defaults.
	LeadAgent     string `json:"lead_agent"`
	LeadModel     string `json:"lead_model"`
	ReviewerAgent string `json:"reviewer_agent"`
	ReviewerModel string `json:"reviewer_model"`
	// Planning models and efforts by phase, for create and start (ADR-031).
	// Empty keeps the role's model and the model's default effort.
	LeadPlanModel      string `json:"lead_plan_model"`
	LeadPlanEffort     string `json:"lead_plan_effort"`
	LeadEffort         string `json:"lead_effort"`
	ReviewerPlanModel  string `json:"reviewer_plan_model"`
	ReviewerPlanEffort string `json:"reviewer_plan_effort"`
	ReviewerEffort     string `json:"reviewer_effort"`
	// start_work_order, change_models, and add_vision_item (optional)
	WorkOrderID string `json:"work_order_id"`
	// Priority is the level of a new vision item, or of the priority an
	// autopilot goal finishes: 0 for P0, 1 for P1, and so on.
	Priority int `json:"priority"`
	// When is "now" or "next_turn", for change_models.
	When string `json:"when"`
	// How, for recover_work_order: "recheck", "continue", "message",
	// "fresh_conversation", or "back_to_ready" (ADR-032).
	How string `json:"how"`
	// Guidance is what continue_work_order tells the agents; empty retries.
	// For set_up_preview it is what fails, if the assistant knows.
	Guidance string `json:"guidance"`
	// After, for a work order to create or start, names work it must follow:
	// a work order's ID, or the title of another of the assistant's actions.
	// Autopilot starts it only once that work has merged: it needs that
	// work's code or result (ADR-036).
	After string `json:"after"`
	// Helper, for answer_helper, names the helper that asked; for a card
	// from a helper's result, the helper that proposed it.
	Helper string `json:"helper"`
	// Brief, for create_work_order, has the new work order's brief written
	// from Description before it starts (ADR-035): it is created as a draft.
	Brief bool `json:"brief,omitempty"`
	// AppBrief, for create_app and plan_project, is what the assistant gathered with the user
	// (what the app is for, decisions, research with sources, what is left
	// out), handed in full to the writer who plans the app; Assumptions are
	// what it guessed, shown on the card.
	AppBrief    string   `json:"app_brief,omitempty"`
	Assumptions []string `json:"assumptions,omitempty"`
	// Attachments, for create_work_order, name the files the user attached
	// that its agents need; the work order's goal names them, so the lead
	// sees them.
	Attachments []string `json:"attachments,omitempty"`
	// Decisions and OpenQuestions, for update_plan, replace the plan's lists
	// in full; PlanItems, when any, replace its build plan, and
	// StackPresets, when any, with Frameworks, its stack.
	Decisions     []string   `json:"decisions,omitempty"`
	OpenQuestions []string   `json:"open_questions,omitempty"`
	PlanItems     []PlanItem `json:"plan_items,omitempty"`
	StackPresets  []string   `json:"stack_presets,omitempty"`
	Frameworks    string     `json:"frameworks,omitempty"`
	// PlanStep, for update_plan, is the planning checklist key the
	// conversation is mostly on, or review; PlanCovered, when any, are the
	// checklist keys the plan covers.
	PlanStep    string   `json:"plan_step,omitempty"`
	PlanCovered []string `json:"plan_covered,omitempty"`
	// PlanDocument, for update_plan, is the written plan in markdown (the
	// draft, or its revision) with its slices as a numbered checklist;
	// empty keeps it (PLAN_MODE_PLAN.md PM6).
	PlanDocument string `json:"plan_document,omitempty"`
	// Domain, Name and Aliases, for the server's cards: the domain a card
	// acts on, and for deploy_app the address it proposes (the empty Name is
	// the domain itself) with its extra addresses.
	Domain  string       `json:"domain,omitempty"`
	Name    string       `json:"name,omitempty"`
	Aliases []AppAddress `json:"aliases,omitempty"`
	// App, for set_app_access, is the app's main address, such as
	// notes.example.com; empty is this project's app. Groups are who may
	// open it with company sign-in; none is anyone at the company.
	App    string   `json:"app,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// AppAddress is an address under one of the server's domains: Name is empty
// for the domain itself.
type AppAddress struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
}

const (
	maxDomainBytes  = 253
	maxNameBytes    = 63
	maxAppAliases   = 10
	maxAccessGroups = 20
)

// ServerCard reports a card the user's app applies through the server's
// host before it is recorded, with what the host answered as its result.
func ServerCard(kind AssistantActionType) bool {
	switch kind {
	case AssistantAddDomain, AssistantInstallAuthentik, AssistantSetAppAccess, AssistantSetHomelab:
		return true
	}
	return false
}

// AnyChat reports a card about the user's server, which a chat outside any
// project may carry too, such as one on serving apps.
func AnyChat(kind AssistantActionType) bool {
	return ServerCard(kind) || kind == AssistantSetAppsPage
}

// validDomain checks a domain's shape loosely: the host checks it for real.
func validDomain(domain string) bool {
	return domain != "" && len(domain) <= maxDomainBytes && strings.Contains(domain, ".") &&
		!strings.ContainsAny(domain, " /:@*")
}

// validName checks an address name's shape loosely; empty is the domain
// itself.
func validName(name string) bool {
	return len(name) <= maxNameBytes && !strings.ContainsAny(name, " ./:@*")
}

// PhaseChoices are the action's planning models and efforts (ADR-031); nil
// when it names none.
func (action AssistantAction) PhaseChoices() *project.PhaseChoices {
	choices := project.PhaseChoices{
		LeadPlanModel: action.LeadPlanModel, LeadPlanEffort: action.LeadPlanEffort, LeadEffort: action.LeadEffort,
		ReviewerPlanModel: action.ReviewerPlanModel, ReviewerPlanEffort: action.ReviewerPlanEffort,
		ReviewerEffort: action.ReviewerEffort,
	}
	if choices == (project.PhaseChoices{}) {
		return nil
	}
	return &choices
}

const (
	// MaxAssistantActions bounds a turn's actions: enough to create and
	// start a whole wave of work orders at once.
	MaxAssistantActions   = 32
	MaxAssistantNoteBytes = 16 * 1024
	// MaxChatTitleBytes bounds the title an assistant turn gives its chat,
	// which the coordinator shortens to fit the chat list.
	MaxChatTitleBytes = 1024
	// MaxAppBriefBytes bounds a new app's brief, so it fits the new app's
	// notes with a line that introduces it.
	MaxAppBriefBytes = 15 * 1024
	// MaxAssistantQuestions bounds a turn's questions, and
	// Min/MaxQuestionOptions a question's suggested answers.
	MaxAssistantQuestions = 3
	MinQuestionOptions    = 2
	MaxQuestionOptions    = 4
	maxQuestionBytes      = 1024
	maxOptionBytes        = 200
	maxAssumptions        = 20
	maxAttachments        = 10
	maxPlanItems          = 60
	maxPlanLines          = 40
)

// Normalize trims the turn and checks its actions.
func (turn AssistantTurn) Normalize() (AssistantTurn, error) {
	turn.Notes = strings.TrimSpace(turn.Notes)
	turn.ChatTitle = strings.Join(strings.Fields(turn.ChatTitle), " ")
	if len(turn.ChatTitle) > MaxChatTitleBytes {
		return AssistantTurn{}, fmt.Errorf("%w: the chat's title is too long", ErrInvalidArtifact)
	}
	if len(turn.Notes) > MaxAssistantNoteBytes {
		return AssistantTurn{}, fmt.Errorf("%w: the assistant's notes are too long", ErrInvalidArtifact)
	}
	turn.Questions = NormalizeQuestions(turn.Questions)
	if len(turn.Actions) > MaxAssistantActions {
		return AssistantTurn{}, fmt.Errorf("%w: at most %d actions per turn", ErrInvalidArtifact, MaxAssistantActions)
	}
	for index := range turn.Actions {
		action := &turn.Actions[index]
		for _, field := range []*string{
			&action.Reason, &action.Title, &action.Description, &action.VisionItemID, &action.Steps,
			&action.LeadAgent, &action.LeadModel, &action.ReviewerAgent, &action.ReviewerModel,
			&action.LeadPlanModel, &action.LeadPlanEffort, &action.LeadEffort,
			&action.ReviewerPlanModel, &action.ReviewerPlanEffort, &action.ReviewerEffort,
			&action.WorkOrderID, &action.When, &action.How, &action.Guidance, &action.After, &action.Helper,
			&action.AppBrief, &action.Frameworks, &action.PlanStep, &action.PlanDocument, &action.App,
		} {
			*field = strings.TrimSpace(*field)
		}
		// Addresses are lower case, as the host keeps them.
		for _, field := range []*string{&action.Domain, &action.Name} {
			*field = strings.ToLower(strings.TrimSpace(*field))
		}
		for index := range action.Aliases {
			alias := &action.Aliases[index]
			alias.Domain = strings.ToLower(strings.TrimSpace(alias.Domain))
			alias.Name = strings.ToLower(strings.TrimSpace(alias.Name))
		}
		for _, list := range []*[]string{
			&action.Assumptions, &action.Decisions, &action.OpenQuestions, &action.StackPresets, &action.Attachments,
			&action.PlanCovered, &action.Groups,
		} {
			kept := (*list)[:0:0]
			for _, line := range *list {
				if line = strings.TrimSpace(line); line != "" {
					kept = append(kept, line)
				}
			}
			*list = kept
		}
		for index := range action.PlanItems {
			item := &action.PlanItems[index]
			item.Title, item.Description = strings.TrimSpace(item.Title), strings.TrimSpace(item.Description)
			for sub := range item.Items {
				item.Items[sub].Title = strings.TrimSpace(item.Items[sub].Title)
				item.Items[sub].Description = strings.TrimSpace(item.Items[sub].Description)
			}
		}
		if err := action.validate(); err != nil {
			return AssistantTurn{}, fmt.Errorf("%w: action %d: %v", ErrInvalidArtifact, index+1, err)
		}
	}
	return turn, nil
}

// NormalizeQuestions trims questions and keeps those that fit: a question
// without text or two answers, or too long, is left out rather than failing
// the turn, past four answers the rest are dropped, a suggestion out of
// range falls to the first answer, and past three questions the rest are
// dropped. A catch-all answer ("Something else", "Other") is left out: the
// card always offers the user their own.
func NormalizeQuestions(questions []AssistantQuestion) []AssistantQuestion {
	kept := make([]AssistantQuestion, 0, min(len(questions), MaxAssistantQuestions))
	for _, question := range questions {
		if len(kept) == MaxAssistantQuestions {
			break
		}
		question.Text = strings.Join(strings.Fields(question.Text), " ")
		if question.Text == "" || len(question.Text) > maxQuestionBytes {
			continue
		}
		options := make([]string, 0, MaxQuestionOptions)
		suggested := -1
		for index, option := range question.Options {
			option = strings.Join(strings.Fields(option), " ")
			if option == "" || len(option) > maxOptionBytes || slices.Contains(options, option) ||
				len(options) == MaxQuestionOptions || catchAll(option) {
				continue
			}
			if index == question.Suggested {
				suggested = len(options)
			}
			options = append(options, option)
		}
		if len(options) < MinQuestionOptions {
			continue
		}
		// A question about the user has no answer to suggest (-1); a choice
		// keeps one, the first when the one asked for is gone.
		if question.Suggested >= 0 {
			suggested = max(suggested, 0)
		}
		question.Options, question.Suggested = options, suggested
		kept = append(kept, question)
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// catchAll reports an answer that only stands for "none of these".
func catchAll(option string) bool {
	switch strings.TrimRight(strings.ToLower(option), ".…!?") {
	case "something else", "other", "others", "anything else", "none of these", "none of the above", "else":
		return true
	}
	return false
}

func (action AssistantAction) validate() error {
	if action.Reason == "" || len(action.Reason) > MaxStepTextBytes {
		return fmt.Errorf("a reason is required")
	}
	if len(action.After) > MaxStepTextBytes {
		return fmt.Errorf("after is too long")
	}
	switch action.Steps {
	case "", "full", "reviewed_plan", "planned", "quick":
	default:
		return fmt.Errorf("steps must be full, planned, or quick")
	}
	switch action.Type {
	case AssistantCreateWorkOrder:
		if action.Title == "" || len(action.Title) > MaxStepTextBytes || len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("a work order needs a title and a bounded description")
		}
		if len(action.Attachments) > maxAttachments {
			return fmt.Errorf("a work order carries at most %d attached files", maxAttachments)
		}
		for _, name := range action.Attachments {
			if len(name) > 100 || strings.ContainsAny(name, "/\\") {
				return fmt.Errorf("an attached file is named by its file name alone")
			}
		}
	case AssistantStartWorkOrder:
		if action.WorkOrderID == "" {
			return fmt.Errorf("start needs a work order")
		}
	case AssistantAddVisionItem:
		if action.Title == "" || len(action.Title) > MaxStepTextBytes || len(action.Description) > MaxStepTextBytes {
			return fmt.Errorf("a vision item needs a title and a short description")
		}
		if action.Priority < 0 || action.Priority > maxVisionPriority {
			return fmt.Errorf("priority runs from 0 to %d", maxVisionPriority)
		}
	case AssistantCreateApp, AssistantPlanProject:
		if action.Title == "" || len(action.Title) > 80 || action.Description == "" || len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("an app or a project needs a short name and a description")
		}
		if len(action.AppBrief) > MaxAppBriefBytes || len(action.Assumptions) > maxAssumptions {
			return fmt.Errorf("the app's brief is too long, or it lists more than %d assumptions", maxAssumptions)
		}
		for _, assumption := range action.Assumptions {
			if len(assumption) > MaxStepTextBytes {
				return fmt.Errorf("an assumption is too long")
			}
		}
	case AssistantCloseWorkOrder:
		if action.WorkOrderID == "" {
			return fmt.Errorf("closing needs a work order")
		}
	case AssistantContinueWorkOrder:
		if action.WorkOrderID == "" || len(action.Guidance) > MaxGoalBytes {
			return fmt.Errorf("continuing needs a work order and bounded guidance")
		}
	case AssistantRecoverWorkOrder:
		if action.WorkOrderID == "" || len(action.Guidance) > MaxGoalBytes {
			return fmt.Errorf("recovering needs a work order and bounded guidance")
		}
		switch action.How {
		case "recheck", "continue", "fresh_conversation", "back_to_ready":
		case "message":
			if action.Guidance == "" {
				return fmt.Errorf("a message to the agents needs its text in guidance")
			}
		default:
			return fmt.Errorf("how must be recheck, continue, message, fresh_conversation, or back_to_ready")
		}
	case AssistantDeployApp:
		if action.Domain == "" && (action.Name != "" || len(action.Aliases) > 0) {
			return fmt.Errorf("an address needs its domain")
		}
		if action.Domain != "" && (!validDomain(action.Domain) || !validName(action.Name)) {
			return fmt.Errorf("the address is a name (or empty, for the domain itself) under a domain")
		}
		if len(action.Aliases) > maxAppAliases {
			return fmt.Errorf("an app has at most %d extra addresses", maxAppAliases)
		}
		for _, alias := range action.Aliases {
			if !validDomain(alias.Domain) || !validName(alias.Name) {
				return fmt.Errorf("each extra address is a name (or empty, for the domain itself) under a domain")
			}
		}
	case AssistantSetAppsPage:
		switch action.How {
		case "public", "private", "company", "off":
		default:
			return fmt.Errorf("how must be public, private, company, or off")
		}
		if action.Domain != "" && !validDomain(action.Domain) {
			return fmt.Errorf("the domain is a name such as example.com")
		}
	case AssistantAddDomain:
		if !validDomain(action.Domain) {
			return fmt.Errorf("adding a domain needs it, such as example.com")
		}
		if action.How != "" && action.How != "wildcard" && action.How != "each_name" {
			return fmt.Errorf("how must be wildcard or each_name")
		}
	case AssistantInstallAuthentik:
		if action.Domain != "" && !validDomain(action.Domain) {
			return fmt.Errorf("the domain is a name such as example.com")
		}
	case AssistantSetAppAccess:
		switch action.How {
		case "public", "private", "company":
		default:
			return fmt.Errorf("how must be public, private, or company")
		}
		if len(action.App) > maxDomainBytes || strings.ContainsAny(action.App, " /") {
			return fmt.Errorf("the app is named by its main address, such as notes.example.com")
		}
		if len(action.Groups) > maxAccessGroups || (len(action.Groups) > 0 && action.How != "company") {
			return fmt.Errorf("groups are for company sign-in, at most %d", maxAccessGroups)
		}
		for _, group := range action.Groups {
			if len(group) > 100 {
				return fmt.Errorf("a group's name is too long")
			}
		}
	case AssistantSetHomelab:
		if action.How != "on" && action.How != "off" {
			return fmt.Errorf("how must be on or off")
		}
	case AssistantListOnAppsPage:
		if action.How != "list" && action.How != "hide" {
			return fmt.Errorf("how must be list or hide")
		}
	case AssistantSetUpPreview:
		if len(action.Guidance) > MaxGoalBytes {
			return fmt.Errorf("the preview's guidance is too long")
		}
	case AssistantWriteVision:
		if action.Description == "" || len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("writing the vision needs a bounded description of the project")
		}
	case AssistantSetUpStack:
	case AssistantAnswerHelper:
		if action.Helper == "" || action.Guidance == "" || len(action.Guidance) > MaxGoalBytes {
			return fmt.Errorf("answering a helper needs the helper and a bounded answer in guidance")
		}
	case AssistantWriteBrief:
		if action.WorkOrderID == "" || action.Description == "" || len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("writing a brief needs a work order and a bounded description")
		}
	case AssistantDesignStack:
		if len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("the stack's description is too long")
		}
	case AssistantResearch:
		if action.Title == "" || len(action.Title) > MaxStepTextBytes ||
			action.Description == "" || len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("research needs a short title and the question in description")
		}
	case AssistantUpdatePlan:
		if len(action.Description) > MaxGoalBytes || len(action.Frameworks) > MaxStepTextBytes ||
			len(action.Decisions) > maxPlanLines || len(action.OpenQuestions) > maxPlanLines ||
			len(action.PlanItems) > maxPlanItems || len(action.StackPresets) > maxVisionStackPresets ||
			len(action.PlanDocument) > MaxPlanDocumentBytes {
			return fmt.Errorf("the plan's update is too long")
		}
		for _, line := range append(append([]string{}, action.Decisions...), action.OpenQuestions...) {
			if len(line) > MaxStepTextBytes {
				return fmt.Errorf("a decision or question is too long")
			}
		}
		for _, item := range action.PlanItems {
			if item.Title == "" || len(item.Title) > MaxStepTextBytes || len(item.Description) > MaxStepTextBytes ||
				item.Priority < 0 || item.Priority > maxVisionPriority {
				return fmt.Errorf("each item of the build plan needs a title and a priority from 0 to %d", maxVisionPriority)
			}
			if len(item.Items) > maxPlanSubItems {
				return fmt.Errorf("an item of the build plan has at most %d sub-items", maxPlanSubItems)
			}
			for _, sub := range item.Items {
				if sub.Title == "" || len(sub.Title) > MaxStepTextBytes || len(sub.Description) > MaxStepTextBytes {
					return fmt.Errorf("each sub-item of the build plan needs a title and a short description")
				}
			}
		}
	case AssistantBuildPlan:
	case AssistantStartPlanning:
		if len(action.Description) > MaxGoalBytes {
			return fmt.Errorf("the planning goal is too long")
		}
	case AssistantStartAutopilot:
		if action.Priority < 0 || action.Priority > maxVisionPriority {
			return fmt.Errorf("priority runs from 0 to %d", maxVisionPriority)
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
