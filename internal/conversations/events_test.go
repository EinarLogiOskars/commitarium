package conversations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
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

type deletedProjectStub struct {
	projectsStub
	missing map[string]error
}

func (stub deletedProjectStub) GetByID(ctx context.Context, id string) (project.Project, error) {
	if err, ok := stub.missing[id]; ok {
		return project.Project{}, err
	}
	return stub.projectsStub.GetByID(ctx, id)
}

type featuresFunc func(context.Context, string) ([]feature.Feature, error)

func (list featuresFunc) List(ctx context.Context, projectID string) ([]feature.Feature, error) {
	return list(ctx, projectID)
}

// A folder left behind by a deleted project is removed at the next look, and
// the watcher does not ask about its work. A failed lookup removes nothing.
func TestTheWatcherForgetsChatsOfDeletedProjects(t *testing.T) {
	service, worker := newTestService(t)
	for _, scope := range []string{"prj_gone", "prj_kept", "prj_down"} {
		if err := service.Wake(t.Context(), scope, "Note", "Text"); err != nil {
			t.Fatal(err)
		}
		worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Done."})
	}
	service.config.Projects = deletedProjectStub{missing: map[string]error{
		"prj_gone": fmt.Errorf("get project: %w", project.ErrNotFound),
		"prj_down": errors.New("database is locked"),
	}}
	looked := []string{}
	watcher := Watcher{Service: service, Features: featuresFunc(func(_ context.Context, id string) ([]feature.Feature, error) {
		looked = append(looked, id)
		return nil, nil
	})}
	watcher.Tick(t.Context())
	if scopes := service.projectScopes(); !slices.Equal(scopes, []string{"prj_down", "prj_kept"}) {
		t.Fatalf("scopes = %v", scopes)
	}
	if slices.Contains(looked, "prj_gone") {
		t.Fatalf("the watcher looked at a deleted project: %v", looked)
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
	current = map[string]work{"fea_1": {Title: "Log", State: feature.StateImplementing, RunID: "run_1",
		RunStatus: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindClarification}}
	conversation := observe()
	if conversation.Status != StatusRunning || len(worker.puts) != 2 ||
		!strings.Contains(worker.puts[1].Instructions, `"Log" is waiting for the user's answer.`) ||
		conversation.Messages[len(conversation.Messages)-1].Text != `"Log" is waiting for the user's answer.` {
		t.Fatalf("a question did not wake the assistant: %+v", conversation)
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

// News (a merge, work back in Ready) stays a line in the chat where the
// assistant only assists: it would repeat it on the user's next message.
// Where it drives the work, it wakes it to move on.
func TestNewsWakesTheAssistantOnlyWhereItDrivesTheWork(t *testing.T) {
	for _, mode := range []project.AssistantMode{project.AssistantModeAssists, project.AssistantModeBuilds} {
		service, worker := newTestService(t)
		service.config.Projects = gapProjects{mode: mode}
		current := map[string]work{"fea_1": {Title: "Log", State: feature.StateImplementing, RunID: "run_1",
			RunStatus: execution.RunStatusRunning}}
		snapshot := func(context.Context, string) (view, error) { return view{Work: current}, nil }
		if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
			t.Fatal(err)
		}
		current = map[string]work{"fea_1": {Title: "Log", State: feature.StateCompleted}}
		if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
			t.Fatal(err)
		}
		stored, _ := service.read("prj_test")
		if lastEvent(stored.Conversation) != `"Log" merged.` {
			t.Fatalf("%s: the merge is not in the chat: %+v", mode, stored.Messages)
		}
		woke := len(worker.puts) == 1
		if woke != (mode == project.AssistantModeBuilds) || len(stored.PendingEvents) != 0 {
			t.Fatalf("%s: woke = %v, pending = %v", mode, woke, stored.PendingEvents)
		}
		if mode == project.AssistantModeBuilds {
			continue
		}
		// The user's next message does not recap it.
		if _, err := service.Send(t.Context(), "prj_test", "A new idea", "msg-1"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(worker.puts[0].Instructions, "Commitarium reports what happened") {
			t.Fatalf("the user's turn recaps old news: %s", worker.puts[0].Instructions)
		}
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
