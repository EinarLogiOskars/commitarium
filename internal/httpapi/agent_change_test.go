package httpapi

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// change_models keeps a started run's agents: naming another agent is
// refused plainly, not checked as a model the current agent lacks.
func TestChangingModelsKeepsTheRunsAgents(t *testing.T) {
	providers := project.AgentProviders{Lead: "claude", Reviewer: "codex", LeadAgent: "claude", ReviewerAgent: "codex"}
	for _, keeps := range [][2]string{{"", ""}, {"claude", ""}, {"claude", "codex"}} {
		if refusal := agentChange(providers, keeps[0], keeps[1]); refusal != "" {
			t.Fatalf("%v refused: %s", keeps, refusal)
		}
	}
	refusal := agentChange(providers, "codex", "")
	if !strings.Contains(refusal, "its lead is claude") || !strings.Contains(refusal, "To use codex instead") {
		t.Fatalf("refusal %q", refusal)
	}
}
