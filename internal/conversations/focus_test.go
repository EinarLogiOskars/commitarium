package conversations

import (
	"errors"
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

// Opened on company sign-in, the assistant carries the setup through for as
// long as the chip stays, and never takes the secret in the chat.
func TestTheSignInFocusCarriesTheSetup(t *testing.T) {
	service, worker := newTestService(t)
	conversation, err := service.SetFocus(t.Context(), "prj_test", &Focus{
		Kind: FocusSignIn, Facts: "Memory: 7.6 GB\x07\n\nHomelab: off",
	})
	if err != nil || conversation.Focus == nil || conversation.Focus.Label != "Company sign-in" {
		t.Fatalf("focus = %+v, %v", conversation.Focus, err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Set up company sign-in.", "msg-1"); err != nil {
		t.Fatal(err)
	}
	instructions := worker.puts[0].Instructions
	for _, want := range []string{
		"https://sign-in.<domain>/oauth2/callback",
		"Settings → Server → Company sign-in",
		"user type to Internal",
		"Never ask for the client secret in this chat",
		"never ask the user for any of it",
		"Memory: 7.6 GB\nHomelab: off",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions miss %q", want)
		}
	}
}

// A chat outside any project may be focused on the user's server, such as
// serving their apps and their DNS, but not on a project's item.
func TestAChatOutsideProjectsMayFocusOnTheServer(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.SetFocus(t.Context(), TopLevel, &Focus{Kind: FocusStack}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a top-level stack focus: %v", err)
	}
	conversation, err := service.SetFocus(t.Context(), TopLevel, &Focus{Kind: FocusServing})
	if err != nil || conversation.Focus == nil || conversation.Focus.Label != "Serving your apps" {
		t.Fatalf("focus = %+v, %v", conversation.Focus, err)
	}
	if _, err := service.Send(t.Context(), TopLevel, "Help me with my DNS.", "msg-1"); err != nil {
		t.Fatal(err)
	}
	instructions := worker.puts[0].Instructions
	for _, want := range []string{
		"One wildcard record (recommended)",
		`"DNS only" (grey cloud)`,
		"Settings → Server → Homelab",
		"sudo commitarium-host pair",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("instructions miss %q", want)
		}
	}
	if _, err := service.SetFocus(t.Context(), TopLevel, nil); err != nil {
		t.Fatalf("clearing it: %v", err)
	}
}
