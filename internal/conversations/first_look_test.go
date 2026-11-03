package conversations

import (
	"strings"
	"testing"
)

func TestTheFirstLookBriefCarriesTheSettingsAndTheMode(t *testing.T) {
	assists := firstLookBrief([]string{"Nothing merges into main until you approve it."}, false)
	for _, want := range []string{"design_stack", "Do not propose set_up_preview",
		"     - Nothing merges into main until you approve it.", "propose none now", "Build for me",
		"a card under your message"} {
		if !strings.Contains(assists, want) {
			t.Errorf("assists brief lacks %q:\n%s", want, assists)
		}
	}
	builds := firstLookBrief(nil, true)
	if !strings.Contains(builds, "you build everything") || !strings.Contains(builds, "propose work orders") ||
		strings.Contains(builds, "a card under your message") {
		t.Errorf("builds brief:\n%s", builds)
	}
}
