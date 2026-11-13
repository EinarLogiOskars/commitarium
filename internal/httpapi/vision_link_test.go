package httpapi

import (
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

func TestANewWorkOrderTakesOverAVisionItemOnlyFromWorkThatEnded(t *testing.T) {
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	visions, err := vision.NewService(t.TempDir(), t.TempDir(), projects, noVisionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := visions.Save(t.Context(), "prj_test", featureartifact.VisionPlan{
		Statement: "Stock for the bakery.", Confidence: "high",
		Priorities: []featureartifact.VisionPriority{{Level: 0, Items: []featureartifact.VisionItem{
			{ID: "stock", Title: "Stock API"},
		}}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := visions.LinkWorkOrder(t.Context(), "prj_test", "stock", "fea_stock"); err != nil {
		t.Fatal(err)
	}
	close := conversations.Action{ID: "act_close", MessageID: "msg_2", AssistantAction: featureartifact.AssistantAction{
		Type: featureartifact.AssistantCloseWorkOrder, WorkOrderID: "fea_stock",
	}}
	create := func(message, itemID string) conversations.Action {
		return conversations.Action{ID: "act_create", MessageID: message, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Title: "Next", VisionItemID: itemID,
		}}
	}
	for name, test := range map[string]struct {
		state  feature.State
		action conversations.Action
		want   string
	}{
		"a follow-up on an item whose work is open":   {feature.StateImplementing, create("msg_1", "stock"), ""},
		"a new one for an item whose work was closed": {feature.StateCancelled, create("msg_1", "stock"), "stock"},
		"a replacement proposed with the close":       {feature.StateCancelled, create("msg_2", ""), "stock"},
		"a replacement whose close is not applied":    {feature.StateImplementing, create("msg_2", ""), ""},
		"unrelated work from another message":         {feature.StateCancelled, create("msg_3", ""), ""},
	} {
		t.Run(name, func(t *testing.T) {
			api := &API{
				visions:       visions,
				features:      &recordingFeatureService{getResult: feature.Feature{ID: "fea_stock", State: test.state}},
				conversations: &conversationStub{actions: []conversations.Action{close}},
			}
			if got := api.itemToLink(t.Context(), "prj_test", test.action); got != test.want {
				t.Fatalf("linked %q, want %q", got, test.want)
			}
		})
	}
}
