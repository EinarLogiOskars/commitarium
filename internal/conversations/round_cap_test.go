package conversations

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

// A work order's round limit wakes the assistant, told as what happened:
// the first, or one after it handled a round limit (her rules say which is
// hers to handle).
func TestTheAssistantHearsOfARoundLimit(t *testing.T) {
	item := work{
		Title: "Show the log", State: feature.StatePlanning, RunID: "run_1",
		RunStatus: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindRoundCap,
		Reason: "The planning discussion completed 10 dialogue rounds without a submitted plan.",
	}
	first, ok := item.event()
	if !ok || !first.Wake || !strings.Contains(first.Text, "round limit for the first time") ||
		strings.Contains(first.Text, "continue") {
		t.Fatalf("the first round limit = %+v", first)
	}
	handled := item
	handled.RoundCapHandled = true
	again, ok := handled.event()
	if !ok || !again.Wake || !strings.Contains(again.Text, "after you handled it once") ||
		strings.Contains(again.Text, "ask for") {
		t.Fatalf("the round limit again = %+v", again)
	}
	if item.signature() == handled.signature() {
		t.Fatal("handling the round limit does not change what the watcher sees")
	}
}
