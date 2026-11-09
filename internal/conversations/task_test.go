package conversations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/outputs"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A general helper runs one writable attempt with the web in a directory of
// its own; the files it leaves in outputs/ are kept, named on its event line
// and to the assistant, and its directory goes.
func TestAGeneralHelperHandsItsFilesBack(t *testing.T) {
	service, worker := newTestService(t)
	store, err := outputs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service.config.Outputs = store
	stored, err := service.read(TopLevel)
	if err != nil {
		t.Fatal(err)
	}
	stored.Actions = append(stored.Actions, Action{ID: "act_deck", Status: ActionProposed,
		AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantDelegate, Reason: "Asked for",
			Title: "Pitch deck", Description: "A ten-slide pitch deck for a bakery app, in English."}})
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	// It is sent without a card, like research.
	if actions, err := service.autopilotActions(t.Context(), TopLevel); err != nil || len(actions) != 1 {
		t.Fatalf("autopilot = %+v, %v", actions, err)
	}
	helper, err := service.StartTask(t.Context(), TopLevel, "act_deck", "Pitch deck",
		"A ten-slide pitch deck for a bakery app, in English.")
	if err != nil {
		t.Fatal(err)
	}
	put := worker.puts[0]
	if helper.Kind != HelperTask || !put.Web || put.WorkspaceAccess != workerhttp.WorkspaceAccessReadWrite ||
		put.Assignment.Role != workerhttp.RoleConsultant || !strings.Contains(put.Instructions, "ten-slide pitch deck") ||
		!strings.Contains(put.Instructions, "outputs/") {
		t.Fatalf("helper = %+v put = %+v", helper, put)
	}
	if _, err := service.Resolve(t.Context(), TopLevel, "act_deck", ActionApplied, "A helper is working on “Pitch deck”.",
		Created{Helper: &helper}); err != nil {
		t.Fatal(err)
	}
	watcher := Watcher{Service: service}
	watcher.followHelpers(t.Context(), TopLevel)
	if stored, _ := service.read(TopLevel); len(stored.PendingEvents) != 0 {
		t.Fatalf("a working helper woke the assistant: %v", stored.PendingEvents)
	}
	// While it works, the chat names it, so its line shows it working.
	if conversation, err := service.Get(t.Context(), TopLevel); err != nil || strings.Join(conversation.Helping, ",") != "Pitch deck" {
		t.Fatalf("helping = %v, %v", conversation.Helping, err)
	}

	directory := filepath.Join(service.config.WorkspaceRoot, helper.ID)
	for name, contents := range map[string]string{"outputs/deck.html": "<h1>Bakery</h1>",
		"outputs/notes/market.md": "# Market", "outputs/empty.txt": "", "scratch.txt": "not for the user"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(directory, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "deck.html is the deck; notes-market.md the market notes."})
	watcher.followHelpers(t.Context(), TopLevel)
	watcher.followHelpers(t.Context(), TopLevel)

	stored, _ = service.read(TopLevel)
	last := stored.Messages[len(stored.Messages)-1]
	if len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "deck.html, notes-market.md") ||
		!strings.Contains(stored.PendingEvents[0], "deck.html is the deck") || !stored.Helpers[0].Done ||
		last.Text != "The helper on “Pitch deck” is done." || strings.Join(last.Outputs, ",") != "deck.html,notes-market.md" {
		t.Fatalf("pending = %v helpers = %+v last = %+v", stored.PendingEvents, stored.Helpers, last)
	}
	if conversation, _ := service.Get(t.Context(), TopLevel); len(conversation.Helping) != 0 {
		t.Fatalf("a finished helper is still working: %v", conversation.Helping)
	}
	files, err := store.List(t.Context(), outputs.TopLevel)
	if err != nil || len(files) != 2 || files[0].Helper != helper.ID || files[0].Title != "Pitch deck" {
		t.Fatalf("outputs = %+v, %v", files, err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("the helper's directory is still there: %v", err)
	}
}

// A helper that stops short still hands back what it made, and wakes the
// assistant to decide what next.
func TestAGeneralHelperThatFailsKeepsWhatItMade(t *testing.T) {
	service, worker := newTestService(t)
	store, _ := outputs.NewStore(t.TempDir())
	service.config.Outputs = store
	helper, err := service.StartTask(t.Context(), "prj_test", "act_1", "Report", "Write a report.")
	if err != nil {
		t.Fatal(err)
	}
	startedHelper(t, service, helper)
	if err := os.WriteFile(filepath.Join(service.config.WorkspaceRoot, helper.ID, "outputs", "draft.md"),
		[]byte("# Draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeFailed, Summary: "Provider error"})
	Watcher{Service: service, Helpers: func(context.Context, string, Helper) (HelperState, error) {
		t.Fatal("a general helper is read through its own attempt")
		return HelperState{}, nil
	}}.followHelpers(t.Context(), "prj_test")
	stored, _ := service.read("prj_test")
	if len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "The helper on “Report” stopped") ||
		!strings.Contains(stored.PendingEvents[0], "draft.md") {
		t.Fatalf("pending = %v", stored.PendingEvents)
	}
}

// The helper is told the defaults only for when the brief names no format,
// and how to make the rest.
func TestHelperInstructionsLetTheBriefsFormatWin(t *testing.T) {
	instructions := taskInstructions("A pitch deck as a .pptx.", nil)
	for _, want := range []string{"when the brief names a format", "Only when it names none", "16:9",
		"PLAYWRIGHT_BROWSERS_PATH=/opt/commitarium-browser", "python-pptx", "nearest one"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions lack %q", want)
		}
	}
}
