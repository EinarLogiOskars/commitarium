package httpapi

import (
	"testing"
)

func TestAttemptPhaseFollowsTheAttemptKind(t *testing.T) {
	session := "run_x:reviewer"
	for attemptID, want := range map[string]string{
		"run_x:reviewer:turn:1":           "clarify",
		"run_x:reviewer:reply:abc":        "clarify",
		"run_x:reviewer:planning:2":       "plan",
		"run_x:reviewer:planning:v2:1":    "plan",
		"run_x:reviewer:approval:1":       "plan_approval",
		"run_x:reviewer:implementation:1": "implement",
		"run_x:reviewer:acceptance:1":     "acceptance_tests",
		"run_x:reviewer:review:v3:2":      "review",
		"run_x:reviewer:correction:1":     "correction",
		"run_x:reviewer:readiness:1":      "readiness",
		"run_x:reviewer:intervention:ff":  "intervention",
		"run_x:reviewer:mystery:1":        "other",
		"other-session:review:1":          "other",
	} {
		if got := attemptPhase(session, attemptID); got != want {
			t.Errorf("attemptPhase(%q) = %q, want %q", attemptID, got, want)
		}
	}
}
