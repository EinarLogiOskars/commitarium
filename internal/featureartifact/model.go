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
)

type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepInProgress StepStatus = "in_progress"
	StepCompleted  StepStatus = "completed"
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

func (kind Kind) IsValid() bool {
	return kind == KindGoalDraft || kind == KindImplementationPlan
}

func (status StepStatus) IsValid() bool {
	return status == StepPending || status == StepInProgress || status == StepCompleted
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
	for index := range plan.Steps {
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
