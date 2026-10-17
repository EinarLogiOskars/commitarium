package assistant

import (
	"strings"
	"testing"
)

// Clarification records the repository facts the plan will depend on, which
// is what most plan-review findings were about (ADR-031).
func TestClarificationRecordsRepositoryFacts(t *testing.T) {
	if instructions := initialInstructions("Add a filter"); !strings.Contains(instructions, "repository facts the plan will depend on") {
		t.Fatalf("instructions = %s", instructions)
	}
}
