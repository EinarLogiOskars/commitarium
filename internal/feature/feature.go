package feature

import (
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type Feature struct {
	ID             string
	ProjectID      string
	Title          string
	Description    string
	State          State
	AcceptedGoal   string
	GoalAcceptedAt *time.Time
	DialogueLimits project.DialogueLimits
	AgentProviders project.AgentProviders
	AgentModels    project.AgentModels
	MergePolicy    project.MergePolicy
	AutonomyPolicy project.AutonomyPolicy
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SettingsOverrides contains the optional setting categories supplied when a
// work order is created. A nil category inherits the project's current value.
type SettingsOverrides struct {
	DialogueLimits *project.DialogueLimits
	AgentProviders *project.AgentProviders
	AgentModels    *project.AgentModels
	MergePolicy    *project.MergePolicy
	AutonomyPolicy *project.AutonomyPolicy
}
