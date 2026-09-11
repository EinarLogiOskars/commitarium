package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
)

type routingWorkerStub struct{ puts, gets int }

func (stub *routingWorkerStub) PutAttempt(
	context.Context, workerhttp.MutationIdentity, workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	stub.puts++
	return workerhttp.Attempt{}, true, nil
}

func (stub *routingWorkerStub) GetAttempt(
	context.Context, workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	stub.gets++
	return workerhttp.Attempt{}, nil
}

type routingPumpStub struct{ runs int }

func (stub *routingPumpStub) Run(context.Context, string) (workeringest.PumpResult, error) {
	stub.runs++
	return workeringest.PumpResult{}, nil
}

type routingRunFinder struct{ runs map[string]execution.Run }

func (finder routingRunFinder) GetRun(_ context.Context, id string) (execution.Run, error) {
	run, ok := finder.runs[id]
	if !ok {
		return execution.Run{}, execution.ErrNotFound
	}
	return run, nil
}

func TestProviderRoutersUseTheDurableRunAssignment(t *testing.T) {
	codexLead, codexReviewer := &routingWorkerStub{}, &routingWorkerStub{}
	claudeLead, claudeReviewer := &routingWorkerStub{}, &routingWorkerStub{}
	finder := routingRunFinder{runs: map[string]execution.Run{
		"run_mixed": {AgentProviders: project.AgentProviders{
			Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex,
		}},
		"run_reverse": {AgentProviders: project.AgentProviders{
			Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude,
		}},
	}}
	workerRouter, err := NewProviderRoutedWorker(finder, ProviderWorkerRoutes{
		CodexLead: codexLead, CodexReviewer: codexReviewer,
		ClaudeLead: claudeLead, ClaudeReviewer: claudeReviewer,
	})
	if err != nil {
		t.Fatalf("create worker router: %v", err)
	}
	for _, test := range []struct {
		sessionID string
		role      workerhttp.Role
	}{
		{sessionID: "run_mixed:lead", role: workerhttp.RoleLead},
		{sessionID: "run_mixed:reviewer", role: workerhttp.RoleReviewer},
		{sessionID: "run_reverse:lead", role: workerhttp.RoleLead},
		{sessionID: "run_reverse:reviewer", role: workerhttp.RoleReviewer},
	} {
		identity := workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: test.sessionID, AttemptID: "attempt_test",
		}}
		if _, _, err := workerRouter.PutAttempt(t.Context(), identity, workerhttp.PutAttemptRequest{
			Assignment: workerhttp.Assignment{Role: test.role},
		}); err != nil {
			t.Fatalf("route %s put: %v", test.role, err)
		}
		if _, err := workerRouter.GetAttempt(t.Context(), identity.AttemptReference); err != nil {
			t.Fatalf("route %s get: %v", test.role, err)
		}
	}
	if claudeLead.puts != 1 || claudeLead.gets != 1 ||
		codexReviewer.puts != 1 || codexReviewer.gets != 1 ||
		codexLead.puts != 1 || codexLead.gets != 1 ||
		claudeReviewer.puts != 1 || claudeReviewer.gets != 1 {
		t.Fatalf("unexpected provider routing codexLead=%+v codexReviewer=%+v claudeLead=%+v claudeReviewer=%+v",
			codexLead, codexReviewer, claudeLead, claudeReviewer)
	}

	codexLeadPump, codexReviewerPump := &routingPumpStub{}, &routingPumpStub{}
	claudeLeadPump, claudeReviewerPump := &routingPumpStub{}, &routingPumpStub{}
	pumpRouter, err := NewProviderRoutedPump(finder, ProviderPumpRoutes{
		CodexLead: codexLeadPump, CodexReviewer: codexReviewerPump,
		ClaudeLead: claudeLeadPump, ClaudeReviewer: claudeReviewerPump,
	})
	if err != nil {
		t.Fatalf("create pump router: %v", err)
	}
	if _, err := pumpRouter.Run(t.Context(), "run_mixed:lead"); err != nil {
		t.Fatalf("route lead pump: %v", err)
	}
	if _, err := pumpRouter.Run(t.Context(), "run_mixed:reviewer"); err != nil {
		t.Fatalf("route reviewer pump: %v", err)
	}
	if claudeLeadPump.runs != 1 || codexReviewerPump.runs != 1 ||
		codexLeadPump.runs != 0 || claudeReviewerPump.runs != 0 {
		t.Fatalf("unexpected pump routing")
	}
}

func TestProviderRoutersRejectUnknownOrMismatchedSessions(t *testing.T) {
	worker := &routingWorkerStub{}
	finder := routingRunFinder{runs: map[string]execution.Run{
		"run_test": {AgentProviders: project.DefaultAgentProviders()},
	}}
	router, _ := NewProviderRoutedWorker(finder, ProviderWorkerRoutes{
		CodexLead: worker, CodexReviewer: worker, ClaudeLead: worker, ClaudeReviewer: worker,
	})
	if _, err := router.GetAttempt(t.Context(), workerhttp.AttemptReference{SessionID: "unknown"}); err == nil {
		t.Fatal("expected unsupported session to fail")
	}
	if _, _, err := router.PutAttempt(t.Context(), workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: "run_test:lead"},
	}, workerhttp.PutAttemptRequest{
		Assignment: workerhttp.Assignment{Role: workerhttp.RoleReviewer},
	}); err == nil {
		t.Fatal("expected mismatched session role to fail")
	}
	if _, err := router.GetAttempt(t.Context(), workerhttp.AttemptReference{SessionID: "run_missing:lead"}); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("expected missing run error, got %v", err)
	}
}
