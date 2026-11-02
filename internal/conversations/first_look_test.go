package conversations

import (
	"strings"
	"testing"
)

func TestTheFirstLookBriefCarriesTheSettingsAndTheMode(t *testing.T) {
	assists := firstLookBrief([]string{"Nothing merges into main until you approve it."}, false)
	for _, want := range []string{"design_stack", "     - Nothing merges into main until you approve it.",
		"Build for me", "a card under your message"} {
		if !strings.Contains(assists, want) {
			t.Errorf("assists brief lacks %q:\n%s", want, assists)
		}
	}
	builds := firstLookBrief(nil, true)
	if !strings.Contains(builds, "you build everything") || strings.Contains(builds, "a card under your message") {
		t.Errorf("builds brief:\n%s", builds)
	}
}
