package vision

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
	PlanningOpen  PlanningStatus = "planning"
	PlanningBuilt PlanningStatus = "built"
)

// ErrNotPlanning is returned for a planning change to a vision that is not
// being planned.
var ErrNotPlanning = errors.New("the vision is not being planned")

type Planning struct {
	Status    PlanningStatus `json:"status"`
	Decisions []string       `json:"decisions"`
	Questions []string       `json:"open_questions"`
	Research  []ResearchNote `json:"research"`
	StartedAt time.Time      `json:"started_at"`
	BuiltAt   *time.Time     `json:"built_at,omitempty"`
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
	Goal      string                       `json:"goal"`
	Decisions []string                     `json:"decisions"`
	Questions []string                     `json:"open_questions"`
	Stack     *featureartifact.VisionStack `json:"stack,omitempty"`
	Items     []featureartifact.PlanItem   `json:"items"`
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

// StartPlanning puts the project's vision in planning, or starts one from
// goal when it has none. started is false when it was already being planned.
func (service *Service) StartPlanning(ctx context.Context, projectID, goal string) (Vision, bool, error) {
	// Reading first seeds a vision the repository carries.
	if _, err := service.Get(ctx, projectID); err != nil && !errors.Is(err, ErrNotFound) {
		return Vision{}, false, err
	}
	goal = strings.TrimSpace(goal)
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.read(projectID)
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
	case current.Planning.Open():
		return current, false, nil
	}
	current.Planning = &Planning{
		Status: PlanningOpen, Decisions: []string{}, Questions: []string{}, Research: []ResearchNote{},
		StartedAt: service.now(),
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
	plan := current.Plan
	if goal := strings.TrimSpace(update.Goal); goal != "" {
		plan.Statement = goal
	}
	if update.Stack != nil {
		if plan.Stack = knownStack(update.Stack); plan.Stack == nil {
			return Vision{}, fmt.Errorf("%w: the stack names none of these runtime presets: %s", ErrInvalid, presetList())
		}
	}
	if len(update.Items) > 0 {
		priorities, err := plannedPriorities(plan, update.Items)
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
	planning := *current.Planning
	planning.Decisions, planning.Questions = decisions, questions
	current.Plan, current.Planning, current.Source = plan, &planning, "assistant"
	current.Revision++
	return service.write(current)
}

// plannedPriorities builds the plan's priorities from the build plan's
// items, with IDs derived from their titles.
func plannedPriorities(plan featureartifact.VisionPlan, items []featureartifact.PlanItem) ([]featureartifact.VisionPriority, error) {
	plan.Priorities = []featureartifact.VisionPriority{}
	for _, item := range items {
		var err error
		if plan, _, err = plan.AddItem(item.Priority, item.Title, item.Description, ""); err != nil {
			return nil, err
		}
	}
	return plan.Priorities, nil
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
	if !hasItems(current.Plan) {
		return Vision{}, fmt.Errorf("%w: the build plan has no items yet", ErrInvalid)
	}
	now := service.now()
	planning := *current.Planning
	planning.Status, planning.BuiltAt = PlanningBuilt, &now
	current.Planning = &planning
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
