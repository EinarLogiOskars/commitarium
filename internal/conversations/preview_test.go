package conversations

import (
	"context"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type runsStub struct{ runs map[string][]execution.Run }

func (stub runsStub) RunsForFeature(_ context.Context, featureID string) ([]execution.Run, error) {
	return stub.runs[featureID], nil
}

func TestAPreviewFailureReportWakesTheAssistant(t *testing.T) {
	service, worker := newTestService(t)
	// Without a conversation there is nobody to tell.
	if err := service.Report(t.Context(), "prj_test", "The app's preview failed to start."); err != nil {
		t.Fatal(err)
	}
	if conversation, _ := service.Get(t.Context(), "prj_test"); len(conversation.Messages) != 0 {
		t.Fatalf("a project without a conversation got %+v", conversation.Messages)
	}

	if _, err := service.Send(t.Context(), "prj_test", "Hi", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Hello."})
	text := `The app's preview failed to start: the "api" part of the app stops right after it starts (exit code 1).`
	if err := service.Report(t.Context(), "prj_test", text); err != nil {
		t.Fatal(err)
	}
	snapshot := func(context.Context, string) (view, error) { return view{Work: map[string]work{}}, nil }
	if err := service.observe(t.Context(), "prj_test", snapshot); err != nil {
		t.Fatal(err)
	}
	if len(worker.puts) != 2 || !strings.Contains(worker.puts[1].Instructions, text) {
		t.Fatalf("the report did not wake the assistant: %d turns", len(worker.puts))
	}
}

func TestTheAssistantFollowsThePreviewSetupButNotOtherBackgroundWork(t *testing.T) {
	service, _ := newTestService(t)
	features := &featuresStub{features: []feature.Feature{
		{ID: "fea_preview", Title: "Fix the preview", Background: true, PreviewCheck: true, State: feature.StateImplementing},
		{ID: "fea_other", Title: "Housekeeping", Background: true, State: feature.StateImplementing},
	}}
	watcher := Watcher{Service: service, Features: features, Runs: runsStub{runs: map[string][]execution.Run{
		"fea_preview": {{ID: "run_preview", Status: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindBlocker,
			Reason: "The app's setup couldn't be made to run."}},
	}}}
	seen, err := watcher.snapshot(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	if _, followed := seen.Work["fea_preview"]; !followed || len(seen.Work) != 1 {
		t.Fatalf("watched work = %+v", seen.Work)
	}
	event, ok := seen.Work["fea_preview"].event()
	if !ok || !event.Wake || !strings.Contains(event.Text, "couldn't be made to run") {
		t.Fatalf("a blocked preview setup said %+v", event)
	}
}

func TestAnAppSetsItsPreviewUpWithoutTheUser(t *testing.T) {
	for _, app := range []bool{true, false} {
		service, _ := newTestService(t)
		if app {
			service.config.Projects = appProjectsStub{}
		}
		service.config.Features = &featuresStub{}
		stored, err := service.read("prj_test")
		if err != nil {
			t.Fatal(err)
		}
		stored.Actions = append(stored.Actions, Action{ID: "act_preview", Status: ActionProposed,
			AssistantAction: featureartifact.AssistantAction{Type: featureartifact.AssistantSetUpPreview, Reason: "It does not open"}})
		if err := service.write(stored); err != nil {
			t.Fatal(err)
		}
		apply, err := service.autopilotActions(t.Context(), "prj_test")
		if err != nil || (len(apply) == 1) != app {
			t.Fatalf("app=%t applied %+v err=%v", app, apply, err)
		}
	}
}
