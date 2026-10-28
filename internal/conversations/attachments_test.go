package conversations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/documents"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

const shot = "screenshot-2026-10-28-1612.png"

// withAttachmentStore gives the assistant a documents store holding a
// screenshot and a note for owner.
func withAttachmentStore(t *testing.T, service *Service, owner string) {
	t.Helper()
	store, err := documents.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{shot: "\x89PNG", "notes.txt": "the header is off"} {
		if _, err := store.Attach(t.Context(), owner, name, []byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	service.config.Attachments = store
}

// A message's files land in the turn's working directory, the prompt names
// them, and the turn looks at the screenshot as an image. The top-level
// conversation, which has no project, keeps them too.
func TestAMessagesFilesReachTheAssistantsTurn(t *testing.T) {
	service, worker := newTestService(t)
	withAttachmentStore(t, service, TopLevel)
	conversation, err := service.SendAttached(t.Context(), TopLevel, "What is wrong here?", "msg-1",
		[]string{shot, "notes.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := conversation.Messages[0].Attachments; !slices.Equal(got, []string{shot, "notes.txt"}) {
		t.Fatalf("the message records %v", got)
	}
	if len(worker.puts) != 1 {
		t.Fatalf("puts = %d", len(worker.puts))
	}
	put := worker.puts[0]
	if !slices.Equal(put.Images, []string{documents.Dir + "/" + shot}) {
		t.Fatalf("the turn looks at %v", put.Images)
	}
	if !strings.Contains(put.Instructions, "What is wrong here?\n\nAttached, in your working directory: "+
		documents.Dir+"/"+shot+", "+documents.Dir+"/notes.txt") {
		t.Fatalf("the prompt does not name the files:\n%s", put.Instructions)
	}
	directory := filepath.Join(service.config.WorkspaceRoot, put.Assignment.WorkspaceID, filepath.FromSlash(documents.Dir))
	for _, name := range []string{shot, "notes.txt"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("%s is not in the turn's working directory: %v", name, err)
		}
	}
	if _, err := service.SendAttached(t.Context(), TopLevel, "And this?", "msg-2", []string{"gone.png"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a missing file = %v", err)
	}
	if _, err := service.SendAttached(t.Context(), TopLevel, "", "msg-3", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty message = %v", err)
	}
}

// A screenshot sent while the assistant answers waits with its message and
// reaches the next turn; a file alone is a message.
func TestAQueuedMessageKeepsItsFiles(t *testing.T) {
	service, worker := newTestService(t)
	withAttachmentStore(t, service, "prj_test")
	if _, err := service.Send(t.Context(), "prj_test", "Build it", "msg-1"); err != nil {
		t.Fatal(err)
	}
	queued, err := service.SendAttached(t.Context(), "prj_test", "", "msg-2", []string{shot})
	if err != nil || len(queued.Queued) != 1 || !slices.Equal(queued.Queued[0].Attachments, []string{shot}) {
		t.Fatalf("queued = %+v err=%v", queued.Queued, err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Building."})
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	snapshot := func(context.Context, string) (view, error) { return view{Work: map[string]work{}}, nil }
	if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
		t.Fatal(err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	if len(worker.puts) != 2 || !slices.Equal(worker.puts[1].Images, []string{documents.Dir + "/" + shot}) ||
		!strings.Contains(worker.puts[1].Instructions, documents.Attached([]string{shot})) {
		t.Fatalf("the next turn did not carry the screenshot: %+v", worker.puts[len(worker.puts)-1])
	}
	last := conversation.Messages[len(conversation.Messages)-1]
	if !slices.Equal(last.Attachments, []string{shot}) {
		t.Fatalf("the sent message records %v", last.Attachments)
	}
	// The turn after carries only its own message's files.
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Yes."})
	if _, err := service.Send(t.Context(), "prj_test", "Thanks", "msg-3"); err != nil {
		t.Fatal(err)
	}
	if put := worker.puts[len(worker.puts)-1]; len(put.Images) != 0 {
		t.Fatalf("a later turn looks at %v", put.Images)
	}
}
