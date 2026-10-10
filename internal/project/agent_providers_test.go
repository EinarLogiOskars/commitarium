package project

import "testing"

func TestAgentProvidersNormalizeLegacyDefaultAndIndependentRoles(t *testing.T) {
	legacy, err := (AgentProviders{}).Normalize()
	if err != nil || legacy != DefaultAgentProviders() {
		t.Fatalf("legacy provider default = %+v, %v", legacy, err)
	}
	mixed := AgentProviders{Lead: AgentProviderClaude, Reviewer: AgentProviderCodex}
	normalized, err := mixed.Normalize()
	want := AgentProviders{
		Lead: AgentProviderClaude, Reviewer: AgentProviderCodex, LeadAgent: "claude", ReviewerAgent: "codex",
	}
	if err != nil || normalized != want {
		t.Fatalf("mixed provider assignment = %+v, %v", normalized, err)
	}
	agents := AgentProviders{
		Lead: AgentProviderClaude, Reviewer: AgentProviderClaude, LeadAgent: "claude-max", ReviewerAgent: "claude-api",
	}
	if normalized, err := agents.Normalize(); err != nil || normalized != agents {
		t.Fatalf("agent assignment = %+v, %v", normalized, err)
	}
}

func TestAgentProvidersRejectPartialOrUnknownAssignments(t *testing.T) {
	for _, providers := range []AgentProviders{
		{Lead: AgentProviderCodex},
		{Lead: AgentProviderCodex, Reviewer: "other"},
		{Lead: AgentProviderCodex, Reviewer: AgentProviderCodex, LeadAgent: "Not Valid"},
		{LeadAgent: "claude-max", ReviewerAgent: "codex"},
	} {
		if _, err := providers.Normalize(); err == nil {
			t.Fatalf("accepted invalid providers %+v", providers)
		}
	}
}
