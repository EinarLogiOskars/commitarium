package orchestration

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
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

func TestPlanningRecoverySuccessorsDoNotTakeTheNextTurn(t *testing.T) {
	session := execution.Session{ID: "run_x:reviewer", Role: worker.RoleReviewer}
	first, err := recoverySuccessorAttemptID(session.ID, "run_x:reviewer:planning:1")
	if err != nil || first != "run_x:reviewer:planning-recovery:1:1" {
		t.Fatalf("first successor = %q err=%v", first, err)
	}
	second, err := recoverySuccessorAttemptID(session.ID, first)
	if err != nil || second != "run_x:reviewer:planning-recovery:1:2" {
		t.Fatalf("second successor = %q err=%v", second, err)
	}
	revised, err := recoverySuccessorAttemptID(session.ID, "run_x:reviewer:planning:v2:3")
	if err != nil || revised != "run_x:reviewer:planning-recovery:v2:3:1" {
		t.Fatalf("revised-plan successor = %q err=%v", revised, err)
	}
	if version, turn, ok := planningAttemptVersionAndTurn(session.ID, revised); !ok || version != 2 || turn != 3 {
		t.Fatalf("successor did not parse as its logical turn: v%d turn %d ok=%v", version, turn, ok)
	}
	if stage := planningStageForAttempt(session, first); stage != planningStageFirstReview {
		t.Fatalf("successor of the first review has stage %q", stage)
	}
	approval, err := recoverySuccessorAttemptID(session.ID, "run_x:reviewer:approval:1")
	if err != nil || approval != "run_x:reviewer:approval:2" {
		t.Fatalf("approval successor = %q err=%v", approval, err)
	}
}

func TestBriefBecomesThePlanningGoalWithAFreshnessCheck(t *testing.T) {
	goal := briefGoal(featureartifact.HandoffBrief{
		Goal: "Add due dates.", Areas: []string{"backend/app/models"},
		Considerations: []string{"Keep rows valid."}, OpenQuestions: []string{},
	})
	if !strings.HasPrefix(goal, "Add due dates.") || !strings.Contains(goal, "Areas to touch:\n- backend/app/models") ||
		!strings.Contains(goal, "Worth planning around:\n- Keep rows valid.") || strings.Contains(goal, "Open questions") {
		t.Fatalf("brief goal:\n%s", goal)
	}
	moved := briefFreshnessInstructions(strings.Repeat("a", 40), strings.Repeat("b", 40))
	if !strings.Contains(moved, "git log --oneline "+strings.Repeat("a", 40)+".."+strings.Repeat("b", 40)) {
		t.Fatalf("moved-base freshness:\n%s", moved)
	}
	if current := briefFreshnessInstructions("", strings.Repeat("b", 40)); strings.Contains(current, "git log") {
		t.Fatalf("unmoved-base freshness asks for history:\n%s", current)
	}
}
