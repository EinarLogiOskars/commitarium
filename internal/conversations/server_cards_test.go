package conversations

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// The server's facts reach every chat's turn, not only one opened on the
// server, so Mayka never asks for an address the server already reports.
func TestServerFactsReachEveryTurn(t *testing.T) {
	service, worker := newTestService(t)
	service.SetServerFacts("Domain: example.com\nDeployed apps: notes.example.com (open to anyone, this project's app)")
	if _, err := service.Send(t.Context(), "prj_test", "Make notes private", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if instructions := worker.puts[0].Instructions; !strings.Contains(instructions, "notes.example.com (open to anyone, this project's app)") ||
		!strings.Contains(instructions, "What the user's server last reported") {
		t.Fatalf("the turn does not carry the server's facts:\n%s", instructions)
	}
}

// A card the app applied and the server refused wakes Mayka at once to
// explain it; one that went through waits for the user's next message.
func TestAFailedServerCardWakesMayka(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "Add example.com and deploy", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Two cards.",
		AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantAddDomain, Reason: "Asked", Domain: "example.com", How: "wildcard", Asked: true},
			{Type: featureartifact.AssistantDeployApp, Reason: "Asked", Asked: true},
		}},
	})
	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil || len(conversation.Actions) != 2 || !conversation.Actions[0].Asked {
		t.Fatalf("after the turn = %+v err=%v", conversation, err)
	}
	pending := func() []string {
		stored, done, err := service.open("prj_test", "")
		if err != nil {
			t.Fatal(err)
		}
		defer done()
		return stored.PendingEvents
	}
	if _, err := service.Resolve(t.Context(), "prj_test", conversation.Actions[1].ID, ActionApplied,
		"Deploying the latest version for visitors. Deployed at notes.example.com.", Created{}); err != nil {
		t.Fatal(err)
	}
	if events := pending(); len(events) != 0 {
		t.Fatalf("Mayka was woken while the reply's other change was still being applied: %+v", events)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", conversation.Actions[0].ID, ActionFailed,
		"Adding example.com. The server refused: *.example.com does not point at this server yet.", Created{}); err != nil {
		t.Fatal(err)
	}
	events := pending()
	if len(events) != 1 || !strings.Contains(events[0], "does not point at this server yet") ||
		!strings.Contains(events[0], "Deployed at notes.example.com") {
		t.Fatalf("the settled changes did not wake Mayka once, with both outcomes: %+v", events)
	}
}

func TestAnAskedChangeThatWentThroughWakesMayka(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "Add notes.example.com as well", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Deploying.",
		AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantDeployApp, Reason: "Asked", Asked: true},
			{Type: featureartifact.AssistantSetAppAccess, Reason: "Her idea", App: "notes.example.com", How: "public"},
		}},
	})
	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil || len(conversation.Actions) != 2 {
		t.Fatalf("after the turn = %+v err=%v", conversation, err)
	}
	// A card the user approves by hand needs no word from Mayka.
	if _, err := service.Resolve(t.Context(), "prj_test", conversation.Actions[1].ID, ActionApplied,
		"notes.example.com is now open to anyone.", Created{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(t.Context(), "prj_test", conversation.Actions[0].ID, ActionApplied,
		"Deploying the latest version for visitors. Deployed at notes.example.com.", Created{}); err != nil {
		t.Fatal(err)
	}
	stored, done, err := service.open("prj_test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "went through") ||
		strings.Contains(stored.PendingEvents[0], "open to anyone") {
		t.Fatalf("the asked change did not wake Mayka alone: %+v", stored.PendingEvents)
	}
}
