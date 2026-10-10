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
	}}
	if got := agentID(run.AgentProviders.Lead, worker.RoleLead); got != "claude-lead" {
		t.Fatalf("lead agent ID = %q", got)
	}
	if got := starter.profileID(run.AgentProviders.Lead, worker.RoleLead); got != "claude" {
		t.Fatalf("lead profile = %q", got)
	}
	if got := starter.forgejoAuthorFor(run, worker.RoleLead); got != "claude-lead" {
		t.Fatalf("lead Forgejo author = %q", got)
	}
	if got := agentID(run.AgentProviders.Reviewer, worker.RoleReviewer); got != "codex-reviewer" {
		t.Fatalf("reviewer agent ID = %q", got)
	}
	if got := starter.profileID(run.AgentProviders.Reviewer, worker.RoleReviewer); got != "codex" {
		t.Fatalf("reviewer profile = %q", got)
	}
	if got := starter.forgejoAuthorFor(run, worker.RoleReviewer); got != "codex-reviewer" {
		t.Fatalf("reviewer Forgejo author = %q", got)
	}

	// The same agent can lead and review; each role keeps its own identity so
	// Forgejo accepts the reviewer's approval of the lead's pull request.
	same := execution.Run{AgentProviders: project.AgentProviders{
		Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderClaude,
	}}
	if starter.profileID(same.AgentProviders.Lead, worker.RoleLead) !=
		starter.profileID(same.AgentProviders.Reviewer, worker.RoleReviewer) ||
		starter.forgejoAuthorFor(same, worker.RoleLead) == starter.forgejoAuthorFor(same, worker.RoleReviewer) {
		t.Fatal("one agent should share its profile but not its Forgejo identity across roles")
	}
}
