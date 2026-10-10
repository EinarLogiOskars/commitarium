package agent

import (
	"strings"
	"testing"
)

func TestIDFromName(t *testing.T) {
	for name, want := range map[string]string{
		"Claude Max":            "claude-max",
		"  Codex Pro #2 ":       "codex-pro-2",
		"!!!":                   "agent",
		strings.Repeat("a", 50): strings.Repeat("a", MaxIDLength),
	} {
		if got := IDFromName(name); got != want {
			t.Errorf("IDFromName(%q) = %q, want %q", name, got, want)
		}
	}
}
