package conversations

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

// A work order's first round limit wakes the assistant to handle it; one
// after it handled a round limit is the user's, and it is told so.
func TestTheAssistantHearsOfARoundLimit(t *testing.T) {
	item := work{
		Title: "Show the log", State: feature.StatePlanning, RunID: "run_1",
		RunStatus: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindRoundCap,
		Reason: "The planning discussion completed 10 dialogue rounds without a submitted plan.",
	}
	first, ok := item.event()
	if !ok || !first.Wake || !strings.Contains(first.Text, "Handle it before the user") ||
		!strings.Contains(first.Text, "How you handle round limits") {
		t.Fatalf("the first round limit = %+v", first)
	}
	handled := item
	handled.RoundCapHandled = true
	again, ok := handled.event()
	if !ok || !again.Wake || !strings.Contains(again.Text, "do not continue it again") ||
		!strings.Contains(again.Text, "ask for their decision") {
		t.Fatalf("the round limit again = %+v", again)
	}
	if item.signature() == handled.signature() {
		t.Fatal("handling the round limit does not change what the watcher sees")
	}
}
