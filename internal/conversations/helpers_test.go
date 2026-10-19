package conversations

import (
	"context"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// startedHelper records a helper the assistant started, as an approved
// write_vision does.
func startedHelper(t *testing.T, service *Service, helper Helper) {
	t.Helper()
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	stored.Actions = append(stored.Actions, Action{ID: "act_vision", Status: ActionProposed,
		AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantWriteVision, Reason: "No vision"}})
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", "act_vision", ActionApplied, "Writing the vision.",
		Created{Helper: &helper}); err != nil {
		t.Fatal(err)
	}
}

// A helper the assistant started that asks wakes the assistant with its
// question, once; the user sees the question without the helper's name for
// the assistant. Helpers the user started elsewhere are not followed.
func TestAHelpersQuestionWakesTheAssistant(t *testing.T) {
	service, _ := newTestService(t)
	state := HelperState{Status: HelperWorking, Turn: 1}
	read := []string{}
	watcher := Watcher{Service: service, Helpers: func(_ context.Context, _ string, helper Helper) (HelperState, error) {
		read = append(read, helper.ID)
		return state, nil
	}}
	watcher.followHelpers(t.Context(), "prj_test")
	if len(read) != 0 {
		t.Fatalf("read helpers the assistant never started: %v", read)
	}

	startedHelper(t, service, Helper{ID: "vis_1", Kind: HelperVision})
	watcher.followHelpers(t.Context(), "prj_test")
	if stored, _ := service.read("prj_test"); len(stored.PendingEvents) != 0 {
		t.Fatalf("a working helper woke the assistant: %v", stored.PendingEvents)
	}
	state = HelperState{Status: HelperAsks, Turn: 2, Text: "Is it for families or one person?"}
	watcher.followHelpers(t.Context(), "prj_test")
	watcher.followHelpers(t.Context(), "prj_test")
	stored, _ := service.read("prj_test")
	if len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "helper vis_1") ||
		!strings.Contains(stored.PendingEvents[0], "answer_helper") ||
		!strings.Contains(stored.PendingEvents[0], "Is it for families or one person?") {
		t.Fatalf("pending = %v", stored.PendingEvents)
	}
	line := stored.Messages[len(stored.Messages)-1]
	if line.Role != "event" || line.Text != "The vision writer asks: Is it for families or one person?" {
		t.Fatalf("the user's line = %+v", line)
	}

	// Once written, the helper is no longer followed.
	state = HelperState{Status: HelperFinished, Turn: 4}
	watcher.followHelpers(t.Context(), "prj_test")
	read = nil
	watcher.followHelpers(t.Context(), "prj_test")
	if len(read) != 0 {
		t.Fatalf("a finished helper is still followed: %v", read)
	}
}

// A helper that stops short wakes the assistant to decide what next.
func TestAHelperThatFailsWakesTheAssistant(t *testing.T) {
	service, _ := newTestService(t)
	startedHelper(t, service, Helper{ID: "vis_1", Kind: HelperVision})
	watcher := Watcher{Service: service, Helpers: func(context.Context, string, Helper) (HelperState, error) {
		return HelperState{Status: HelperFailed, Turn: 2}, nil
	}}
	watcher.followHelpers(t.Context(), "prj_test")
	stored, _ := service.read("prj_test")
	if len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "The vision writer stopped") ||
		!stored.Helpers[0].Done {
		t.Fatalf("pending = %v helpers = %+v", stored.PendingEvents, stored.Helpers)
	}
}

// The assistant's answer to a helper is sent at once, where it assists as
// where it builds everything: it changes nothing in the project.
func TestAnAnswerToAHelperIsSentWithoutApproval(t *testing.T) {
	for _, mode := range []project.AssistantMode{project.AssistantModeAssists, project.AssistantModeBuilds} {
		service, _ := newTestService(t)
		service.config.Projects = gapProjects{mode: mode}
		stored, _ := service.read("prj_test")
		stored.Actions = []Action{
			{ID: "act_answer", Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
				Type: featureartifact.AssistantAnswerHelper, Reason: "From the vision", Helper: "vis_1", Guidance: "Families.",
			}},
			{ID: "act_create", Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
				Type: featureartifact.AssistantCreateWorkOrder, Reason: "Asked", Title: "Sort",
			}},
		}
		if err := service.write(stored); err != nil {
			t.Fatal(err)
		}
		actions, err := service.autopilotActions(t.Context(), "prj_test")
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, action := range actions {
			ids = append(ids, action.ID)
		}
		want := "act_answer"
		if mode == project.AssistantModeBuilds {
			want = "act_answer act_create"
		}
		if strings.Join(ids, " ") != want {
			t.Fatalf("%s: applied %v, want %s", mode, ids, want)
		}
	}
}

// A brief writer's proposal becomes a brief card under the line that
// announces it; a newer proposal replaces a card still waiting. Assisting,
// the card waits for the user; building everything, autopilot applies it.
func TestABriefBecomesACardApprovedAsTheModeSays(t *testing.T) {
	for _, mode := range []project.AssistantMode{project.AssistantModeAssists, project.AssistantModeBuilds} {
		service, _ := newTestService(t)
		service.config.Projects = gapProjects{mode: mode}
		service.config.Features = &featuresStub{features: []feature.Feature{
			{ID: "fea_sort", Title: "Sort the shelf", State: feature.StateDraft},
		}}
		startedHelper(t, service, Helper{ID: "ast_1", Kind: HelperBrief, WorkOrderID: "fea_sort", Title: "Sort the shelf"})
		state := HelperState{Status: HelperProposes, Turn: 2, Text: "Sort the shelf by date."}
		watcher := Watcher{Service: service, Helpers: func(context.Context, string, Helper) (HelperState, error) {
			return state, nil
		}}
		watcher.followHelpers(t.Context(), "prj_test")
		state = HelperState{Status: HelperProposes, Turn: 4, Text: "Sort the shelf by date, newest first."}
		watcher.followHelpers(t.Context(), "prj_test")
		stored, _ := service.read("prj_test")
		cards := stored.Actions[1:]
		if len(cards) != 2 || cards[0].Status != ActionDismissed || cards[1].Status != ActionProposed ||
			cards[1].Type != featureartifact.AssistantAcceptBrief || cards[1].WorkOrderID != "fea_sort" ||
			cards[1].Description != "Sort the shelf by date, newest first." ||
			cards[1].MessageID != stored.Messages[len(stored.Messages)-1].ID ||
			stored.Messages[len(stored.Messages)-1].Text != "The brief for “Sort the shelf” is ready." {
			t.Fatalf("cards = %+v", cards)
		}
		if !stored.handles("fea_sort") {
			t.Fatal("the gap check would start a work order whose brief is being written")
		}
		applied, err := service.autopilotActions(t.Context(), "prj_test")
		if err != nil {
			t.Fatal(err)
		}
		if builds := mode == project.AssistantModeBuilds; (len(applied) == 1 && applied[0].ID == cards[1].ID) != builds {
			t.Fatalf("%s: autopilot applied %+v", mode, applied)
		}
		// Accepting the brief ends the helper's work.
		if _, err := service.Resolve(t.Context(), "prj_test", cards[1].ID, ActionApplied, "Ready.", Created{}); err != nil {
			t.Fatal(err)
		}
		if stored, _ := service.read("prj_test"); !stored.Helpers[0].Done {
			t.Fatal("the brief writer is still followed after its brief was accepted")
		}
	}
}
