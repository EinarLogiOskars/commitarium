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
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

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
	// Kind is the kind of work to create; empty is a work order. A session
	// has neither a plan nor a reviewer.
	Kind Kind
	// Background creates a work order Commitarium runs by itself.
	Background bool
}
