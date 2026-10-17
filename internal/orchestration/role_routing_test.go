package orchestration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
)

type routingWorkerStub struct {
	puts, gets int
	models     []string
	efforts    []string
}

func (stub *routingWorkerStub) PutAttempt(
	_ context.Context, _ workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	stub.puts++
	stub.models = append(stub.models, request.Assignment.Model)
	stub.efforts = append(stub.efforts, request.Assignment.Effort)
	return workerhttp.Attempt{}, true, nil
}

func (stub *routingWorkerStub) GetAttempt(
	context.Context, workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	stub.gets++
	return workerhttp.Attempt{}, nil
}

func (*routingWorkerStub) Supersede(
	context.Context, workerhttp.MutationIdentity, workerhttp.SupersedeRequest,
) (workerhttp.Attempt, error) {
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

func routeTo[T any](byAgent map[string]T) func(string) (T, error) {
	return func(agentID string) (T, error) {
		target, ok := byAgent[agentID]
		if !ok {
			var zero T
			return zero, errors.New("unknown agent " + agentID)
		}
		return target, nil
	}
}

func TestAgentRoutersUseTheDurableRunAssignment(t *testing.T) {
	codex, claude := &routingWorkerStub{}, &routingWorkerStub{}
	finder := routingRunFinder{runs: map[string]execution.Run{
		"run_mixed": {AgentProviders: project.AgentProviders{
			Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex,
			LeadAgent: "claude-max", ReviewerAgent: "codex",
		}, AgentModels: project.AgentModels{Lead: "claude-lead-pinned-1", Reviewer: "gpt-review-pinned-1"}},
		"run_reverse": {AgentProviders: project.AgentProviders{
			Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude,
			LeadAgent: "codex", ReviewerAgent: "claude-max",
		}, AgentModels: project.AgentModels{Lead: "gpt-lead-pinned-1", Reviewer: "claude-review-pinned-1"}},
	}}
	workerRouter, err := NewAgentRoutedWorker(finder, routeTo(map[string]RemoteLeadWorker{
		"codex": codex, "claude-max": claude,
	}))
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
	if codex.puts != 2 || codex.gets != 2 || claude.puts != 2 || claude.gets != 2 {
		t.Fatalf("unexpected agent routing codex=%+v claude=%+v", codex, claude)
	}
	if strings.Join(claude.models, ",") != "claude-lead-pinned-1,claude-review-pinned-1" ||
		strings.Join(codex.models, ",") != "gpt-review-pinned-1,gpt-lead-pinned-1" {
		t.Fatalf("router did not inject exact run model snapshots: codex=%v claude=%v", codex.models, claude.models)
	}

	codexPump, claudePump := &routingPumpStub{}, &routingPumpStub{}
	pumpRouter, err := NewAgentRoutedPump(finder, routeTo(map[string]RemoteLeadPump{
		"codex": codexPump, "claude-max": claudePump,
	}))
	if err != nil {
		t.Fatalf("create pump router: %v", err)
	}
	if _, err := pumpRouter.Run(t.Context(), "run_mixed:lead"); err != nil {
		t.Fatalf("route lead pump: %v", err)
	}
	if _, err := pumpRouter.Run(t.Context(), "run_mixed:reviewer"); err != nil {
		t.Fatalf("route reviewer pump: %v", err)
	}
	if claudePump.runs != 1 || codexPump.runs != 1 {
		t.Fatalf("unexpected pump routing")
	}
}

func TestAgentRoutersRejectUnknownOrMismatchedSessions(t *testing.T) {
	worker := &routingWorkerStub{}
	finder := routingRunFinder{runs: map[string]execution.Run{
		"run_test": {AgentProviders: project.DefaultAgentProviders()},
	}}
	router, _ := NewAgentRoutedWorker(finder, routeTo(map[string]RemoteLeadWorker{
		"codex": worker, "claude": worker,
	}))
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

type phaseChoiceStub map[string]project.PhaseChoices

func (stub phaseChoiceStub) PhaseChoices(_ context.Context, featureID string) (project.PhaseChoices, error) {
	return stub[featureID], nil
}

// Planning turns run on each role's planning model and effort; later turns on
// its model and effort (ADR-031).
func TestAgentRouterUsesPhaseChoices(t *testing.T) {
	codex := &routingWorkerStub{}
	finder := routingRunFinder{runs: map[string]execution.Run{
		"run_one": {FeatureID: "feat_one", AgentProviders: project.AgentProviders{
			Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderCodex,
			LeadAgent: "codex", ReviewerAgent: "codex",
		}, AgentModels: project.AgentModels{Lead: "gpt-6.1-sol", Reviewer: "gpt-6.1-sol"}},
	}}
	router, err := NewAgentRoutedWorker(finder, routeTo(map[string]RemoteLeadWorker{"codex": codex}))
	if err != nil {
		t.Fatal(err)
	}
	router.WithPhaseChoices(phaseChoiceStub{"feat_one": {
		LeadPlanModel: "gpt-6-astra", LeadPlanEffort: "high", LeadEffort: "medium",
		ReviewerPlanEffort: "xhigh",
	}})
	for _, turn := range []struct {
		session, attempt string
		role             workerhttp.Role
	}{
		{"run_one:lead", "run_one:lead:planning:1", workerhttp.RoleLead},
		{"run_one:reviewer", "run_one:reviewer:approval:1", workerhttp.RoleReviewer},
		{"run_one:lead", "run_one:lead:planning-recovery:2:1", workerhttp.RoleLead},
		{"run_one:lead", "run_one:lead:implementation:1", workerhttp.RoleLead},
		{"run_one:reviewer", "run_one:reviewer:review:1:1:1", workerhttp.RoleReviewer},
	} {
		identity := workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: turn.session, AttemptID: turn.attempt,
		}}
		if _, _, err := router.PutAttempt(t.Context(), identity, workerhttp.PutAttemptRequest{
			Assignment: workerhttp.Assignment{Role: turn.role},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(codex.models, ","); got != "gpt-6-astra,gpt-6.1-sol,gpt-6-astra,gpt-6.1-sol,gpt-6.1-sol" {
		t.Fatalf("models = %s", got)
	}
	if got := strings.Join(codex.efforts, ","); got != "high,xhigh,high,medium," {
		t.Fatalf("efforts = %s", got)
	}
}
