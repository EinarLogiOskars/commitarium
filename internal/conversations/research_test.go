package conversations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// Research runs one read-only attempt on the assistant's agent, with the
// web and an empty directory instead of a repository; its findings and
// sources come back to the assistant, at the top level too, where a new
// app is talked through.
func TestResearchBringsItsFindingsBackToTheAssistant(t *testing.T) {
	service, worker := newTestService(t)
	stored, err := service.read(TopLevel)
	if err != nil {
		t.Fatal(err)
	}
	stored.Actions = append(stored.Actions, Action{ID: "act_vat", Status: ActionProposed,
		AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantResearch, Reason: "A fact",
			Title: "Iceland's VAT rates", Description: "Which VAT rates apply in Iceland, and when is a return due?"}})
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	// It is sent without a card, assisting too.
	actions, err := service.autopilotActions(t.Context(), TopLevel)
	if err != nil || len(actions) != 1 || actions[0].ID != "act_vat" {
		t.Fatalf("autopilot = %+v, %v", actions, err)
	}
	helper, err := service.StartResearch(t.Context(), TopLevel, "act_vat", "Iceland's VAT rates",
		"Which VAT rates apply in Iceland, and when is a return due?")
	if err != nil {
		t.Fatal(err)
	}
	put := worker.puts[0]
	if len(worker.puts) != 1 || !put.Web || put.WorkspaceAccess != workerhttp.WorkspaceAccessReadOnly ||
		put.OutputContract != "" || put.Assignment.Role != workerhttp.RoleConsultant || put.Assignment.AgentProfileID != "codex" ||
		!strings.Contains(put.Instructions, "Which VAT rates apply in Iceland") || !strings.Contains(put.Instructions, "Sources:") {
		t.Fatalf("put = %+v", put)
	}
	if entries, err := os.ReadDir(filepath.Join(service.config.WorkspaceRoot, helper.ID)); err != nil || len(entries) != 0 {
		t.Fatalf("the research directory is not empty: %v, %v", entries, err)
	}
	if _, err := service.Resolve(t.Context(), TopLevel, "act_vat", ActionApplied, "Looking up “Iceland's VAT rates”.",
		Created{Helper: &helper}); err != nil {
		t.Fatal(err)
	}

	watcher := Watcher{Service: service}
	watcher.followHelpers(t.Context(), TopLevel)
	if stored, _ := service.read(TopLevel); len(stored.PendingEvents) != 0 {
		t.Fatalf("working research woke the assistant: %v", stored.PendingEvents)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "The standard rate is 24%, the reduced rate 11%.\n\nSources:\n- https://www.skatturinn.is/"})
	watcher.followHelpers(t.Context(), TopLevel)
	watcher.followHelpers(t.Context(), TopLevel)
	stored, _ = service.read(TopLevel)
	if len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "standard rate is 24%") ||
		!strings.Contains(stored.PendingEvents[0], "https://www.skatturinn.is/") || !stored.Helpers[0].Done ||
		lastEvent(stored.Conversation) != "The research on “Iceland's VAT rates” is back." {
		t.Fatalf("pending = %v helpers = %+v last = %q", stored.PendingEvents, stored.Helpers, lastEvent(stored.Conversation))
	}
	if _, err := os.Stat(filepath.Join(service.config.WorkspaceRoot, helper.ID)); !os.IsNotExist(err) {
		t.Fatalf("the research directory is still there: %v", err)
	}
	// The top level has no work to observe, yet the findings start its turn.
	if err := service.wakeIfWaiting(t.Context(), TopLevel); err != nil {
		t.Fatal(err)
	}
	if len(worker.puts) != 2 || !strings.Contains(worker.puts[1].Instructions, "standard rate is 24%") {
		t.Fatalf("the findings did not start a turn: puts = %d", len(worker.puts))
	}
}

// Research that stops short wakes the assistant to decide what next.
func TestResearchThatFailsWakesTheAssistant(t *testing.T) {
	service, worker := newTestService(t)
	helper, err := service.StartResearch(t.Context(), "prj_test", "act_1", "Invoice format", "Which fields must an invoice have?")
	if err != nil {
		t.Fatal(err)
	}
	startedHelper(t, service, helper)
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeFailed, Summary: "Provider error"})
	Watcher{Service: service, Helpers: func(context.Context, string, Helper) (HelperState, error) {
		t.Fatal("research is read through its own attempt")
		return HelperState{}, nil
	}}.followHelpers(t.Context(), "prj_test")
	stored, _ := service.read("prj_test")
	if len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "The research on “Invoice format” stopped") {
		t.Fatalf("pending = %v", stored.PendingEvents)
	}
}

// At the top level, each turn says how to settle a new app before it is
// built; without Developer mode, the app stays self-contained.
func TestANewAppIsSettledBeforeItIsBuilt(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), TopLevel, "I'd like a new app: VAT returns", "msg-1"); err != nil {
		t.Fatal(err)
	}
	first := worker.puts[0].Instructions
	for _, want := range []string{"first judge its scope", "Every third round, step back",
		"Never ask what you asked before", "When the user says go", "does not use Developer mode", "app_brief",
		"never propose plan_project"} {
		if !strings.Contains(first, want) {
			t.Fatalf("the top level's turn lacks %q", want)
		}
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Who files it?"})
	if _, err := service.Get(t.Context(), TopLevel); err != nil {
		t.Fatal(err)
	}
	if err := service.SetDeveloperMode(t.Context(), TopLevel, true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), TopLevel, "Just me", "msg-2"); err != nil {
		t.Fatal(err)
	}
	if next := worker.puts[1].Instructions; !strings.Contains(next, "only Developer mode allows (the user uses it)") ||
		!strings.Contains(next, "- plan_project:") || strings.Contains(next, "does not use Developer mode") {
		t.Fatalf("Developer mode is not in the next turn: %s", next)
	}
	// A project's turns carry none of it.
	if _, err := service.Send(t.Context(), "prj_test", "Hi", "msg-3"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(worker.puts[2].Instructions, "New apps.") {
		t.Fatal("a project's turn has the new-app instructions")
	}
}
