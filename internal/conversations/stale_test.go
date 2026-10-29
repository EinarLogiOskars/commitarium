package conversations

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// A card whose work order already got where it leads settles itself, while
// the assistant only assists too: started, closed, or no longer blocked.
// New work and messages to a working lead never go stale.
func TestStaleCardsSettleThemselvesInEveryMode(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Projects = gapProjects{mode: project.AssistantModeAssists}
	service.config.Features = &featuresStub{features: []feature.Feature{
		{ID: "fea_started", Title: "Glossary", State: feature.StateImplementing},
		{ID: "fea_ready", Title: "Sort", State: feature.StateReady},
		{ID: "fea_closed", Title: "Old", State: feature.StateCancelled},
		{ID: "fea_running", Title: "Sources", State: feature.StateReviewing},
		{ID: "fea_blocked", Title: "Counts", State: feature.StateReviewing},
	}}
	service.config.Blocked = func(_ context.Context, workOrderID string) bool { return workOrderID == "fea_blocked" }
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	action := func(id string, kind featureartifact.AssistantActionType, workOrderID, how string) Action {
		return Action{ID: id, Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: kind, Reason: "Next", WorkOrderID: workOrderID, How: how}}
	}
	stored.Actions = append(stored.Actions,
		action("act_start", featureartifact.AssistantStartWorkOrder, "fea_started", ""),
		action("act_start_ready", featureartifact.AssistantStartWorkOrder, "fea_ready", ""),
		action("act_close", featureartifact.AssistantCloseWorkOrder, "fea_closed", ""),
		action("act_continue", featureartifact.AssistantContinueWorkOrder, "fea_running", ""),
		action("act_recover", featureartifact.AssistantRecoverWorkOrder, "fea_running", "recheck"),
		action("act_message", featureartifact.AssistantRecoverWorkOrder, "fea_running", "message"),
		action("act_recover_blocked", featureartifact.AssistantRecoverWorkOrder, "fea_blocked", "continue"),
		action("act_create", featureartifact.AssistantCreateWorkOrder, "", ""),
	)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	if apply, err := service.autopilotActions(t.Context(), "prj_test"); err != nil || len(apply) != 0 {
		t.Fatalf("assisting, autopilot applied %+v err=%v", apply, err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	want := map[string]string{
		"act_start":    "Already started.",
		"act_close":    "Already closed.",
		"act_continue": "No longer needed: the work order is not waiting on that blocker any more.",
		"act_recover":  "No longer needed: the work order is not waiting on that blocker any more.",
	}
	for _, action := range conversation.Actions {
		result, stale := want[action.ID]
		switch {
		case stale && (action.Status != ActionApplied || !action.Settled || action.Result != result):
			t.Errorf("%s = %s settled=%v %q, want settled %q", action.ID, action.Status, action.Settled, action.Result, result)
		case !stale && action.Status != ActionProposed:
			t.Errorf("%s = %s %q, want it still waiting", action.ID, action.Status, action.Result)
		}
	}
	if instructions := recentActions(conversation.Actions, nil); !strings.Contains(instructions, "settled without applying") {
		t.Fatalf("the assistant does not hear the card settled: %s", instructions)
	}
}

// Autopilot's apply refused because the work order got there first settles
// the card instead of leaving it for the user's approval.
func TestARefusedStaleActionSettlesInsteadOfWaiting(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Projects = gapProjects{mode: project.AssistantModeBuilds}
	features := &featuresStub{features: []feature.Feature{{ID: "fea_glossary", Title: "Glossary", State: feature.StateReady}}}
	service.config.Features = features
	stored, _ := service.read("prj_test")
	start := Action{ID: "act_start", Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
		Type: featureartifact.AssistantStartWorkOrder, Reason: "Next", Title: "Start the beginner glossary", WorkOrderID: "fea_glossary"}}
	stored.Actions = append(stored.Actions, start)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	// It started between the look and the apply.
	features.features[0].State = feature.StatePlanning
	if !service.settleIfStale(t.Context(), "prj_test", start) {
		t.Fatal("a stale start was not settled")
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if action := conversation.Actions[0]; action.Status != ActionApplied || !action.Settled || action.Result != "Already started." {
		t.Fatalf("action = %+v", action)
	}
	for _, message := range conversation.Messages {
		if strings.Contains(message.Text, "waits for your approval") {
			t.Fatalf("noted %q", message.Text)
		}
	}
}

// Autopilot's last step on a work order is its latest action on it, or now
// while the assistant works, so a blocker it keeps recovering never reads as
// stalled.
func TestAutopilotActiveAtFollowsItsLastStep(t *testing.T) {
	service, _ := newTestService(t)
	now := time.Date(2026, time.October, 24, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	stored, _ := service.read("prj_test")
	recovered := now.Add(-3 * time.Minute)
	stored.Actions = append(stored.Actions,
		Action{ID: "act_old", Status: ActionApplied, UpdatedAt: now.Add(-time.Hour), AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantRecoverWorkOrder, WorkOrderID: "fea_ui", How: "recheck"}},
		Action{ID: "act_new", Status: ActionApplied, UpdatedAt: recovered, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantRecoverWorkOrder, WorkOrderID: "fea_ui", How: "continue"}},
		Action{ID: "act_other", Status: ActionApplied, UpdatedAt: now, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantRecoverWorkOrder, WorkOrderID: "fea_other", How: "continue"}},
	)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	if got := service.AutopilotActiveAt(t.Context(), "prj_test", "fea_ui"); !got.Equal(recovered) {
		t.Fatalf("active at %v, want %v", got, recovered)
	}
	stored.Status = StatusRunning
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	if got := service.AutopilotActiveAt(t.Context(), "prj_test", "fea_ui"); !got.Equal(now) {
		t.Fatalf("in a turn, active at %v, want now", got)
	}
}

// A start_autopilot card left waiting settles once autopilot has since taken
// on the same priority, or the whole vision, or finished it; a card newer
// than both, or one for another priority, still waits.
func TestAnAutopilotCardSettlesOnceAutopilotTookItOn(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Projects = gapProjects{mode: project.AssistantModeAssists}
	service.config.Features = &featuresStub{}
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, time.October, 28, 18, 0, 0, 0, time.UTC)
	ended := started.Add(time.Hour)
	stored.Goal = &Goal{Title: "Finish P0", Level: 0, Status: GoalDone, StartedAt: started, EndedAt: &ended}
	card := func(id string, priority int, at time.Time) Action {
		return Action{ID: id, Status: ActionProposed, UpdatedAt: at, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantStartAutopilot, Reason: "Next", Title: "Build it", Priority: priority}}
	}
	stored.Actions = append(stored.Actions,
		card("act_old", 0, started.Add(-time.Hour)),
		card("act_during", 0, started.Add(time.Minute)),
		card("act_new", 0, started.Add(2*time.Hour)),
		card("act_other", 1, started.Add(-time.Hour)),
	)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := service.autopilotActions(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	for _, action := range conversation.Actions {
		settled := action.ID == "act_old" || action.ID == "act_during"
		if settled != (action.Status == ActionApplied && action.Settled) {
			t.Errorf("%s = %s settled=%v %q", action.ID, action.Status, action.Settled, action.Result)
		}
	}
}
