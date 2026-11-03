package conversations

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// A Start autopilot card Mayka offers while autopilot already runs for the
// whole vision waits on the user for nothing, as one offered before it did.
func TestARunningGoalCoversAStartCardOfferedAfterIt(t *testing.T) {
	started := time.Date(2026, 11, 3, 23, 0, 0, 0, time.UTC)
	card := Action{Status: ActionProposed, UpdatedAt: started.Add(time.Minute)}
	card.Type, card.Priority = featureartifact.AssistantStartAutopilot, 0

	running := &Goal{Level: AllPriorities, Status: GoalActive, StartedAt: started}
	if !running.covers(card) {
		t.Fatal("a running goal for the whole vision should cover a later Finish P0 card")
	}
	stopped := &Goal{Level: AllPriorities, Status: GoalStopped, StartedAt: started}
	if stopped.covers(card) {
		t.Fatal("a goal the user stopped should leave the card to start it again")
	}
	other := &Goal{Level: 1, Status: GoalActive, StartedAt: started}
	if other.covers(card) {
		t.Fatal("a goal for another priority should not cover the card")
	}
}
