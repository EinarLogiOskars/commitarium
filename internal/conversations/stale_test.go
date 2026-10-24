package conversations

import (
	"context"
	"strings"
	"testing"

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
