package conversations

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// signInsStub is the work orders' record of agents needing a sign-in.
type signInsStub struct {
	known    bool
	marked   []string
	answered []string
}

func (stub *signInsStub) MarkAgentNeedsSignIn(_ context.Context, agentID, _ string) error {
	stub.marked = append(stub.marked, agentID)
	stub.known = true
	return nil
}

func (stub *signInsStub) AgentNeedsSignIn(context.Context, string) bool { return stub.known }

func (stub *signInsStub) AgentAnswered(_ context.Context, agentID string) {
	stub.answered = append(stub.answered, agentID)
}

var loginExpired = workerhttp.TerminalResult{
	Outcome: workerhttp.OutcomeFailed, Summary: "Failed to authenticate",
	Error: &workerhttp.ProtocolError{
		Code: workerhttp.ErrorProviderLoginRequired, Message: "OAuth session expired and could not be refreshed",
	},
}

func lastEvent(conversation Conversation) string {
	for index := len(conversation.Messages) - 1; index >= 0; index-- {
		if conversation.Messages[index].Role == "event" {
			return conversation.Messages[index].Text
		}
	}
	return ""
}

// A turn that fails on its agent's login keeps the user's message, tries
// again by itself, then waits for a sign-in, and is sent once the agent
// works again (LOGIN_RECOVERY_PLAN.md).
func TestALoginFailureKeepsTheMessageUntilTheAgentWorks(t *testing.T) {
	service, worker := newTestService(t)
	signIns := &signInsStub{}
	service.config.SignIns = signIns
	if _, err := service.Send(t.Context(), "prj_test", "Add a dark mode", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(loginExpired)
	waiting, err := service.Get(t.Context(), "prj_test")
	if err != nil || waiting.SignIn == nil || waiting.SignIn.NeedsUser || waiting.SignIn.RetryAt == nil ||
		!strings.Contains(lastEvent(waiting), "Your message is kept") || strings.Contains(lastEvent(waiting), "again in a moment") {
		t.Fatalf("waiting = %+v (last event %q) err=%v", waiting.SignIn, lastEvent(waiting), err)
	}
	now := time.Now()
	if err := service.retrySignIn(t.Context(), "prj_test", false, now); err != nil || len(worker.puts) != 1 {
		t.Fatalf("tried again before it was due: puts=%d err=%v", len(worker.puts), err)
	}
	// Each try fails the same way; once they are spent, the user is asked.
	for try := 1; try <= len(signInBackoff); try++ {
		if err := service.retrySignIn(t.Context(), "prj_test", false, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if len(worker.puts) != try+1 || !strings.Contains(worker.puts[try].Instructions, "Add a dark mode") {
			t.Fatalf("try %d: puts=%d", try, len(worker.puts))
		}
		worker.finish(loginExpired)
		if _, err := service.Get(t.Context(), "prj_test"); err != nil {
			t.Fatal(err)
		}
	}
	asked, _ := service.Get(t.Context(), "prj_test")
	if asked.SignIn == nil || !asked.SignIn.NeedsUser || len(signIns.marked) != 1 ||
		!strings.Contains(lastEvent(asked), "needs you to sign in again") {
		t.Fatalf("asked = %+v marked=%v last event %q", asked.SignIn, signIns.marked, lastEvent(asked))
	}
	if err := service.retrySignIn(t.Context(), "prj_test", false, now.Add(24*time.Hour)); err != nil ||
		len(worker.puts) != len(signInBackoff)+1 {
		t.Fatalf("a turn waiting for the user tried by itself: puts=%d err=%v", len(worker.puts), err)
	}
	// The agent signs in: the kept message is sent, and the answer clears it.
	service.AgentSignedIn(t.Context(), "codex")
	if len(worker.puts) != len(signInBackoff)+2 || !strings.Contains(worker.puts[len(worker.puts)-1].Instructions, "Add a dark mode") {
		t.Fatalf("the kept message was not sent after the sign-in: puts=%d", len(worker.puts))
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Summary: "Dark mode is on its way."})
	answered, _ := service.Get(t.Context(), "prj_test")
	if answered.SignIn != nil || len(signIns.answered) != 1 || signIns.answered[0] != "codex" {
		t.Fatalf("answered = %+v answered=%v", answered.SignIn, signIns.answered)
	}
}

// An agent the work orders already know needs a sign-in waits at once, and
// what the user says meanwhile goes with the kept message.
func TestAKnownSignedOutAgentWaitsAndNewMessagesJoinTheKeptOne(t *testing.T) {
	service, worker := newTestService(t)
	service.config.SignIns = &signInsStub{known: true}
	if _, err := service.Send(t.Context(), "prj_test", "Add a dark mode", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(loginExpired)
	waiting, _ := service.Get(t.Context(), "prj_test")
	if waiting.SignIn == nil || !waiting.SignIn.NeedsUser {
		t.Fatalf("a known signed-out agent tried by itself: %+v", waiting.SignIn)
	}
	stored, err := service.read("prj_test")
	if err != nil || stored.free() {
		t.Fatalf("a conversation waiting for a sign-in counts as free: err=%v", err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "And bigger buttons", "msg-2"); err != nil {
		t.Fatal(err)
	}
	last := worker.puts[len(worker.puts)-1].Instructions
	if !strings.Contains(last, "Add a dark mode") || !strings.Contains(last, "And bigger buttons") {
		t.Fatalf("the new message left the kept one behind: %q", last)
	}
}
