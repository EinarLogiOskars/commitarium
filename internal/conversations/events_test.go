package conversations

import (
	"context"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestChangesWakeTheAssistantOnlyForWhatMatters(t *testing.T) {
	waiting := func(kind execution.RunWaitKind) work {
		return work{Title: "Log", State: feature.StateImplementing, RunID: "run_1",
			RunStatus: execution.RunStatusWaitingForUser, WaitKind: kind, Reason: "Needs a decision.\nMore."}
	}
	limited := waiting(execution.RunWaitKindBlocker)
	limited.LimitAgent = "codex"
	retrying := waiting(execution.RunWaitKindBlocker)
	retrying.Retrying = true
	autoMerge := waiting(execution.RunWaitKindMergeGate)
	autoMerge.AutoMerge = true
	session := waiting(execution.RunWaitKindClarification)
	session.Session = true
	for name, test := range map[string]struct {
		now  work
		text string
		wake bool
	}{
		"merged":           {work{Title: "Log", State: feature.StateCompleted}, `"Log" merged.`, true},
		"blocked":          {waiting(execution.RunWaitKindBlocker), `"Log" is blocked: Needs a decision.`, true},
		"merge approval":   {waiting(execution.RunWaitKindMergeGate), `"Log" is ready to merge and waits for the user's approval.`, true},
		"usage limit":      {limited, `"Log" is waiting for codex's usage limit to reset.`, false},
		"retrying":         {retrying, `"Log" was interrupted and is retrying automatically.`, false},
		"auto merge gate":  {autoMerge, "", false},
		"session waiting":  {session, "", false},
		"paused by a user": {waiting(execution.RunWaitKindPaused), "", false},
		"running":          {work{Title: "Log", State: feature.StateImplementing, RunID: "run_1", RunStatus: execution.RunStatusRunning}, "", false},
	} {
		events := changes(map[string]string{"fea_1": "implementing|run_1|running||"}, map[string]work{"fea_1": test.now})
		if test.text == "" {
			if len(events) != 0 {
				t.Errorf("%s: unexpected events %+v", name, events)
			}
			continue
		}
		if len(events) != 1 || events[0].Text != test.text || events[0].Wake != test.wake {
			t.Errorf("%s: events = %+v", name, events)
		}
	}
	if events := changes(nil, map[string]work{"fea_1": {Title: "Log", State: feature.StateCompleted}}); len(events) != 0 {
		t.Fatalf("the first look reported events: %+v", events)
	}
	unchanged := map[string]work{"fea_1": {Title: "Log", State: feature.StateCompleted}}
	if events := changes(map[string]string{"fea_1": "completed"}, unchanged); len(events) != 0 {
		t.Fatalf("an unchanged work order reported events: %+v", events)
	}
}

func TestEventsWakeTheAssistantOneTurnAtATime(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "Hi", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Hello."})
	current := map[string]work{"fea_1": {Title: "Log", State: feature.StateImplementing, RunID: "run_1", RunStatus: execution.RunStatusRunning}}
	snapshot := func(context.Context, string) (view, error) { return view{Work: current}, nil }
	observe := func() Conversation {
		t.Helper()
		if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
			t.Fatal(err)
		}
		conversation, err := service.Get(t.Context(), "prj_test")
		if err != nil {
			t.Fatal(err)
		}
		return conversation
	}

	if conversation := observe(); conversation.Status != StatusIdle || len(worker.puts) != 1 {
		t.Fatalf("the first look woke the assistant: %+v", conversation)
	}
	current = map[string]work{"fea_1": {Title: "Log", State: feature.StateCompleted}}
	conversation := observe()
	if conversation.Status != StatusRunning || len(worker.puts) != 2 ||
		!strings.Contains(worker.puts[1].Instructions, `"Log" merged.`) ||
		conversation.Messages[len(conversation.Messages)-1].Text != `"Log" merged.` {
		t.Fatalf("a merge did not wake the assistant: %+v", conversation)
	}

	// While it answers, a new event waits for the next turn.
	current["fea_2"] = work{Title: "Sync", State: feature.StateImplementing, RunID: "run_2", RunStatus: execution.RunStatusRunning}
	observe()
	current["fea_2"] = work{Title: "Sync", State: feature.StateImplementing, RunID: "run_2",
		RunStatus: execution.RunStatusFailed, Reason: "Tests failed."}
	if conversation := observe(); conversation.Status != StatusRunning || len(worker.puts) != 2 {
		t.Fatalf("an event interrupted the assistant's turn: %+v", conversation)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Merged."})
	if conversation := observe(); conversation.Status != StatusRunning || len(worker.puts) != 3 ||
		!strings.Contains(worker.puts[2].Instructions, `"Sync" failed: Tests failed.`) {
		t.Fatalf("the waiting event was not delivered: %+v", conversation)
	}
}

func TestAQueuedMessageStartsTheNextTurn(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "Build it", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Send(t.Context(), "prj_test", "Is the device voice enough?", "msg-2"); err != nil {
		t.Fatal(err)
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
	if len(worker.puts) != 2 || !strings.Contains(worker.puts[1].Instructions, "Is the device voice enough?") ||
		len(conversation.Queued) != 0 || conversation.Status != StatusRunning ||
		conversation.Messages[len(conversation.Messages)-1].Text != "Is the device voice enough?" {
		t.Fatalf("the queued message did not start the next turn: puts=%d %+v", len(worker.puts), conversation)
	}
}

func TestAWorkOrderBackInReadyWakesTheAssistant(t *testing.T) {
	events := changes(map[string]string{"fea_1": "ready_to_merge|run_1|waiting_for_user|blocker|"},
		map[string]work{"fea_1": {Title: "Practice", State: feature.StateReady, RunID: "run_1", RunStatus: execution.RunStatusStopped}})
	if len(events) != 1 || !events[0].Wake || !strings.Contains(events[0].Text, "back in Ready") {
		t.Fatalf("events = %+v", events)
	}
}
