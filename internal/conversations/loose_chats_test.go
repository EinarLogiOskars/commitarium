package conversations

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// finishTitled ends a turn with a reply and the chat title the assistant
// gave.
func (worker *chatsWorker) finishTitled(attemptID, reply, title string) {
	worker.finish(attemptID, reply)
	worker.mu.Lock()
	defer worker.mu.Unlock()
	attempt := worker.attempts[attemptID]
	attempt.Result.AssistantTurn = &featureartifact.AssistantTurn{ChatTitle: title}
	worker.attempts[attemptID] = attempt
}

// answer sends a message in a chat and finishes the assistant's turn with
// reply and title. It answers the instructions the turn ran on.
func answer(t *testing.T, service *Service, worker *chatsWorker, scope, chat, message, key, reply, title string) string {
	t.Helper()
	if _, err := service.Send(InChat(t.Context(), chat), scope, message, key); err != nil {
		t.Fatal(err)
	}
	stored, err := service.readChat(scope, chat)
	if err != nil {
		t.Fatal(err)
	}
	worker.finishTitled(stored.AttemptID, reply, title)
	if _, err := service.Get(InChat(t.Context(), chat), scope); err != nil {
		t.Fatal(err)
	}
	texts := worker.instructions()
	return texts[len(texts)-1]
}

// A loose chat at the top level moves into the project made from it as the
// project's first chat, with its messages and the events it has yet to
// hear; its provider session stays behind, and the top level never makes a
// main chat again, so the old IDs stay unique.
func TestALooseChatMovesIntoItsProject(t *testing.T) {
	service, worker := newChatsService(t)
	loose, err := service.CreateChat(t.Context(), TopLevel, "")
	if err != nil || loose.Chat != MainChat {
		t.Fatalf("chat = %+v err=%v", loose, err)
	}
	instructions := answer(t, service, worker, TopLevel, MainChat, "Could something read my bank's CSVs?", "msg-1",
		"Yes: a small importer would do.", "")
	if !strings.Contains(instructions, "loose chat") {
		t.Fatalf("a loose chat's turn was not told what it is:\n%s", instructions)
	}
	if err := service.updateShared(TopLevel, func(shared *sharedState) bool {
		shared.PendingEvents = map[string][]string{MainChat: {"The research on CSV formats finished."}}
		return true
	}); err != nil {
		t.Fatal(err)
	}

	moved, err := service.MoveChat(t.Context(), TopLevel, MainChat, "prj_bank")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Scope != "prj_bank" || moved.Chat != MainChat || len(moved.Messages) != 2 ||
		moved.Messages[0].Text != "Could something read my bank's CSVs?" {
		t.Fatalf("moved = %+v", moved)
	}
	stored, err := service.readChat("prj_bank", MainChat)
	if err != nil || stored.ProviderSessionID != "" || len(stored.PendingEvents) != 1 {
		t.Fatalf("moved chat = %+v err=%v", stored, err)
	}
	if left, err := service.Chats(t.Context(), TopLevel, true); err != nil || len(left) != 0 {
		t.Fatalf("the top level still has %+v err=%v", left, err)
	}
	if shared, _ := service.sharedOf(TopLevel); len(shared.PendingEvents) != 0 {
		t.Fatalf("the top level kept the chat's events: %v", shared.PendingEvents)
	}
	next, err := service.CreateChat(t.Context(), TopLevel, "")
	if err != nil || next.Chat == MainChat {
		t.Fatalf("the top level made main again: %+v err=%v", next, err)
	}

	// The next turn in the project starts a fresh session there, which reads
	// the chat so far and hears the event.
	instructions = answer(t, service, worker, "prj_bank", MainChat, "Go on", "msg-2", "Planning it.", "")
	if !strings.Contains(instructions, "Could something read my bank's CSVs?") ||
		!strings.Contains(instructions, "The research on CSV formats finished.") {
		t.Fatalf("the project's turn did not read the chat:\n%s", instructions)
	}
	if _, err := service.MoveChat(t.Context(), TopLevel, MainChat, "prj_bank"); err == nil {
		t.Fatal("a chat that moved away moved again")
	}
}

// A chat titled from its first message is named by the assistant on its
// next turn, once; the assistant is asked only while it may name it.
func TestTheAssistantNamesAChat(t *testing.T) {
	service, worker := newChatsService(t)
	if _, err := service.CreateChat(t.Context(), "prj_test", ""); err != nil {
		t.Fatal(err)
	}
	instructions := answer(t, service, worker, "prj_test", MainChat, "you can continue", "msg-1", "On it.", "Sign-in bug")
	if !strings.Contains(instructions, "This chat has no name yet") {
		t.Fatalf("the assistant was not asked to name the chat:\n%s", instructions)
	}
	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil || conversation.Title != "Sign-in bug" {
		t.Fatalf("title = %q err=%v", conversation.Title, err)
	}
	instructions = answer(t, service, worker, "prj_test", MainChat, "And the reset link?", "msg-2", "Also that.", "Password reset")
	if strings.Contains(instructions, "This chat has no name yet") {
		t.Fatal("the assistant was asked to name a named chat")
	}
	if conversation, _ := service.Get(t.Context(), "prj_test"); conversation.Title != "Sign-in bug" {
		t.Fatalf("the assistant renamed its own name: %q", conversation.Title)
	}
}

// The assistant never overwrites a name the user gave a chat, by renaming
// it or when starting it.
func TestTheAssistantNeverRenamesTheUsersChat(t *testing.T) {
	service, worker := newChatsService(t)
	if _, err := service.CreateChat(t.Context(), "prj_test", ""); err != nil {
		t.Fatal(err)
	}
	answer(t, service, worker, "prj_test", MainChat, "1. Just me", "msg-1", "Noted.", "")
	if _, err := service.RenameChat(t.Context(), "prj_test", MainChat, "1. Just me"); err != nil {
		t.Fatal(err)
	}
	instructions := answer(t, service, worker, "prj_test", MainChat, "Next", "msg-2", "Sure.", "Team size")
	if strings.Contains(instructions, "This chat has no name yet") {
		t.Fatal("the assistant was asked to name the user's chat")
	}
	if conversation, _ := service.Get(t.Context(), "prj_test"); conversation.Title != "1. Just me" {
		t.Fatalf("the user's name was overwritten: %q", conversation.Title)
	}

	named, err := service.CreateChat(t.Context(), "prj_test", "Pricing")
	if err != nil {
		t.Fatal(err)
	}
	answer(t, service, worker, "prj_test", named.Chat, "What should it cost?", "msg-3", "Ten a month.", "Monthly price")
	if conversation, _ := service.Get(InChat(t.Context(), named.Chat), "prj_test"); conversation.Title != "Pricing" {
		t.Fatalf("the user's name was overwritten: %q", conversation.Title)
	}
}
