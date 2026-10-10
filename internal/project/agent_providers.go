package project

import (
	"errors"
	"fmt"
	"regexp"
)

type AgentProvider string

const (
	AgentProviderCodex  AgentProvider = "codex"
	AgentProviderClaude AgentProvider = "claude"
)

// AgentProviders names the agents that lead and review (ADR-016) and the
// provider each agent uses. The provider always follows from the agent; it is
// kept beside the agent ID for model selection and display. Agents migrated
// from the fixed provider profiles have the provider's name as their ID.
type AgentProviders struct {
	Lead          AgentProvider
	Reviewer      AgentProvider
	LeadAgent     string
	ReviewerAgent string
}

var agentIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)

// ValidAgentID reports whether id is a well-formed agent ID: lowercase
// letters, digits, and dashes, at most 30 characters, so derived worker,
// volume, file, and Forgejo user names stay valid.
func ValidAgentID(id string) bool { return agentIDPattern.MatchString(id) }

var ErrInvalidAgentProviders = errors.New("invalid project agent providers")

func DefaultAgentProviders() AgentProviders {
	return AgentProviders{
		Lead: AgentProviderCodex, Reviewer: AgentProviderCodex,
		LeadAgent: string(AgentProviderCodex), ReviewerAgent: string(AgentProviderCodex),
	}
}

func (provider AgentProvider) IsValid() bool {
	return provider == AgentProviderCodex || provider == AgentProviderClaude
}

// Normalize preserves compatibility with projects and runs created before
// provider selection existed. An entirely empty value means the historical
// Codex/Codex default; a half-specified assignment remains an error. A missing
// agent ID means the migrated agent named after the provider.
func (providers AgentProviders) Normalize() (AgentProviders, error) {
	if providers.Lead == "" && providers.Reviewer == "" &&
		providers.LeadAgent == "" && providers.ReviewerAgent == "" {
		providers = DefaultAgentProviders()
	}
	if !providers.Lead.IsValid() {
		return AgentProviders{}, fmt.Errorf(
			"%w: lead provider must be codex or claude",
			ErrInvalidAgentProviders,
		)
	}
	if !providers.Reviewer.IsValid() {
		return AgentProviders{}, fmt.Errorf(
			"%w: reviewer provider must be codex or claude",
			ErrInvalidAgentProviders,
		)
	}
	if providers.LeadAgent == "" {
		providers.LeadAgent = string(providers.Lead)
	}
	if providers.ReviewerAgent == "" {
		providers.ReviewerAgent = string(providers.Reviewer)
	}
	if !ValidAgentID(providers.LeadAgent) || !ValidAgentID(providers.ReviewerAgent) {
		return AgentProviders{}, fmt.Errorf("%w: agent IDs are invalid", ErrInvalidAgentProviders)
	}
	return providers, nil
}

func (providers AgentProviders) Validate() error {
	_, err := providers.Normalize()
	return err
}
