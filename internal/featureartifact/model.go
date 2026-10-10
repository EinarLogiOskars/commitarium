package featureartifact

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Kind string

const (
	KindGoalDraft          Kind = "goal_draft"
	KindImplementationPlan Kind = "implementation_plan"
	KindAcceptanceTests    Kind = "acceptance_tests"
	KindHandoffBrief       Kind = "handoff_brief"
)

type StepStatus string

type AcceptanceTestStatus string

const (
	StepPending    StepStatus = "pending"
	StepInProgress StepStatus = "in_progress"
	StepCompleted  StepStatus = "completed"
)

const (
	AcceptanceTestPending       AcceptanceTestStatus = "pending"
	AcceptanceTestRunning       AcceptanceTestStatus = "running"
	AcceptanceTestPassed        AcceptanceTestStatus = "passed"
	AcceptanceTestFailed        AcceptanceTestStatus = "failed"
	AcceptanceTestNotApplicable AcceptanceTestStatus = "not_applicable"
)

const (
	MaxGoalBytes       = 64 * 1024
	MaxPlanSteps       = 100
	MaxStepTextBytes   = 32 * 1024
	MaxQuestionCount   = 50
	MaxVerificationSet = 50
)

var (
	ErrInvalidArtifact = errors.New("invalid feature artifact")
	stepIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	commitIDPattern    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

type GoalDraft struct {
	Goal          string   `json:"goal"`
	OpenQuestions []string `json:"open_questions"`
}

// HandoffBrief is a clarified work order: what to build and why, where in the
// code, and what is worth planning around. The commit-by-commit plan is left
// to the agents. BaseCommitID is the default-branch commit it was written
// against, so a later start can check what changed since.
type HandoffBrief struct {
	Goal           string   `json:"goal"`
	Areas          []string `json:"areas"`
	Considerations []string `json:"considerations"`
	OpenQuestions  []string `json:"open_questions"`
	BaseCommitID   string   `json:"base_commit_id"`
}

const MaxBriefItems = 50

func (brief HandoffBrief) Validate() error {
	if err := validRequiredText("goal", brief.Goal, MaxGoalBytes); err != nil {
		return err
	}
	for name, items := range map[string][]string{
		"area": brief.Areas, "consideration": brief.Considerations, "open question": brief.OpenQuestions,
	} {
		if len(items) > MaxBriefItems {
			return fmt.Errorf("%w: too many %ss", ErrInvalidArtifact, name)
		}
		for index, item := range items {
			if err := validRequiredText(fmt.Sprintf("%s %d", name, index+1), item, MaxStepTextBytes); err != nil {
				return err
			}
		}
	}
	if brief.BaseCommitID != "" && !commitIDPattern.MatchString(brief.BaseCommitID) {
		return fmt.Errorf("%w: handoff brief has an invalid base commit", ErrInvalidArtifact)
	}
	return nil
}

type ImplementationPlan struct {
	PlanVersion int                      `json:"plan_version"`
	Title       string                   `json:"title"`
	Subtitle    string                   `json:"subtitle"`
	Steps       []ImplementationPlanStep `json:"steps"`
}

type ImplementationPlanStep struct {
	ID              string     `json:"id"`
	Position        int        `json:"position"`
	Title           string     `json:"title"`
	Subtitle        string     `json:"subtitle"`
	DetailsMarkdown string     `json:"details_markdown"`
	Verification    []string   `json:"verification"`
	CommitSubject   string     `json:"commit_subject"`
	Status          StepStatus `json:"status"`
	CommitID        string     `json:"commit_id,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

// AcceptanceTests is deliberately presentation metadata, not executable test
// source. The private reviewer checkout remains the source of truth for the
// test commit and commands; this document powers the user-facing checklist.
type AcceptanceTests struct {
	PlanVersion            int              `json:"plan_version"`
	TestCommitID           string           `json:"test_commit_id"`
	ImplementationCommitID string           `json:"implementation_commit_id"`
	Tests                  []AcceptanceTest `json:"tests"`
}

type AcceptanceTest struct {
	ID       string               `json:"id"`
	Position int                  `json:"position"`
	Title    string               `json:"title"`
	Status   AcceptanceTestStatus `json:"status"`
	Note     string               `json:"note"`
}

func (kind Kind) IsValid() bool {
	return kind == KindGoalDraft || kind == KindImplementationPlan || kind == KindAcceptanceTests ||
		kind == KindHandoffBrief
}

func (status StepStatus) IsValid() bool {
	return status == StepPending || status == StepInProgress || status == StepCompleted
}

func (status AcceptanceTestStatus) IsValid() bool {
	switch status {
	case AcceptanceTestPending, AcceptanceTestRunning, AcceptanceTestPassed,
		AcceptanceTestFailed, AcceptanceTestNotApplicable:
		return true
	default:
		return false
	}
}

// NormalizeInitial turns reviewer-authored metadata into a coordinator-owned
// checklist. A reviewer cannot pre-declare a result before the implementation
// revision is available.
func (tests AcceptanceTests) NormalizeInitial(planVersion int) (AcceptanceTests, error) {
	tests.PlanVersion = planVersion
	tests.ImplementationCommitID = ""
	tests.Tests = append([]AcceptanceTest(nil), tests.Tests...)
	ids := make([]string, len(tests.Tests))
	for index := range tests.Tests {
		ids[index] = tests.Tests[index].ID
	}
	ids = normalizeIDs(ids, "test")
	for index := range tests.Tests {
		tests.Tests[index].ID = ids[index]
		tests.Tests[index].Position = index + 1
		tests.Tests[index].Status = AcceptanceTestPending
		tests.Tests[index].Note = ""
	}
	if err := tests.Validate(); err != nil {
		return AcceptanceTests{}, err
	}
	return tests, nil
}

func (tests AcceptanceTests) Validate() error {
	if tests.PlanVersion < 1 {
		return fmt.Errorf("%w: acceptance test plan version must be positive", ErrInvalidArtifact)
	}
	if !commitIDPattern.MatchString(tests.TestCommitID) {
		return fmt.Errorf("%w: acceptance tests require a valid private test commit", ErrInvalidArtifact)
	}
	if tests.ImplementationCommitID != "" && !commitIDPattern.MatchString(tests.ImplementationCommitID) {
		return fmt.Errorf("%w: acceptance tests have an invalid implementation commit", ErrInvalidArtifact)
	}
	if len(tests.Tests) == 0 || len(tests.Tests) > MaxPlanSteps {
		return fmt.Errorf("%w: acceptance tests must contain between 1 and %d entries", ErrInvalidArtifact, MaxPlanSteps)
	}
	seen := make(map[string]struct{}, len(tests.Tests))
	for index, test := range tests.Tests {
		if !stepIDPattern.MatchString(test.ID) {
			return fmt.Errorf("%w: acceptance test %d has an invalid ID", ErrInvalidArtifact, index+1)
		}
		if _, exists := seen[test.ID]; exists {
			return fmt.Errorf("%w: duplicate acceptance test ID %q", ErrInvalidArtifact, test.ID)
		}
		seen[test.ID] = struct{}{}
		if test.Position != index+1 {
			return fmt.Errorf("%w: acceptance test %q position must match its array order", ErrInvalidArtifact, test.ID)
		}
		if err := validRequiredText("acceptance test "+test.ID+" title", test.Title, MaxStepTextBytes); err != nil {
			return err
		}
		if !test.Status.IsValid() {
			return fmt.Errorf("%w: acceptance test %q has invalid status %q", ErrInvalidArtifact, test.ID, test.Status)
		}
		if test.Note != strings.TrimSpace(test.Note) || len([]byte(test.Note)) > MaxStepTextBytes {
			return fmt.Errorf("%w: acceptance test %q note is invalid", ErrInvalidArtifact, test.ID)
		}
		if (test.Status == AcceptanceTestFailed || test.Status == AcceptanceTestNotApplicable) && test.Note == "" {
			return fmt.Errorf("%w: acceptance test %q requires a note for status %q", ErrInvalidArtifact, test.ID, test.Status)
		}
		if test.Status != AcceptanceTestFailed && test.Status != AcceptanceTestNotApplicable && test.Note != "" {
			return fmt.Errorf("%w: acceptance test %q cannot have a note for status %q", ErrInvalidArtifact, test.ID, test.Status)
		}
		if tests.ImplementationCommitID == "" && test.Status != AcceptanceTestPending {
			return fmt.Errorf("%w: acceptance test %q cannot run before an implementation commit is pinned", ErrInvalidArtifact, test.ID)
		}
	}
	return nil
}

func (draft GoalDraft) Validate() error {
	if err := validRequiredText("goal", draft.Goal, MaxGoalBytes); err != nil {
		return err
	}
	if len(draft.OpenQuestions) > MaxQuestionCount {
		return fmt.Errorf("%w: too many open questions", ErrInvalidArtifact)
	}
	for index, question := range draft.OpenQuestions {
		if err := validRequiredText(fmt.Sprintf("open question %d", index+1), question, MaxStepTextBytes); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeInitial turns a provider-submitted plan into the coordinator-owned
// initial checklist. Providers do not choose progress or commit identities.
func (plan ImplementationPlan) NormalizeInitial(planVersion int) (ImplementationPlan, error) {
	plan.PlanVersion = planVersion
	plan.Steps = append([]ImplementationPlanStep(nil), plan.Steps...)
	ids := make([]string, len(plan.Steps))
	for index := range plan.Steps {
		ids[index] = plan.Steps[index].ID
	}
	ids = normalizeIDs(ids, "step")
	for index := range plan.Steps {
		plan.Steps[index].ID = ids[index]
		plan.Steps[index].Position = index + 1
		plan.Steps[index].Status = StepPending
		plan.Steps[index].CommitID = ""
		plan.Steps[index].CompletedAt = nil
	}
	if err := plan.Validate(); err != nil {
		return ImplementationPlan{}, err
	}
	return plan, nil
}

func (plan ImplementationPlan) Validate() error {
	if plan.PlanVersion < 1 {
		return fmt.Errorf("%w: plan version must be positive", ErrInvalidArtifact)
	}
	if err := validRequiredText("plan title", plan.Title, MaxStepTextBytes); err != nil {
		return err
	}
	if err := validRequiredText("plan subtitle", plan.Subtitle, MaxStepTextBytes); err != nil {
		return err
	}
	if len(plan.Steps) == 0 || len(plan.Steps) > MaxPlanSteps {
		return fmt.Errorf("%w: plan must contain between 1 and %d steps", ErrInvalidArtifact, MaxPlanSteps)
	}
	seen := make(map[string]struct{}, len(plan.Steps))
	inProgress := 0
	completedPrefix := true
	for index, step := range plan.Steps {
		if !stepIDPattern.MatchString(step.ID) {
			return fmt.Errorf("%w: step %d has an invalid ID", ErrInvalidArtifact, index+1)
		}
		if _, exists := seen[step.ID]; exists {
			return fmt.Errorf("%w: duplicate step ID %q", ErrInvalidArtifact, step.ID)
		}
		seen[step.ID] = struct{}{}
		if step.Position != index+1 {
			return fmt.Errorf("%w: step %q position must match its array order", ErrInvalidArtifact, step.ID)
		}
		for label, value := range map[string]string{
			"title": step.Title, "subtitle": step.Subtitle,
			"details": step.DetailsMarkdown, "commit subject": step.CommitSubject,
		} {
			if err := validRequiredText("step "+step.ID+" "+label, value, MaxStepTextBytes); err != nil {
				return err
			}
		}
		if len(step.Verification) == 0 || len(step.Verification) > MaxVerificationSet {
			return fmt.Errorf("%w: step %q must contain verification commands or checks", ErrInvalidArtifact, step.ID)
		}
		for checkIndex, check := range step.Verification {
			if err := validRequiredText(fmt.Sprintf("step %s verification %d", step.ID, checkIndex+1), check, MaxStepTextBytes); err != nil {
				return err
			}
		}
		if !step.Status.IsValid() {
			return fmt.Errorf("%w: step %q has invalid status %q", ErrInvalidArtifact, step.ID, step.Status)
		}
		switch step.Status {
		case StepPending:
			completedPrefix = false
			if step.CommitID != "" || step.CompletedAt != nil {
				return fmt.Errorf("%w: pending step %q cannot have completion data", ErrInvalidArtifact, step.ID)
			}
		case StepInProgress:
			inProgress++
			completedPrefix = false
			if step.CommitID != "" || step.CompletedAt != nil {
				return fmt.Errorf("%w: active step %q cannot have completion data", ErrInvalidArtifact, step.ID)
			}
		case StepCompleted:
			if !completedPrefix {
				return fmt.Errorf("%w: completed steps must form an ordered prefix", ErrInvalidArtifact)
			}
			if !commitIDPattern.MatchString(step.CommitID) || step.CompletedAt == nil || step.CompletedAt.IsZero() {
				return fmt.Errorf("%w: completed step %q requires a valid commit and time", ErrInvalidArtifact, step.ID)
			}
		}
	}
	if inProgress > 1 {
		return fmt.Errorf("%w: only one plan step may be in progress", ErrInvalidArtifact)
	}
	return nil
}

func (plan ImplementationPlan) Complete() bool {
	if len(plan.Steps) == 0 {
		return false
	}
	for _, step := range plan.Steps {
		if step.Status != StepCompleted {
			return false
		}
	}
	return true
}

func validRequiredText(label, value string, maximum int) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s must be non-empty and trimmed", ErrInvalidArtifact, label)
	}
	if len([]byte(value)) > maximum {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidArtifact, label, maximum)
	}
	return nil
}

// normalizeIDs turns agent-chosen IDs into valid, unique checklist IDs instead
// of failing a whole turn over formatting: "Docker Compose: start" becomes
// "docker-compose-start". Valid unique IDs are kept unchanged.
func normalizeIDs(ids []string, fallback string) []string {
	normalized := make([]string, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for index, id := range ids {
		var builder strings.Builder
		dash := false
		for _, r := range strings.ToLower(strings.TrimSpace(id)) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_':
				builder.WriteRune(r)
				dash = false
			case !dash && builder.Len() > 0:
				builder.WriteByte('-')
				dash = true
			}
		}
		candidate := strings.Trim(builder.String(), "-._")
		if len(candidate) > 56 {
			candidate = strings.Trim(candidate[:56], "-._")
		}
		if candidate == "" {
			candidate = fmt.Sprintf("%s-%d", fallback, index+1)
		}
		unique := candidate
		for suffix := 2; ; suffix++ {
			if _, taken := seen[unique]; !taken {
				break
			}
			unique = fmt.Sprintf("%s-%d", candidate, suffix)
		}
		seen[unique] = struct{}{}
		normalized[index] = unique
	}
	return normalized
}
