package conversations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/outputs"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// deckHelper starts a general helper in prj_test that has made deck.html,
// and hands back its service, worker, store, and helper as followed.
func deckHelper(t *testing.T, finish bool) (*Service, *workerStub, *outputs.Store, Helper) {
	t.Helper()
	service, worker := newTestService(t)
	store, err := outputs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service.config.Outputs = store
	stored, _ := service.read("prj_test")
	stored.Actions = append(stored.Actions, Action{ID: "act_deck", Status: ActionProposed,
		AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantDelegate, Reason: "Asked for",
			Title: "Deck", Description: "A five-slide deck about bread."}})
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	helper, err := service.StartTask(t.Context(), "prj_test", "act_deck", "Deck", "A five-slide deck about bread.", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", "act_deck", ActionApplied, "Started.", Created{Helper: &helper}); err != nil {
		t.Fatal(err)
	}
	writeOutput(t, service, helper, "deck.html", "<h1>Bread</h1>")
	if finish {
		worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Summary: "deck.html is the deck."})
		Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	}
	return service, worker, store, followed(t, service, helper.ID)
}

func writeOutput(t *testing.T, service *Service, helper Helper, name, contents string) {
	t.Helper()
	path := filepath.Join(service.helperDir(helper), outputs.Dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func followed(t *testing.T, service *Service, id string) Helper {
	t.Helper()
	helper, err := service.LookUpHelper(t.Context(), "prj_test", id)
	if err != nil {
		t.Fatal(err)
	}
	return helper
}

// Feedback on a finished helper's file resumes the same helper's session for
// its next turn in its kept directory, and what it hands back is the file's
// next version, on a line that says so.
func TestReviseResumesTheSameHelper(t *testing.T) {
	service, worker, store, helper := deckHelper(t, true)
	if !helper.Done || !helper.Kept || helper.Session != "thread" {
		t.Fatalf("helper = %+v", helper)
	}
	stored, _ := service.read("prj_test")
	stored.PendingEvents = nil
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	// Mayka hears which files this chat's helper made, at which version.
	if listed := service.outputsInstructions(t.Context(), followedRecord(t, service)); !strings.Contains(listed, "deck.html v1") ||
		!strings.Contains(listed, helper.ID) {
		t.Fatalf("listed = %q", listed)
	}
	// By the file's name, as Mayka may name it.
	result, err := service.ReviseTask(t.Context(), "prj_test", "", "deck.html", "Make the titles blue.")
	if err != nil || !strings.Contains(result, "is making the change") {
		t.Fatalf("result = %q, %v", result, err)
	}
	put := worker.puts[len(worker.puts)-1]
	if put.Mode != workerhttp.AttemptModeResume || put.ProviderSessionID != "thread" ||
		put.Assignment.WorkspaceID != helper.ID || !strings.Contains(put.Instructions, "Make the titles blue.") ||
		!strings.Contains(put.Instructions, "rather than starting over") || worker.attempt.AttemptID != helper.ID+":turn:2" {
		t.Fatalf("put = %+v attempt = %+v", put, worker.attempt.AttemptReference)
	}
	if revised := followed(t, service, helper.ID); revised.Done || revised.Turn != 2 || revised.Change != "Make the titles blue." {
		t.Fatalf("revised = %+v", revised)
	}
	writeOutput(t, service, helper, "deck.html", "<h1 style='color:blue'>Bread</h1>")
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Summary: "Titles are blue."})
	Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	stored, _ = service.read("prj_test")
	last := stored.Messages[len(stored.Messages)-1]
	if last.Text != "The helper on “Deck” made the change: deck.html (v2)." || strings.Join(last.Outputs, ",") != "deck.html" ||
		len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "finished the change you sent") ||
		!strings.Contains(stored.PendingEvents[0], "Titles are blue.") {
		t.Fatalf("last = %+v pending = %v", last, stored.PendingEvents)
	}
	files, _ := store.List(t.Context(), "prj_test")
	if len(files) != 1 || files[0].Version != 2 {
		t.Fatalf("files = %+v", files)
	}
}

func followedRecord(t *testing.T, service *Service) record {
	t.Helper()
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// A change sent while the helper works waits for its turn to end, and its
// next turn makes it at once.
func TestReviseWaitsWhileTheHelperWorks(t *testing.T) {
	service, worker, _, helper := deckHelper(t, false)
	puts := len(worker.puts)
	result, err := service.ReviseTask(t.Context(), "prj_test", "", helper.ID, "Add a slide on rye.")
	if err != nil || !strings.Contains(result, "once its current turn ends") || len(worker.puts) != puts {
		t.Fatalf("result = %q, %v, puts = %d", result, err, len(worker.puts))
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Summary: "Five slides."})
	Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	put := worker.puts[len(worker.puts)-1]
	if len(worker.puts) != puts+1 || put.Mode != workerhttp.AttemptModeResume || !strings.Contains(put.Instructions, "rye") {
		t.Fatalf("puts = %d put = %+v", len(worker.puts), put)
	}
	stored, _ := service.read("prj_test")
	last := stored.Messages[len(stored.Messages)-1]
	if helper := followed(t, service, helper.ID); helper.Done || helper.Turn != 2 || len(helper.Waiting) != 0 ||
		!strings.Contains(last.Text, "now making the change") || len(stored.PendingEvents) != 0 {
		t.Fatalf("helper = %+v last = %q pending = %v", helper, last.Text, stored.PendingEvents)
	}
}

// Once the helper's directory and session are gone (the output marked Done),
// a revise starts a fresh helper from the latest version and the earlier
// briefs, so the work continues; the versions stay.
func TestReviseContinuesInAFreshHelperOnceItsSessionIsGone(t *testing.T) {
	service, worker, store, helper := deckHelper(t, true)
	if err := service.OutputDone(t.Context(), "prj_test", "deck.html"); err != nil {
		t.Fatal(err)
	}
	if ended := followed(t, service, helper.ID); ended.Kept || ended.Session != "" {
		t.Fatalf("ended = %+v", ended)
	}
	if _, err := os.Stat(service.helperDir(helper)); !os.IsNotExist(err) {
		t.Fatalf("the helper's directory is still there: %v", err)
	}
	if files, _ := store.List(t.Context(), "prj_test"); len(files) != 1 {
		t.Fatalf("its files went too: %+v", files)
	}
	if _, err := service.ReviseTask(t.Context(), "prj_test", "", helper.ID, "Shorter, please."); err != nil {
		t.Fatal(err)
	}
	put := worker.puts[len(worker.puts)-1]
	fresh := followed(t, service, helper.ID)
	if put.Mode != workerhttp.AttemptModeStart || put.Assignment.WorkspaceID != helper.ID+"-2" ||
		!strings.Contains(put.Instructions, "A five-slide deck about bread.") || !strings.Contains(put.Instructions, "Shorter, please.") ||
		!strings.Contains(put.Instructions, "deck.html") || fresh.Generation != 1 || fresh.Turn != 2 || fresh.Done {
		t.Fatalf("put = %+v fresh = %+v", put, fresh)
	}
	contents, err := os.ReadFile(filepath.Join(service.helperDir(fresh), outputs.Dir, "deck.html"))
	if err != nil || string(contents) != "<h1>Bread</h1>" {
		t.Fatalf("the fresh helper's deck = %q, %v", contents, err)
	}
	// What it hands back is still the same file's next version.
	writeOutput(t, service, fresh, "deck.html", "<h1>Bread, short</h1>")
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Summary: "Shorter."})
	Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	if files, _ := store.List(t.Context(), "prj_test"); len(files) != 1 || files[0].Version != 2 {
		t.Fatalf("files = %+v", files)
	}
}

// A resumed turn that fails (its provider session was gone, say) is taken
// over once by a fresh helper.
func TestAFailedRevisionContinuesAfresh(t *testing.T) {
	service, worker, _, helper := deckHelper(t, true)
	if _, err := service.ReviseTask(t.Context(), "prj_test", "", helper.ID, "Blue titles."); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeFailed, Summary: "no such session"})
	Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	fresh := followed(t, service, helper.ID)
	put := worker.puts[len(worker.puts)-1]
	if put.Mode != workerhttp.AttemptModeStart || !fresh.Restarted || fresh.Generation != 1 || fresh.Done ||
		!strings.Contains(put.Instructions, "Blue titles.") {
		t.Fatalf("put = %+v fresh = %+v", put, fresh)
	}
	// A fresh helper that fails too stops, and Mayka hears of it.
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeFailed, Summary: "still failing"})
	Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	if stopped := followed(t, service, helper.ID); !stopped.Done || stopped.Kept {
		t.Fatalf("stopped = %+v", stopped)
	}
	if stored, _ := service.read("prj_test"); !strings.Contains(strings.Join(stored.PendingEvents, "\n"), "stopped before it finished") {
		t.Fatalf("pending = %v", stored.PendingEvents)
	}
}

// A helper unrevised for 14 days lets go of its directory; so do the
// helpers of a deleted chat, and a deleted project removes them all.
func TestKeptHelpersEnd(t *testing.T) {
	service, _, _, helper := deckHelper(t, true)
	if err := service.updateShared("prj_test", func(shared *sharedState) bool {
		old := time.Now().Add(-15 * 24 * time.Hour)
		shared.Helpers[0].RevisedAt = &old
		return true
	}); err != nil {
		t.Fatal(err)
	}
	Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	if expiredHelper := followed(t, service, helper.ID); expiredHelper.Kept {
		t.Fatalf("expired = %+v", expiredHelper)
	}
	if _, err := os.Stat(service.helperDir(helper)); !os.IsNotExist(err) {
		t.Fatalf("an expired helper's directory is still there: %v", err)
	}

	service, _, _, helper = deckHelper(t, true)
	if err := service.DeleteChat(t.Context(), "prj_test", helper.Chat); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.helperDir(helper)); !os.IsNotExist(err) {
		t.Fatalf("a deleted chat's helper's directory is still there: %v", err)
	}

	service, _, _, helper = deckHelper(t, true)
	if err := service.DeleteProject(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.helperDir(helper)); !os.IsNotExist(err) {
		t.Fatalf("a deleted project's helper's directory is still there: %v", err)
	}
}

// Done while the helper works ends it once its turn ends.
func TestDoneWhileWorkingEndsAfterTheTurn(t *testing.T) {
	service, worker, _, helper := deckHelper(t, false)
	if err := service.EndTask(t.Context(), "prj_test", helper.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.helperDir(helper)); err != nil {
		t.Fatalf("a working helper's directory went: %v", err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Summary: "Done."})
	Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	if ended := followed(t, service, helper.ID); ended.Kept || !ended.Done || ended.Closing {
		t.Fatalf("ended = %+v", ended)
	}
	if _, err := os.Stat(service.helperDir(helper)); !os.IsNotExist(err) {
		t.Fatalf("the helper's directory is still there: %v", err)
	}
}

// A test of the app is not revised, and an unknown file names no helper.
func TestReviseRefusesWhatItCannotRevise(t *testing.T) {
	service, _, _, _ := deckHelper(t, true)
	if _, err := service.ReviseTask(t.Context(), "prj_test", "", "nothing.md", "Change it."); err == nil {
		t.Fatal("a file no helper made was revised")
	}
	if err := service.updateShared("prj_test", func(shared *sharedState) bool {
		shared.Helpers[0].TestApp = true
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviseTask(context.Background(), "prj_test", "", "deck.html", "Again."); err == nil {
		t.Fatal("a test of the app was revised")
	}
}

// revise is sent at once, and needs its helper and change.
func TestReviseIsSentAtOnce(t *testing.T) {
	if !sentAtOnce(featureartifact.AssistantRevise) {
		t.Fatal("revise waits for a card")
	}
	turn := featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{{
		Type: featureartifact.AssistantRevise, Reason: "Feedback", Helper: "tsk_1"}}}
	if _, err := turn.Normalize(); err == nil {
		t.Fatal("a revise without a change was accepted")
	}
}
