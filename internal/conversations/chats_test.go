package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// chatsWorker keeps each attempt by its ID, so several chats' turns run at
// once. While hold is set, the first turn it is given waits in PutAttempt
// until hold is closed, and entered says it got there.
type chatsWorker struct {
	mu       sync.Mutex
	attempts map[string]workerhttp.Attempt
	puts     []workerhttp.PutAttemptRequest
	hold     chan struct{}
	entered  chan struct{}
}

func (worker *chatsWorker) PutAttempt(_ context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error) {
	if err := request.Validate(identity); err != nil {
		return workerhttp.Attempt{}, false, err
	}
	worker.mu.Lock()
	hold := worker.hold
	worker.hold = nil
	worker.mu.Unlock()
	if hold != nil {
		close(worker.entered)
		<-hold
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.attempts == nil {
		worker.attempts = map[string]workerhttp.Attempt{}
	}
	worker.puts = append(worker.puts, request)
	attempt := workerhttp.Attempt{
		AttemptReference: identity.AttemptReference, Mode: request.Mode, Assignment: request.Assignment,
		ProviderSessionID: "thread-" + identity.SessionID, State: workerhttp.AttemptStateRunning,
	}
	worker.attempts[identity.AttemptID] = attempt
	return attempt, true, nil
}

func (worker *chatsWorker) GetAttempt(_ context.Context, reference workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.attempts[reference.AttemptID], nil
}

func (*chatsWorker) SendCommand(context.Context, workerhttp.MutationIdentity, workerhttp.CommandRequest) (workerhttp.Attempt, error) {
	panic("unexpected command")
}

func (*chatsWorker) ForceStop(context.Context, workerhttp.MutationIdentity, workerhttp.ForceStopRequest) (workerhttp.Attempt, error) {
	panic("unexpected force stop")
}

func (*chatsWorker) Supersede(context.Context, workerhttp.MutationIdentity, workerhttp.SupersedeRequest) (workerhttp.Attempt, error) {
	panic("unexpected supersede")
}

func (worker *chatsWorker) finish(attemptID, reply string) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	attempt := worker.attempts[attemptID]
	attempt.State = workerhttp.AttemptStateTerminal
	attempt.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: reply,
		AssistantTurn: &featureartifact.AssistantTurn{},
	}
	worker.attempts[attemptID] = attempt
}

func (worker *chatsWorker) instructions() []string {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	texts := make([]string, 0, len(worker.puts))
	for _, put := range worker.puts {
		texts = append(texts, put.Instructions)
	}
	return texts
}

// newChatsService is an assistant set up on the codex agent, on a worker
// that runs many turns at once.
func newChatsService(t *testing.T) (*Service, *chatsWorker) {
	t.Helper()
	worker := &chatsWorker{}
	service, err := NewService(Config{
		Root: t.TempDir(), WorkspaceRoot: t.TempDir(), Projects: projectsStub{}, Agents: agentsStub{},
		Describer: describerStub{},
		Workers:   func(string) (workerhttp.Service, error) { return worker, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveSettings(t.Context(), Settings{Agent: "codex", Model: "codex-default"}); err != nil {
		t.Fatal(err)
	}
	return service, worker
}

// A project's single conversation from before chats becomes its main chat,
// named after its first message, with its IDs kept; a migration stopped
// part way runs again, and one that finished is not redone.
func TestASingleConversationBecomesTheMainChat(t *testing.T) {
	service, _ := newChatsService(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	legacy := record{
		Conversation: Conversation{
			Scope: "prj_test", Status: StatusIdle, Notes: "Likes dark themes.", UpdatedAt: at,
			Messages: []Message{
				{ID: "msg_prj_test_1", Role: "user", Text: "Add a dark theme\nwith a toggle in the header", OccurredAt: at},
				{ID: "msg_prj_test_2", Role: "assistant", Text: "I drafted it.", OccurredAt: at},
			},
			Actions: []Action{{
				ID: "act_prj_test_1_1", MessageID: "msg_prj_test_2", Status: ActionApplied, UpdatedAt: at,
				AssistantAction: featureartifact.AssistantAction{
					Type: featureartifact.AssistantCreateWorkOrder, Title: "Dark theme", WorkOrderID: "fea_dark",
				},
			}},
		},
		Turn: 1, Session: 1, ProviderSessionID: "thread",
		Watched:       map[string]string{"fea_dark": "implementing"},
		PendingEvents: []string{"“Dark theme” failed."},
	}
	conversations := filepath.Join(service.config.Root, "conversations")
	writeLegacy := func(stored record) {
		t.Helper()
		encoded, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(conversations, "prj_test.json"), encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeLegacy(legacy)
	// Stopped after writing a chat but before the project's state: it runs
	// again.
	if err := os.MkdirAll(filepath.Join(conversations, "prj_test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conversations, "prj_test", "main.json"), []byte(`{"id":"main","scope":"prj_test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Chat != MainChat || conversation.Title != "Add a dark theme" || len(conversation.Messages) != 2 ||
		conversation.Notes != "Likes dark themes." || len(conversation.Actions) != 1 ||
		conversation.Actions[0].ID != "act_prj_test_1_1" {
		t.Fatalf("migrated = %+v", conversation)
	}
	if _, err := os.Stat(filepath.Join(conversations, "prj_test.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old conversation is still there: %v", err)
	}
	stored, err := service.read("prj_test")
	if err != nil || stored.Turn != 1 || stored.ProviderSessionID != "thread" || len(stored.PendingEvents) != 1 ||
		stored.Watched["fea_dark"] != "implementing" {
		t.Fatalf("migrated record = %+v err=%v", stored, err)
	}
	chats, err := service.Chats(t.Context(), "prj_test", false)
	if err != nil || len(chats) != 1 || chats[0].ID != MainChat || chats[0].Title != "Add a dark theme" {
		t.Fatalf("chats = %+v err=%v", chats, err)
	}
	// Its next message keeps numbering the conversation's IDs.
	if _, err := service.Send(t.Context(), "prj_test", "And a light one?", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if stored, _ := service.read("prj_test"); stored.Messages[2].ID != "msg_prj_test_3" {
		t.Fatalf("the next message is %q", stored.Messages[2].ID)
	}

	// Stopped after the project's state was written: the old file is only
	// removed, and what the chats wrote since stands.
	legacy.Notes = "Old notes."
	writeLegacy(legacy)
	if conversation, err := service.Get(t.Context(), "prj_test"); err != nil || conversation.Notes != "Likes dark themes." ||
		len(conversation.Messages) != 3 {
		t.Fatalf("a finished migration ran again: %+v err=%v", conversation, err)
	}
	if _, err := os.Stat(filepath.Join(conversations, "prj_test.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old conversation is still there: %v", err)
	}
}

// Two chats in a project answer at once: one chat's slow turn start does
// not hold up the other, each keeps its own reply, and each turn hears what
// the other chat said.
func TestTwoChatsAnswerAtOnce(t *testing.T) {
	service, worker := newChatsService(t)
	first, err := service.CreateChat(t.Context(), "prj_test", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateChat(t.Context(), "prj_test", "Pricing")
	if err != nil || first.Chat != MainChat || second.Chat == MainChat || second.Title != "Pricing" {
		t.Fatalf("chats = %q and %+v err=%v", first.Chat, second, err)
	}

	hold := make(chan struct{})
	worker.hold, worker.entered = hold, make(chan struct{})
	held := make(chan error, 1)
	go func() {
		_, err := service.Send(InChat(t.Context(), MainChat), "prj_test", "Plan the API", "msg-a")
		held <- err
	}()
	<-worker.entered
	answered := make(chan error, 1)
	go func() {
		conversation, err := service.Send(InChat(t.Context(), second.Chat), "prj_test", "What should it cost?", "msg-b")
		if err == nil && (conversation.Status != StatusRunning || conversation.Chat != second.Chat) {
			err = errors.New("the second chat did not start its turn: " + string(conversation.Status))
		}
		answered <- err
	}()
	select {
	case err := <-answered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second chat waited for the first chat's turn")
	}
	close(hold)
	if err := <-held; err != nil {
		t.Fatal(err)
	}

	main, err := service.readChat("prj_test", MainChat)
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.readChat("prj_test", second.Chat)
	if err != nil {
		t.Fatal(err)
	}
	if main.Status != StatusRunning || other.Status != StatusRunning || main.AttemptID == other.AttemptID {
		t.Fatalf("turns = %s %q and %s %q", main.Status, main.AttemptID, other.Status, other.AttemptID)
	}
	// The second turn started after the first chat's message, so it heard of
	// it.
	instructions := worker.instructions()
	if len(instructions) != 2 {
		t.Fatalf("%d turns", len(instructions))
	}
	heard := false
	for _, text := range instructions {
		if strings.Contains(text, "The user says:\nWhat should it cost?") {
			heard = strings.Contains(text, "the user said “Plan the API”")
		}
	}
	if !heard {
		t.Fatalf("the second chat's turn did not hear of the first chat: %q", instructions)
	}

	worker.finish(main.AttemptID, "Here is the API plan.")
	worker.finish(other.AttemptID, "Ten a month.")
	// The first chat's reply is folded in last, after the second chat's.
	for _, turn := range []struct{ chat, reply string }{{second.Chat, "Ten a month."}, {MainChat, "Here is the API plan."}} {
		chat, reply := turn.chat, turn.reply
		conversation, err := service.Get(InChat(t.Context(), chat), "prj_test")
		if err != nil || conversation.Status != StatusIdle || len(conversation.Messages) != 2 ||
			conversation.Messages[1].Text != reply {
			t.Fatalf("chat %s = %+v err=%v", chat, conversation, err)
		}
	}
	// A third turn in the second chat hears what the first chat answered.
	if _, err := service.Send(InChat(t.Context(), second.Chat), "prj_test", "And yearly?", "msg-c"); err != nil {
		t.Fatal(err)
	}
	if last := worker.instructions()[2]; !strings.Contains(last, "you answered “Here is the API plan.”") {
		t.Fatalf("the turn did not hear of the other chat:\n%s", last)
	}
}

// An event about a work order goes to the chat whose action created or
// started it, one about the goal's work to the chat that started the goal,
// and the rest to the most recently used open chat; each chat wakes on its
// own events.
func TestEventsGoToTheChatWhereTheWorkStarted(t *testing.T) {
	service, worker := newChatsService(t)
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	ids := map[string]string{}
	for _, name := range []string{"creates", "starts", "goal", "archived", "recent"} {
		created, err := service.CreateChat(t.Context(), "prj_test", name)
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = created.Chat
	}
	applied := func(chat string, kind featureartifact.AssistantActionType, workOrderID string) {
		t.Helper()
		stored, done, err := service.open("prj_test", ids[chat])
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		stored.Actions = append(stored.Actions, Action{
			ID: "act_" + chat + "_" + workOrderID, Status: ActionApplied, UpdatedAt: service.now(),
			AssistantAction: featureartifact.AssistantAction{Type: kind, WorkOrderID: workOrderID},
		})
		if err := service.write(stored); err != nil {
			t.Fatal(err)
		}
	}
	applied("creates", featureartifact.AssistantCreateWorkOrder, "fea_made")
	// Created in one chat and started in another, it stays with its creator.
	applied("starts", featureartifact.AssistantStartWorkOrder, "fea_made")
	applied("starts", featureartifact.AssistantStartWorkOrder, "fea_started")
	applied("archived", featureartifact.AssistantCreateWorkOrder, "fea_archived")
	if err := service.updateShared("prj_test", func(shared *sharedState) bool {
		shared.Goal = &Goal{Title: "Finish P0", Status: GoalActive, Chat: ids["goal"], Handed: []string{"fea_goal"}}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ArchiveChat(t.Context(), "prj_test", ids["archived"]); err != nil {
		t.Fatal(err)
	}
	// The last chat the user wrote in is the most recently used.
	if _, err := service.Send(InChat(t.Context(), ids["recent"]), "prj_test", "Hello", "msg-1"); err != nil {
		t.Fatal(err)
	}
	stored, _ := service.readChat("prj_test", ids["recent"])
	worker.finish(stored.AttemptID, "Hi.")

	workOrders := []string{"fea_made", "fea_started", "fea_goal", "fea_archived", "fea_other"}
	look := func(status execution.RunStatus) {
		t.Helper()
		if err := service.observe(t.Context(), "prj_test", func(context.Context, string) (view, error) {
			seen := view{Work: map[string]work{}}
			for _, id := range workOrders {
				seen.Work[id] = work{Title: id, State: feature.StateImplementing, RunID: "run_" + id, RunStatus: status}
			}
			return seen, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	look(execution.RunStatusRunning)
	look(execution.RunStatusFailed)

	expected := map[string][]string{
		"creates": {"fea_made"}, "starts": {"fea_started"}, "goal": {"fea_goal"},
		"recent": {"fea_archived", "fea_other"}, "archived": nil,
	}
	for name, want := range expected {
		stored, err := service.readChat("prj_test", ids[name])
		if err != nil {
			t.Fatal(err)
		}
		heard := []string{}
		for _, message := range stored.Messages {
			if message.Role == "event" && strings.HasSuffix(message.Text, " failed.") {
				heard = append(heard, strings.Trim(strings.TrimSuffix(message.Text, " failed."), `"`))
			}
		}
		if strings.Join(heard, ",") != strings.Join(want, ",") {
			t.Fatalf("chat %q heard %v, want %v", name, heard, want)
		}
		// Each chat that heard of a failure woke on it.
		if (len(want) > 0) != (stored.Status == StatusRunning) || len(stored.PendingEvents) != 0 {
			t.Fatalf("chat %q is %s with %d events waiting", name, stored.Status, len(stored.PendingEvents))
		}
	}

	// The scope's own routes find an action in whichever chat holds it.
	action, err := service.Action(t.Context(), "prj_test", "act_starts_fea_started")
	if err != nil || action.Chat != ids["starts"] {
		t.Fatalf("action = %+v err=%v", action, err)
	}
}

// The scope's own calls act on the most recently used open chat: the one
// the user last wrote in, and once it is archived the one before.
func TestTheScopesOwnCallsActOnTheMostRecentChat(t *testing.T) {
	service, _ := newChatsService(t)
	clock := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	first, _ := service.CreateChat(t.Context(), "prj_test", "")
	second, _ := service.CreateChat(t.Context(), "prj_test", "")
	if conversation, err := service.Get(t.Context(), "prj_test"); err != nil || conversation.Chat != second.Chat {
		t.Fatalf("most recent = %q err=%v", conversation.Chat, err)
	}
	if _, err := service.Send(InChat(t.Context(), first.Chat), "prj_test", "Back to this one", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if conversation, _ := service.Get(t.Context(), "prj_test"); conversation.Chat != first.Chat ||
		conversation.Title != "Back to this one" {
		t.Fatalf("most recent = %q %q", conversation.Chat, conversation.Title)
	}
	if _, err := service.ArchiveChat(t.Context(), "prj_test", first.Chat); err != nil {
		t.Fatal(err)
	}
	if conversation, _ := service.Get(t.Context(), "prj_test"); conversation.Chat != second.Chat {
		t.Fatalf("after archiving, most recent = %q", conversation.Chat)
	}
	if _, err := service.Get(InChat(t.Context(), "chat_0000000000000000"), "prj_test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing chat answered %v", err)
	}
	if _, err := service.RenameChat(t.Context(), "prj_test", second.Chat, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty title answered %v", err)
	}
	if renamed, err := service.RenameChat(t.Context(), "prj_test", second.Chat, "Pricing"); err != nil || renamed.Title != "Pricing" {
		t.Fatalf("renamed = %+v err=%v", renamed, err)
	}
	if open, _ := service.Chats(t.Context(), "prj_test", false); len(open) != 1 || open[0].Title != "Pricing" {
		t.Fatalf("open chats = %+v", open)
	}
	if every, _ := service.Chats(t.Context(), "prj_test", true); len(every) != 2 {
		t.Fatalf("every chat = %+v", every)
	}
}
