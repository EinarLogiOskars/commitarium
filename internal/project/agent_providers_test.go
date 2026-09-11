package project

import "testing"

func TestAgentProvidersNormalizeLegacyDefaultAndIndependentRoles(t *testing.T) {
	legacy, err := (AgentProviders{}).Normalize()
	if err != nil || legacy != DefaultAgentProviders() {
		t.Fatalf("legacy provider default = %+v, %v", legacy, err)
	}
	mixed := AgentProviders{Lead: AgentProviderClaude, Reviewer: AgentProviderCodex}
	normalized, err := mixed.Normalize()
	if err != nil || normalized != mixed {
		t.Fatalf("mixed provider assignment = %+v, %v", normalized, err)
	}
}

func TestAgentProvidersRejectPartialOrUnknownAssignments(t *testing.T) {
	for _, providers := range []AgentProviders{
		{Lead: AgentProviderCodex},
		{Lead: AgentProviderCodex, Reviewer: "other"},
	} {
		if _, err := providers.Normalize(); err == nil {
			t.Fatalf("accepted invalid providers %+v", providers)
		}
	}
}
