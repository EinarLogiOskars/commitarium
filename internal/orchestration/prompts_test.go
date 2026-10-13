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

func TestPlanningPromptsSayTheCounterpartIsReachedBetweenTurns(t *testing.T) {
	for _, prompt := range []string{
		leadPlanningResponseInstructions("R1 resolved.", "Claude"),
		reviewerResponseInstructions("Changed step 2.", "Codex"),
	} {
		if !strings.Contains(prompt, "separate agent that Commitarium runs") ||
			!strings.Contains(prompt, "never present their views as") {
			t.Fatalf("a planning prompt lets the agent stand in for its counterpart: %s", prompt)
		}
	}
}

func TestBaseUpdateInstructionsMergeMainWithoutRewritingHistory(t *testing.T) {
	prepared := workspace.Workspace{
		RepositoryOwner: "owner", RepositoryName: "repo", BaseBranch: "main",
		Branch: "commitarium/fea_x", PullRequestNumber: 7,
	}
	approved, mainHead := strings.Repeat("a", 40), strings.Repeat("c", 40)
	attemptID := baseUpdateAttemptForVersion("run_x:lead", 1, 1)
	instructions := implementationBaseUpdateInstructions(prepared, approved, mainHead, "", attemptID)
	for _, required := range []string{
		workspace.ImplementationPublicationBaseUpdate.Marker(attemptID),
		"'## Base update'",
		"git fetch commitarium main",
		"git merge --no-edit FETCH_HEAD",
		"Never rebase",
		"descends from the approved commit",
		"both sides keep their intent",
		"Check existing comments for the marker",
		"Exact approved commit: " + approved,
		"moved to " + mainHead,
		"return action 'blocked'",
		forgejoTokenNote,
	} {
		if !strings.Contains(instructions, required) {
			t.Fatalf("base update instructions omit %q:\n%s", required, instructions)
		}
	}
	if strings.Contains(instructions, "User guidance") {
		t.Fatalf("unguided update mentions guidance:\n%s", instructions)
	}
	guided := implementationBaseUpdateInstructions(prepared, approved, "", "Keep the new date format from main.", attemptID)
	if !strings.Contains(guided, "User guidance: Keep the new date format from main.") {
		t.Fatalf("guided update omits the user's guidance:\n%s", guided)
	}
}

func TestReviewAfterBaseUpdateReviewsOnlyTheMerge(t *testing.T) {
	prepared := workspace.Workspace{BaseBranch: "main"}
	approved, updated := strings.Repeat("a", 40), strings.Repeat("b", 40)
	rereview := implementationReviewInstructions(prepared, "Merged main.", updated, "attempt", approved, true)
	for _, required := range []string{
		"You approved commit " + approved,
		"git show --remerge-diff " + updated,
		"nothing you approved in " + approved + " was lost",
		"Re-run the relevant tests",
	} {
		if !strings.Contains(rereview, required) {
			t.Fatalf("re-review after an update omits %q:\n%s", required, rereview)
		}
	}
	correction := implementationReviewInstructions(prepared, "Fixed F1.", updated, "attempt", approved, false)
	if strings.Contains(correction, "remerge-diff") || !strings.Contains(correction, "git diff "+approved+" "+updated) {
		t.Fatalf("re-review of a correction lost its scope:\n%s", correction)
	}
}

func TestBaseUpdateAttemptsHaveTheirOwnIdentity(t *testing.T) {
	lead := execution.Session{ID: "run_x:lead", Role: worker.RoleLead}
	first := baseUpdateAttemptForVersion(lead.ID, 1, 1)
	revised := baseUpdateAttemptForVersion(lead.ID, 2, 3)
	if first != "run_x:lead:update:1" || revised != "run_x:lead:update:v2:3" {
		t.Fatalf("update attempt IDs = %q, %q", first, revised)
	}
	if turn, ok := baseUpdateTurnNumber(lead.ID, revised); !ok || turn != 3 {
		t.Fatalf("revised update parsed as turn %d ok=%v", turn, ok)
	}
	if _, ok := baseUpdateTurnNumber(lead.ID, implementationReadinessAttemptForVersion(lead.ID, 1, 1)); ok {
		t.Fatal("a readiness turn parsed as an update")
	}
	successor, err := recoverySuccessorAttemptID(lead.ID, revised)
	if err != nil || successor != baseUpdateAttemptForVersion(lead.ID, 2, 4) {
		t.Fatalf("update successor = %q err=%v", successor, err)
	}
	if kind := waitKindForAttempt(lead, first); kind != execution.RunWaitKindPhaseCheckpoint {
		t.Fatalf("update wait kind = %q", kind)
	}
	if reason := waitingReasonForAttempt(lead, first); reason != implementationBaseUpdateRunningReason {
		t.Fatalf("update waiting reason = %q", reason)
	}
	if version, ok := leadAttemptPlanVersion(lead.ID, revised); !ok || version != 2 {
		t.Fatalf("update plan version = %d ok=%v", version, ok)
	}
}

func TestNextBaseUpdateFollowsEveryEarlierUpdate(t *testing.T) {
	lead := "run_x:lead"
	readiness := execution.WorkerAttemptCheckpoint{SessionID: lead, AttemptID: implementationReadinessAttemptForVersion(lead, 1, 3)}
	if turn := nextBaseUpdateTurn(nil, lead, 1, readiness); turn != 1 {
		t.Fatalf("first update turn = %d", turn)
	}
	// The second update of the version follows the first one's recovery
	// successor, which the run returned from verified.
	events := []execution.Event{
		{ID: baseUpdateIntentID(implementationReadinessAttemptForVersion(lead, 1, 2))},
		{ID: baseUpdateAttemptForVersion(lead, 1, 2) + ":publication-verified"},
		{ID: baseUpdateAttemptForVersion(lead, 2, 7) + ":publication-verified"},
	}
	if turn := nextBaseUpdateTurn(events, lead, 1, readiness); turn != 3 {
		t.Fatalf("second update turn = %d", turn)
	}
	leadOnly := execution.WorkerAttemptCheckpoint{SessionID: lead, AttemptID: baseUpdateAttemptForVersion(lead, 1, 4)}
	if turn := nextBaseUpdateTurn(events, lead, 1, leadOnly); turn != 5 {
		t.Fatalf("update after a lead-only update = %d", turn)
	}
}
