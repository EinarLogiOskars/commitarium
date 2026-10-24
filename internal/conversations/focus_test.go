package conversations

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
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

// Opened on a work order that already started because the assistant builds
// everything, the assistant says so plainly, offers to pass the user's wishes
// to the working agent, and points at Assists me, rather than clarifying it
// as if it were a draft.
func TestTheFocusTellsTheAssistantAWorkOrderAlreadyStarted(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Projects = gapProjects{mode: project.AssistantModeBuilds}
	service.config.Features = &featuresStub{features: []feature.Feature{
		{ID: "fea_glossary", Title: "Add a beginner glossary", State: feature.StateImplementing},
	}}
	if _, err := service.SetFocus(t.Context(), "prj_test", &Focus{Kind: FocusWorkOrder, WorkOrderID: "fea_glossary"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Help me clarify it.", "msg-1"); err != nil {
		t.Fatal(err)
	}
	instructions := worker.puts[0].Instructions
	for _, want := range []string{
		"“Add a beginner glossary” (fea_glossary, running (implementing))",
		"already started when you added it",
		"recover_work_order with how message",
		"\"Assists me\", in Project settings",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions lack %q:\n%s", want, instructions)
		}
	}
	if strings.Contains(instructions, "propose write_brief with everything you settled") {
		t.Fatal("the assistant was told to clarify a started work order as a draft")
	}
}
