package vision

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// Planning mode (ONE_CONVERSATION_PLAN.md phase 2): in Developer mode, a
// project can be planned with the assistant before anything is built. The
// vision is the plan being written: its statement is the goal, its stack and
// items the stack and build plan, and Planning holds the rest (decisions,
// open questions, and research notes with their sources). The assistant
// updates it as the conversation goes, and Build it finishes it; nothing is
// converted.

type PlanningStatus string

const (
	PlanningOpen PlanningStatus = "planning"
	// PlanningPaused is planning the user stopped: the plan is kept as it
	// is, nothing is built, and starting planning again resumes it.
	PlanningPaused PlanningStatus = "paused"
	PlanningBuilt  PlanningStatus = "built"
)

// ErrNotPlanning is returned for a planning change to a vision that is not
// being planned.
var ErrNotPlanning = errors.New("the vision is not being planned")

// ErrNoDraft is returned for Build it before the plan is written: planning
// ends with a written plan and its slices (PLAN_MODE_PLAN.md PM6).
var ErrNoDraft = errors.New("the plan has no written draft yet: write the plan first")

// PlanningChecklist is what a good plan covers (ADR-039): a checklist, not
// an order. The assistant marks the keys the plan covers as it goes, and
// decisions and open questions start with their key and a colon ("look:
// dark theme") so the plan can show each under it.
var PlanningChecklist = []string{"purpose", "features", "data", "look", "details", "priorities", "stack"}

// RoundChecklist is what a next round's plan covers: what the round is for
// rather than the app's purpose, and no stack, which is settled.
var RoundChecklist = []string{"round", "features", "data", "look", "details", "priorities"}

// ReviewStep is the step once everything is covered: the plan, ready to
// build.
const ReviewStep = "review"

type Planning struct {
	Status PlanningStatus `json:"status"`
	// Step is the checklist key the conversation is mostly on, or review;
	// empty before the assistant has named one.
	Step string `json:"step,omitempty"`
	// Covered are the checklist keys the plan covers, in checklist order;
	// absent on plans from before the checklist.
	Covered   []string       `json:"covered,omitempty"`
	Decisions []string       `json:"decisions"`
	Questions []string       `json:"open_questions"`
	Research  []ResearchNote `json:"research"`
	StartedAt time.Time      `json:"started_at"`
	BuiltAt   *time.Time     `json:"built_at,omitempty"`
	// Chat is the assistant chat the planning is in (ADR-039): only its
	// turns plan. Empty, as planning started before chats had it, every
	// chat of the project plans.
	Chat string `json:"chat,omitempty"`
	// Round marks the planning of a next round: the vision had built work
	// when it started, so its checklist is RoundChecklist, the items it adds go in
	// from RoundPriority on, and the built items stay as they are.
	Round         bool `json:"round,omitempty"`
	RoundPriority int  `json:"round_priority,omitempty"`
	// Phase is where the planning stands (PLAN_MODE_PLAN.md PM6): talking
	// (empty) until the assistant writes the plan's draft, then drafted,
	// while the user reviews it. Build it needs a draft.
	Phase PlanningPhase `json:"phase,omitempty"`
	// Document is the written plan, in markdown: the goal, decisions with
	// their reasons, design notes, and the slices as a numbered checklist.
	// DocumentPath is where in the repository its first slice commits it,
	// such as docs/plans/sharing.md; set with the first draft, it stays.
	Document     string `json:"document,omitempty"`
	DocumentPath string `json:"document_path,omitempty"`
	// Before is the vision as the session found it, which Discard puts
	// back (plan_records.go); absent on sessions from before it.
	Before *PlanningSnapshot `json:"before,omitempty"`
}

// PlanningPhase is where a planning stands: talking, or drafted once the
// written plan is there to review.
type PlanningPhase string

const (
	PhaseTalking PlanningPhase = ""
	PhaseDrafted PlanningPhase = "drafted"
)

// Drafted reports whether the planning has its written plan.
func (planning *Planning) Drafted() bool {
	return planning != nil && planning.Phase == PhaseDrafted && planning.Document != ""
}

// Checklist is what the planning's plan covers.
func (planning *Planning) Checklist() []string {
	if planning != nil && planning.Round {
		return RoundChecklist
	}
	return PlanningChecklist
}

// ResearchNote is what a research helper found, with its sources.
type ResearchNote struct {
	Title    string    `json:"title"`
	Findings string    `json:"findings"`
	Sources  []string  `json:"sources"`
	AddedAt  time.Time `json:"added_at"`
}

// PlanningUpdate is the assistant's change to a vision being planned.
// Decisions and Questions replace the lists there are; an empty Goal, a nil
// Stack, and no Items keep what there is.
type PlanningUpdate struct {
	Goal string `json:"goal"`
	// Step, when set, is the checklist key the conversation moved to, or
	// review. Covered, when set, replaces the keys the plan covers. Keys
	// not on the planning's checklist are ignored.
	Step      string                       `json:"step,omitempty"`
	Covered   []string                     `json:"covered,omitempty"`
	Decisions []string                     `json:"decisions"`
	Questions []string                     `json:"open_questions"`
	Stack     *featureartifact.VisionStack `json:"stack,omitempty"`
	Items     []featureartifact.PlanItem   `json:"items"`
	// Document, when set, is the written plan: the first draft, or its
	// revision in full. Empty keeps it.
	Document string `json:"document,omitempty"`
}

const (
	maxDecisions     = 40
	maxQuestions     = 20
	maxResearchNotes = 30
	maxFindingsBytes = 16 * 1024
	maxSources       = 20
)

// Open reports whether the vision is being planned.
func (planning *Planning) Open() bool {
	return planning != nil && planning.Status == PlanningOpen
}

// Paused reports whether the user stopped planning the vision.
func (planning *Planning) Paused() bool {
	return planning != nil && planning.Status == PlanningPaused
}

// StartPlanning puts the project's vision in planning in chat, or starts one
// from goal when it has none; paused planning resumes as it was. Planning
// open in another chat moves to chat, and an empty chat keeps the one it is
// in. started is false when it was already being planned in that chat.
func (service *Service) StartPlanning(ctx context.Context, projectID, goal, chat string) (Vision, bool, error) {
	// Reading first seeds a vision the repository carries.
	if _, err := service.Get(ctx, projectID); err != nil && !errors.Is(err, ErrNotFound) {
		return Vision{}, false, err
	}
	goal = strings.TrimSpace(goal)
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.read(projectID)
	existed := err == nil
	switch {
	case errors.Is(err, ErrNotFound):
		if goal == "" {
			return Vision{}, false, fmt.Errorf("%w: say what the project is for", ErrInvalid)
		}
		current = Vision{ProjectID: projectID, Source: "user", Plan: featureartifact.VisionPlan{
			Statement: goal, Confidence: "medium", Priorities: []featureartifact.VisionPriority{}, Guesses: []string{},
		}}
	case err != nil:
		return Vision{}, false, err
	case current.Planning.Open() && (chat == "" || chat == current.Planning.Chat):
		return current, false, nil
	case current.Planning.Open(), current.Planning.Paused():
		planning := *current.Planning
		planning.Status = PlanningOpen
		if chat != "" {
			planning.Chat = chat
		}
		current.Planning = &planning
		current.Revision++
		stored, err := service.write(current)
		return stored, err == nil, err
	}
	// A built planning from before plan records is kept as the first plan,
	// and the session keeps the vision as it finds it for Discard.
	archiveBuilt(&current)
	before := snapshotOf(current, existed)
	current.Planning = &Planning{
		Status: PlanningOpen, Decisions: []string{}, Questions: []string{}, Research: []ResearchNote{},
		StartedAt: service.now(), Chat: chat, Before: before,
	}
	if built(current.Plan) {
		current.Planning.Round, current.Planning.RoundPriority = true, nextPriority(current.Plan)
	}
	current.Revision++
	stored, err := service.write(current)
	return stored, err == nil, err
}

// UpdatePlanning applies the assistant's change to a vision being planned.
func (service *Service) UpdatePlanning(ctx context.Context, projectID string, update PlanningUpdate) (Vision, error) {
	if _, err := service.projects.GetByID(ctx, projectID); err != nil {
		return Vision{}, err
	}
	decisions, questions := trimmed(update.Decisions), trimmed(update.Questions)
	step := strings.ToLower(strings.TrimSpace(update.Step))
	document := strings.TrimSpace(update.Document)
	if len(document) > featureartifact.MaxPlanDocumentBytes {
		return Vision{}, fmt.Errorf("%w: the written plan is too long", ErrInvalid)
	}
	if len(decisions) > maxDecisions || len(questions) > maxQuestions {
		return Vision{}, fmt.Errorf("%w: at most %d decisions and %d open questions", ErrInvalid, maxDecisions, maxQuestions)
	}
	for _, text := range append(append([]string{}, decisions...), questions...) {
		if len(text) > featureartifact.MaxStepTextBytes {
			return Vision{}, fmt.Errorf("%w: a decision or question is too long", ErrInvalid)
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.read(projectID)
	if err != nil {
		return Vision{}, err
	}
	if !current.Planning.Open() {
		return Vision{}, ErrNotPlanning
	}
	checklist := current.Planning.Checklist()
	if step != ReviewStep && !slices.Contains(checklist, step) {
		step = ""
	}
	plan := current.Plan
	if goal := strings.TrimSpace(update.Goal); goal != "" {
		plan.Statement = goal
	}
	planning := *current.Planning
	if document != "" {
		// The draft, or its revision: the planning is drafted, and its path
		// is set with the first draft.
		planning.Document, planning.Phase = document, PhaseDrafted
		if planning.DocumentPath == "" {
			planning.DocumentPath = documentPath(document, plan)
		}
	}
	if update.Stack != nil {
		if plan.Stack = knownStack(update.Stack); plan.Stack == nil {
			return Vision{}, fmt.Errorf("%w: the stack names none of these runtime presets: %s", ErrInvalid, presetList())
		}
	}
	if len(update.Items) > 0 {
		priorities, err := plannedPriorities(plan, update.Items, &planning)
		if err != nil {
			return Vision{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		keepWorkOrders(priorities, current.Plan)
		plan.Priorities = priorities
	}
	plan.Confidence = "high"
	if len(questions) > 0 {
		plan.Confidence = "medium"
	}
	if planning.DocumentPath != "" {
		stampDocument(plan.Priorities, planning.DocumentPath)
	}
	planning.Decisions, planning.Questions = decisions, questions
	if step != "" {
		planning.Step = step
	}
	if update.Covered != nil {
		planning.Covered = coveredKeys(checklist, update.Covered)
	}
	current.Plan, current.Planning, current.Source = plan, &planning, "assistant"
	current.Revision++
	return service.write(current)
}

// PausePlanning stops planning the vision: the plan is kept as it is and
// nothing is built until planning resumes.
func (service *Service) PausePlanning(ctx context.Context, projectID string) (Vision, error) {
	if _, err := service.projects.GetByID(ctx, projectID); err != nil {
		return Vision{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.read(projectID)
	if err != nil {
		return Vision{}, err
	}
	if current.Planning.Paused() {
		return current, nil
	}
	if !current.Planning.Open() {
		return Vision{}, ErrNotPlanning
	}
	planning := *current.Planning
	planning.Status = PlanningPaused
	current.Planning = &planning
	current.Revision++
	return service.write(current)
}

// plannedPriorities builds the plan's priorities from the build plan's
// items, with IDs derived from their titles. In a round, the built items
// stay as they are, whatever the items say of them, and the others go in at
// the round's priority or after it.
func plannedPriorities(
	plan featureartifact.VisionPlan, items []featureartifact.PlanItem, planning *Planning,
) ([]featureartifact.VisionPriority, error) {
	kept := map[string]bool{}
	if planning.Round {
		plan.Priorities = builtItems(plan)
		for _, priority := range plan.Priorities {
			for _, item := range priority.Items {
				kept[strings.ToLower(item.Title)] = true
			}
		}
	} else {
		plan.Priorities = []featureartifact.VisionPriority{}
	}
	for _, item := range items {
		if kept[strings.ToLower(strings.TrimSpace(item.Title))] {
			continue
		}
		level := item.Priority
		if planning.Round {
			level = max(level, planning.RoundPriority)
		}
		var err error
		if plan, _, err = plan.AddPlannedItem(level, item, planning.DocumentPath); err != nil {
			return nil, err
		}
	}
	return plan.Priorities, nil
}

// built reports whether the plan has built work: an item done, or with a
// work order of its own.
func built(plan featureartifact.VisionPlan) bool {
	for _, priority := range plan.Priorities {
		for _, item := range priority.Items {
			if itemBuilt(item) {
				return true
			}
		}
	}
	return false
}

// itemBuilt reports whether an item, or one of its sub-items, is done or
// has a work order.
func itemBuilt(item featureartifact.VisionItem) bool {
	if item.Done || item.FeatureID != "" {
		return true
	}
	for _, sub := range item.Items {
		if itemBuilt(sub) {
			return true
		}
	}
	return false
}

// builtItems are the plan's priorities with only their built items.
func builtItems(plan featureartifact.VisionPlan) []featureartifact.VisionPriority {
	priorities := []featureartifact.VisionPriority{}
	for _, priority := range plan.Priorities {
		items := []featureartifact.VisionItem{}
		for _, item := range priority.Items {
			if itemBuilt(item) {
				items = append(items, item)
			}
		}
		if len(items) > 0 {
			priorities = append(priorities, featureartifact.VisionPriority{Level: priority.Level, Items: items})
		}
	}
	return priorities
}

// nextPriority is the priority after the plan's highest one, where a
// round's items go.
func nextPriority(plan featureartifact.VisionPlan) int {
	next := 0
	for _, priority := range plan.Priorities {
		if len(priority.Items) > 0 {
			next = max(next, priority.Level+1)
		}
	}
	return min(next, 9) // P9 is the last priority there is
}

// AddResearch records what a research helper found on a vision being
// planned, with the sources its findings name. A vision not being planned
// is left as it is.
func (service *Service) AddResearch(ctx context.Context, projectID, title, findings string) error {
	if _, err := service.projects.GetByID(ctx, projectID); err != nil {
		return err
	}
	title, findings = strings.TrimSpace(title), strings.TrimSpace(findings)
	if title == "" || findings == "" {
		return ErrInvalid
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.read(projectID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !current.Planning.Open() {
		return nil
	}
	sources := Sources(findings)
	if len(findings) > maxFindingsBytes {
		findings = findings[:maxFindingsBytes] + "…"
	}
	planning := *current.Planning
	planning.Research = append(append([]ResearchNote{}, planning.Research...),
		ResearchNote{Title: title, Findings: findings, Sources: sources, AddedAt: service.now()})
	if len(planning.Research) > maxResearchNotes {
		planning.Research = planning.Research[len(planning.Research)-maxResearchNotes:]
	}
	current.Planning = &planning
	current.Revision++
	_, err = service.write(current)
	return err
}

// FinishPlanning ends planning when the user presses Build it: the vision
// is the plan as it stands, and what is still open becomes its guesses.
func (service *Service) FinishPlanning(ctx context.Context, projectID string) (Vision, error) {
	if _, err := service.projects.GetByID(ctx, projectID); err != nil {
		return Vision{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.read(projectID)
	if err != nil {
		return Vision{}, err
	}
	if !current.Planning.Open() {
		return Vision{}, ErrNotPlanning
	}
	if !current.Planning.Drafted() {
		return Vision{}, fmt.Errorf("%w: %w", ErrInvalid, ErrNoDraft)
	}
	if !hasItems(current.Plan) {
		return Vision{}, fmt.Errorf("%w: the build plan has no items yet", ErrInvalid)
	}
	now := service.now()
	planning := *current.Planning
	planning.Status, planning.BuiltAt, planning.Before = PlanningBuilt, &now, nil
	current.Planning = &planning
	// The session is kept as a plan of its own.
	current.Plans = append(append([]PlanRecord{}, current.Plans...),
		recordOf(planning, current.Plan, fmt.Sprintf("plan-%d", len(current.Plans)+1)))
	current.Plan.Guesses = append([]string{}, planning.Questions...)
	if len(current.Plan.Guesses) > featureartifact.MaxBriefItems {
		current.Plan.Guesses = current.Plan.Guesses[:featureartifact.MaxBriefItems]
	}
	current.Revision++
	return service.write(current)
}

func hasItems(plan featureartifact.VisionPlan) bool {
	for _, priority := range plan.Priorities {
		if len(priority.Items) > 0 {
			return true
		}
	}
	return false
}

// coveredKeys are the checklist's keys that covered names, in checklist
// order.
func coveredKeys(checklist, covered []string) []string {
	keys := []string{}
	for _, key := range checklist {
		if slices.ContainsFunc(covered, func(named string) bool {
			return strings.EqualFold(strings.TrimSpace(named), key)
		}) {
			keys = append(keys, key)
		}
	}
	return keys
}

func trimmed(values []string) []string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			kept = append(kept, value)
		}
	}
	return kept
}

var urlPattern = regexp.MustCompile(`https?://[^\s)\]>"']+`)

// Sources are the URLs research findings name, those after its closing
// "Sources:" when it has one.
func Sources(findings string) []string {
	if index := strings.LastIndex(strings.ToLower(findings), "sources:"); index >= 0 {
		findings = findings[index:]
	}
	seen, sources := map[string]bool{}, []string{}
	for _, found := range urlPattern.FindAllString(findings, -1) {
		found = strings.TrimRight(found, ".,;:")
		if !seen[found] && len(sources) < maxSources {
			seen[found] = true
			sources = append(sources, found)
		}
	}
	return sources
}
