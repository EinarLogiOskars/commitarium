package orchestration

import (
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestRemoteWorkflowDerivesProfileAndForgejoAuthorFromTheAgent(t *testing.T) {
	starter := &RemoteLeadStarter{}
	run := execution.Run{AgentProviders: project.AgentProviders{
		Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex,
		LeadAgent: "claude-max", ReviewerAgent: "codex",
	}}
	if got := agentID(run.AgentProviders.LeadAgent, worker.RoleLead); got != "claude-max-lead" {
		t.Fatalf("lead agent ID = %q", got)
	}
	if got := starter.profileID(agentForSession(run, worker.RoleLead), worker.RoleLead); got != "claude-max" {
		t.Fatalf("lead profile = %q", got)
	}
	if got := starter.forgejoAuthorFor(run, worker.RoleLead); got != "claude-max-lead" {
		t.Fatalf("lead Forgejo author = %q", got)
	}
	if got := agentID(run.AgentProviders.ReviewerAgent, worker.RoleReviewer); got != "codex-reviewer" {
		t.Fatalf("reviewer agent ID = %q", got)
	}
	if got := starter.profileID(agentForSession(run, worker.RoleReviewer), worker.RoleReviewer); got != "codex" {
		t.Fatalf("reviewer profile = %q", got)
	}
	if got := starter.forgejoAuthorFor(run, worker.RoleReviewer); got != "codex-reviewer" {
		t.Fatalf("reviewer Forgejo author = %q", got)
	}

	// The same agent can lead and review; each role keeps its own identity so
	// Forgejo accepts the reviewer's approval of the lead's pull request.
	same := execution.Run{AgentProviders: project.AgentProviders{
		Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderClaude,
		LeadAgent: "claude-max", ReviewerAgent: "claude-max",
	}}
	if agentForSession(same, worker.RoleLead) != agentForSession(same, worker.RoleReviewer) ||
		starter.forgejoAuthorFor(same, worker.RoleLead) == starter.forgejoAuthorFor(same, worker.RoleReviewer) {
		t.Fatal("one agent should share its profile but not its Forgejo identity across roles")
	}
}
