package conversations

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

// Opened on a Draft, the assistant hears which work order the user means
// and how to clarify it; the chip names it until the user clears it.
func TestTheFocusTellsTheAssistantWhatTheUserMeans(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Features = &featuresStub{features: []feature.Feature{
		{ID: "fea_sort", Title: "Sort the shelf", Description: "By date.", State: feature.StateDraft},
	}}
	if _, err := service.SetFocus(t.Context(), "prj_test", &Focus{Kind: FocusWorkOrder, WorkOrderID: "fea_missing"}); err == nil {
		t.Fatal("focused on a work order that does not exist")
	}
	conversation, err := service.SetFocus(t.Context(), "prj_test", &Focus{Kind: FocusWorkOrder, WorkOrderID: "fea_sort"})
	if err != nil || conversation.Focus == nil || conversation.Focus.Label != "Sort the shelf" {
		t.Fatalf("focus = %+v, %v", conversation.Focus, err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Help me clarify it.", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if instructions := worker.puts[0].Instructions; !strings.Contains(instructions, "“Sort the shelf” (fea_sort, draft)") ||
		!strings.Contains(instructions, "propose write_brief") {
		t.Fatalf("instructions = %s", instructions)
	}
	if conversation, err := service.SetFocus(t.Context(), "prj_test", nil); err != nil || conversation.Focus != nil {
		t.Fatalf("cleared focus = %+v, %v", conversation.Focus, err)
	}
}
