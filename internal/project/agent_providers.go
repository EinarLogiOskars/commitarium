package project

import (
	"errors"
	"fmt"
)

type AgentProvider string

const (
	AgentProviderCodex  AgentProvider = "codex"
	AgentProviderClaude AgentProvider = "claude"
)

type AgentProviders struct {
	Lead     AgentProvider
	Reviewer AgentProvider
}

var ErrInvalidAgentProviders = errors.New("invalid project agent providers")

func DefaultAgentProviders() AgentProviders {
	return AgentProviders{Lead: AgentProviderCodex, Reviewer: AgentProviderCodex}
}

func (provider AgentProvider) IsValid() bool {
	return provider == AgentProviderCodex || provider == AgentProviderClaude
}

// Normalize preserves compatibility with projects and runs created before
// provider selection existed. An entirely empty value means the historical
// Codex/Codex default; a half-specified assignment remains an error.
func (providers AgentProviders) Normalize() (AgentProviders, error) {
	if providers.Lead == "" && providers.Reviewer == "" {
		return DefaultAgentProviders(), nil
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
	return providers, nil
}

func (providers AgentProviders) Validate() error {
	_, err := providers.Normalize()
	return err
}
