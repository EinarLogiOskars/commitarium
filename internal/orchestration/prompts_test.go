package orchestration

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func TestPlanningDialogueBriefsOnceAndAddressesTheOtherAgent(t *testing.T) {
	providers := project.AgentProviders{Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex}
	lead, reviewer := leadParticipant(providers), reviewerParticipant(providers)
	if lead != "Claude (lead)" || reviewer != "Codex (reviewer)" {
		t.Fatalf("participants = %q, %q", lead, reviewer)
	}
	goal := "Export the visible report columns as CSV."
	storedFeature := feature.Feature{AcceptedGoal: goal}
	prepared := workspace.Workspace{RepositoryOwner: "owner", RepositoryName: "repo", BaseBranch: "main"}

	proposal := planningInstructions(storedFeature, prepared, reviewer)
	firstReview := reviewerPlanningInstructions(storedFeature, prepared, "Step 1: add the export.", lead)
	for name, text := range map[string]string{"proposal": proposal, "first review": firstReview} {
		if !strings.Contains(text, goal) || !strings.Contains(text, "Repository: owner/repo") {
			t.Fatalf("%s omits the briefing:\n%s", name, text)
		}
	}
	if !strings.Contains(proposal, "as few as the goal needs") || !strings.Contains(proposal, reviewer) {
		t.Fatalf("proposal does not bound steps or name the reviewer:\n%s", proposal)
	}
	if !strings.Contains(firstReview, "R1") || !strings.Contains(firstReview, lead) {
		t.Fatalf("first review does not number concerns or name the lead:\n%s", firstReview)
	}

	leadReply := leadPlanningResponseInstructions("R1: add a failure-path test.", reviewer)
	reviewerReply := reviewerResponseInstructions("R1 accepted: step 2 now adds it.", lead)
	for name, text := range map[string]string{"lead reply": leadReply, "reviewer reply": reviewerReply} {
		if strings.Contains(text, goal) || strings.Contains(text, "Repository:") {
			t.Fatalf("%s repeats the briefing:\n%s", name, text)
		}
		if strings.Contains(text, "again") {
			t.Fatalf("%s orders a repeated inspection:\n%s", name, text)
		}
	}
	if !strings.Contains(leadReply, "R1: add a failure-path test.") || !strings.Contains(leadReply, "by its number") ||
		!strings.Contains(leadReply, "submit_plan") {
		t.Fatalf("lead reply lost the message or the numbered-answer rule:\n%s", leadReply)
	}
	if !strings.Contains(reviewerReply, "R1 accepted") || !strings.Contains(reviewerReply, "Do not restate the plan") {
		t.Fatalf("reviewer reply lost the message or the no-restating rule:\n%s", reviewerReply)
	}
}
