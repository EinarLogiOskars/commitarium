package project

import "time"

type Project struct {
	ID                string
	Name              string
	RecoveryPolicy    RecoveryPolicy
	MergePolicy       MergePolicy
	DialogueLimits    DialogueLimits
	AgentProviders    AgentProviders
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
