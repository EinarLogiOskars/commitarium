package project

import "time"

type Project struct {
	ID                         string
	Name                       string
	RecoveryPolicy             RecoveryPolicy
	MergePolicy                MergePolicy
	AutonomyPolicy             AutonomyPolicy
	IndependentAcceptanceTests bool
	// App marks a project the user uses as an app (APPS_PLAN.md).
	App            bool
	DialogueLimits DialogueLimits
	// AgentProviders and AgentModels are deprecated (ADR-027): projects no
	// longer choose agents or models for new work. New projects leave them
	// zero; older ones keep what they stored, which nothing uses as a default.
	AgentProviders    AgentProviders
	AgentModels       AgentModels
	ForgejoRepository *ForgejoRepository
	CreatedAt         time.Time
}

// ForgejoRepository is the verified internal repository identity for a
// project. Credentials and clone URLs are deliberately not part of this
// durable model.
type ForgejoRepository struct {
	Owner         string
	Name          string
	DefaultBranch string
	BoundAt       time.Time
}
