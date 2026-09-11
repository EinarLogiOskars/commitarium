package orchestration

import (
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestRemoteWorkflowUsesProviderSpecificIdentityProfileAndForgejoAuthor(t *testing.T) {
	starter := &RemoteLeadStarter{
		agentProfileID: "codex-lead-profile", reviewerAgentProfileID: "codex-reviewer-profile",
		claudeAgentProfileID: "claude-lead-profile", claudeReviewerAgentProfileID: "claude-reviewer-profile",
		forgejoAuthor: "codex-lead-user", reviewerForgejoAuthor: "codex-reviewer-user",
		claudeForgejoAuthor: "claude-lead-user", claudeReviewerForgejoAuthor: "claude-reviewer-user",
	}
	run := execution.Run{AgentProviders: project.AgentProviders{
		Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex,
	}}
	if got := agentID(run.AgentProviders.Lead, worker.RoleLead); got != "claude-lead" {
		t.Fatalf("lead agent ID = %q", got)
	}
	if got := starter.profileID(run.AgentProviders.Lead, worker.RoleLead); got != "claude-lead-profile" {
		t.Fatalf("lead profile = %q", got)
	}
	if got := starter.forgejoAuthorFor(run, worker.RoleLead); got != "claude-lead-user" {
		t.Fatalf("lead Forgejo author = %q", got)
	}
	if got := agentID(run.AgentProviders.Reviewer, worker.RoleReviewer); got != "codex-reviewer" {
		t.Fatalf("reviewer agent ID = %q", got)
	}
	if got := starter.profileID(run.AgentProviders.Reviewer, worker.RoleReviewer); got != "codex-reviewer-profile" {
		t.Fatalf("reviewer profile = %q", got)
	}
	if got := starter.forgejoAuthorFor(run, worker.RoleReviewer); got != "codex-reviewer-user" {
		t.Fatalf("reviewer Forgejo author = %q", got)
	}
}
