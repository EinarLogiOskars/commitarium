package conversations

import (
	"strings"
	"testing"
)

// One brief for every level: it carries the settings and tells her to close
// from her say, offering Build everything under Ask first (ADR-041).
func TestTheFirstLookBriefCarriesTheSettingsAndHerSay(t *testing.T) {
	brief := firstLookBrief([]string{"Nothing merges into main until you approve it."})
	for _, want := range []string{"design_stack", "Do not propose set_up_preview",
		"     - Nothing merges into main until you approve it.", "from your say in the state",
		"Under Build everything you start on the list without asking", "proposing none now",
		"a card under your message"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "Assists me") || strings.Contains(brief, "Build for me") {
		t.Errorf("brief still names the old modes:\n%s", brief)
	}
}
