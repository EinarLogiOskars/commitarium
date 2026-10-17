package feature

import (
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type Feature struct {
	ID                         string
	ProjectID                  string
	Title                      string
	Description                string
	State                      State
	AcceptedGoal               string
	GoalAcceptedAt             *time.Time
	DialogueLimits             project.DialogueLimits
	AgentProviders             project.AgentProviders
	AgentModels                project.AgentModels
	MergePolicy                project.MergePolicy
	AutonomyPolicy             project.AutonomyPolicy
	IndependentAcceptanceTests bool
	Steps                      project.WorkSteps
	Kind                       Kind
	// Background marks a work order Commitarium started by itself, such as
	// the preview setup; it is not listed with the user's work.
	Background bool
	// CreatedBy is who created it: the assistant acts on its own only on its
	// own work (ADR-027).
	CreatedBy Creator
	// PreviewCheck makes the work order merge only once Commitarium's
	// preview check of its exact commit passed (ADR-028).
	PreviewCheck bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Creator is who created a work order: the user by hand, or the assistant
// (its actions, and Commitarium's own setup work, which runs on the
// assistant's agent).
type Creator string

const (
	CreatedByUser      Creator = "user"
	CreatedByAssistant Creator = "assistant"
)

// Kind is the kind of work: a work order that runs on its own, or an
// interactive session with one agent (ADR-019).
type Kind string

const (
	KindWorkOrder Kind = "work_order"
	KindSession   Kind = "session"
)

func (kind Kind) IsValid() bool {
	return kind == KindWorkOrder || kind == KindSession
}

// SettingsOverrides contains the optional setting categories supplied when a
// work order is created. A nil category inherits the project's current value.
type SettingsOverrides struct {
	DialogueLimits             *project.DialogueLimits
	AgentProviders             *project.AgentProviders
	AgentModels                *project.AgentModels
	MergePolicy                *project.MergePolicy
	AutonomyPolicy             *project.AutonomyPolicy
	IndependentAcceptanceTests *bool
	Steps                      *project.WorkSteps
	// PhaseChoices are the optional planning models and efforts (ADR-031).
	PhaseChoices *project.PhaseChoices
	// Kind is the kind of work to create; empty is a work order. A session
	// has neither a plan nor a reviewer.
	Kind Kind
	// Background creates a work order Commitarium runs by itself.
	Background bool
	// CreatedBy is who creates it; empty is the user.
	CreatedBy Creator
	// PreviewCheck checks that the app's preview runs before the work order
	// merges.
	PreviewCheck bool
}
