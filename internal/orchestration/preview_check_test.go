package orchestration

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// previewFixCommits are the commits the lead publishes in its fixes.
var previewFixCommits = []string{
	strings.Repeat("3", 40), strings.Repeat("4", 40), strings.Repeat("5", 40), strings.Repeat("6", 40),
}

const previewCheckLog = "ModuleNotFoundError: No module named 'fastapi'"

type previewCheckClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *previewCheckClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *previewCheckClock) Advance(by time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(by)
}

type previewCheckFixture struct {
	*baseUpdateFixture
	checks *previewcheck.Service
	clock  *previewCheckClock
}

// startPreviewSetupRun starts a lead-only, run-to-completion work order that
// sets the preview up: it publishes commit a, then fixes publish the
// previewFixCommits in order.
func startPreviewSetupRun(t *testing.T, runID string, fixes int) *previewCheckFixture {
	t.Helper()
	clock := &previewCheckClock{now: time.Date(2026, time.October, 14, 9, 0, 0, 0, time.UTC)}
	checks := previewcheck.NewService(previewcheck.NewMemoryStore()).WithClock(clock.Now)
	fixture := startBaseUpdateRun(t, runID, false, project.MergePolicyAutoAfterGates, 0,
		func(fixture *baseUpdateFixture) {
			if _, err := fixture.db.ExecContext(t.Context(),
				`UPDATE features SET preview_check = 1 WHERE id = ?`, fixture.featureID); err != nil {
				t.Fatalf("mark the preview setup: %v", err)
			}
			fixture.starter.previewChecks = checks
			for turn := 2; turn < 2+fixes; turn++ {
				addCompletedPreviewFixAttempt(fixture.worker, runID, fixture.project.ID, fixture.featureID, turn,
					previewFixCommits[turn-2])
			}
		})
	return &previewCheckFixture{baseUpdateFixture: fixture, checks: checks, clock: clock}
}

func addCompletedPreviewFixAttempt(
	stub *conversationalRemoteLeadWorker, runID, projectID, featureID string, turn int, commitID string,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{SessionID: sessionID, AttemptID: implementationAttemptID(sessionID, turn)}
	now := time.Date(2026, time.October, 14, 10, turn, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleLead, WorkspaceID: "wsp_managed_feature",
		},
		ProviderSessionID: "codex-thread-test", State: workerhttp.AttemptStateRunning,
		StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	summary := "Installed the API's dependencies in its image."
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 2
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: summary,
		Publication: &workerhttp.ImplementationPublication{CommitID: commitID, PullRequestNumber: 7},
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage, Text: summary, OccurredAt: now},
		{AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal, Text: summary, OccurredAt: endedAt},
	}
}

// waitForCheck waits at the merge gate for the pending check of commitID.
func (fixture *previewCheckFixture) waitForCheck(t *testing.T, commitID string) previewcheck.Check {
	t.Helper()
	gated := fixture.waitAtMergeGate(t)
	if gated.Reason != previewCheckPendingReason {
		t.Fatalf("the run does not wait for its preview check: %+v", gated)
	}
	check, found, err := fixture.checks.Latest(t.Context(), fixture.runID, commitID)
	if err != nil || !found || check.Status != previewcheck.StatusPending {
		t.Fatalf("no pending preview check of %s: %+v %t %v", commitID, check, found, err)
	}
	return check
}

// finish runs the check as the desktop does and reports it to the workflow.
func (fixture *previewCheckFixture) finish(t *testing.T, check previewcheck.Check, passed bool) {
	t.Helper()
	if _, _, err := fixture.checks.Claim(t.Context(), check.ID); err != nil {
		t.Fatalf("claim the preview check: %v", err)
	}
	evidence := previewcheck.Evidence{Stage: previewcheck.StageReady, Open: "web:5173", Services: []previewcheck.ServiceEvidence{
		{Service: "web", State: "running"}, {Service: "api", State: "running"},
	}}
	if !passed {
		evidence = previewcheck.Evidence{Stage: previewcheck.StageStart, Reason: "service api exited with code 1",
			Open: "web:5173", Services: []previewcheck.ServiceEvidence{
				{Service: "web", State: "running", Logs: "VITE ready\n"},
				{Service: "api", State: "exited", ExitCode: 1, Logs: previewCheckLog + "\n"},
			}}
	}
	if _, _, err := fixture.checks.Complete(t.Context(), check.ID, passed, evidence); err != nil {
		t.Fatalf("complete the preview check: %v", err)
	}
	if err := fixture.starter.PreviewCheckCompleted(t.Context(), check.ID); err != nil {
		t.Fatalf("report the preview check: %v", err)
	}
}

// fixRequests are the turns that handed a failed check to the lead.
func (fixture *previewCheckFixture) fixRequests() []workerhttp.PutAttemptRequest {
	var fixes []workerhttp.PutAttemptRequest
	for _, request := range fixture.requests() {
		if strings.Contains(request.Instructions, "Commitarium's preview check:") {
			fixes = append(fixes, request)
		}
	}
	return fixes
}

func TestPreviewSetupMergesOnlyOnceItsPreviewCheckPasses(t *testing.T) {
	fixture := startPreviewSetupRun(t, "run_preview_check_pass", 0)
	check := fixture.waitForCheck(t, leadOnlyApprovedSHA)
	if fixture.workspace.mergeCalls != 0 {
		t.Fatal("the work order merged before its preview check")
	}
	if _, _, err := fixture.starter.Merge(t.Context(), fixture.runID, "merge-unchecked"); !errors.Is(err, ErrPreviewCheckRequired) {
		t.Fatalf("merge before the preview check = %v", err)
	}
	told := false
	for _, request := range fixture.requests() {
		told = told || (request.OutputContract == workerhttp.OutputContractImplementationLead &&
			strings.Contains(request.Instructions, "Commitarium's preview check builds and starts"))
	}
	if !told {
		t.Fatal("the lead was not told how its setup is checked")
	}
	fixture.finish(t, check, true)
	fixture.waitMerged(t)
	if merged := fixture.workspace.mergedCommits; len(merged) != 1 || merged[0] != leadOnlyApprovedSHA {
		t.Fatalf("merged %v, want the checked commit", merged)
	}
}

func TestFailedPreviewCheckGoesBackToTheSameLeadWithItsEvidence(t *testing.T) {
	fixture := startPreviewSetupRun(t, "run_preview_check_fix", 1)
	var during execution.Run
	fixture.worker.onPut = func(reference workerhttp.AttemptReference) {
		if reference.AttemptID == implementationAttemptID(remoteLeadSessionID(fixture.runID), 2) {
			during, _ = fixture.executions.GetRun(t.Context(), fixture.runID)
		}
	}
	fixture.finish(t, fixture.waitForCheck(t, leadOnlyApprovedSHA), false)
	fixed := fixture.waitForCheck(t, previewFixCommits[0])

	fixes := fixture.fixRequests()
	if len(fixes) != 1 || fixes[0].Mode != workerhttp.AttemptModeResume || fixes[0].ProviderSessionID != "codex-thread-test" ||
		!strings.Contains(fixes[0].Instructions, previewCheckLog) ||
		!strings.Contains(fixes[0].Instructions, `Service "api": state exited, exit code 1`) ||
		!strings.Contains(fixes[0].Instructions, "do not stop because you cannot run Docker") {
		t.Fatalf("the lead did not get the check's evidence in its conversation: %+v", fixes)
	}
	// The fix is the work order's own step: the run never blocked, so
	// autopilot had nothing to continue.
	if during.Status != execution.RunStatusRunning || during.Reason != previewCheckFixingReason {
		t.Fatalf("the run during the fix = %+v", during)
	}
	if fixture.workspace.mergeReadyClears != 1 || fixture.workspace.prepared.ApprovedCommitID != previewFixCommits[0] {
		t.Fatalf("the fix did not replace the checked commit: %+v", fixture.workspace)
	}

	fixture.finish(t, fixed, true)
	fixture.waitMerged(t)
	if merged := fixture.workspace.mergedCommits; len(merged) != 1 || merged[0] != previewFixCommits[0] {
		t.Fatalf("merged %v, want the fixed commit", merged)
	}
	events, err := fixture.executions.EventsForSession(t.Context(), remoteLeadSessionID(fixture.runID))
	if err != nil {
		t.Fatalf("lead events: %v", err)
	}
	for _, event := range events {
		if event.Type == worker.EventUserMessage {
			t.Fatalf("Commitarium's own fix was shown as someone's guidance: %+v", event)
		}
	}
}

func TestPreviewCheckFixesStopAtTheLimitWithOnePlainReason(t *testing.T) {
	fixture := startPreviewSetupRun(t, "run_preview_check_limit", maxPreviewFixes+1)
	commits := append([]string{leadOnlyApprovedSHA}, previewFixCommits...)
	for _, commitID := range commits[:maxPreviewFixes+1] {
		fixture.finish(t, fixture.waitForCheck(t, commitID), false)
	}
	blocked := fixture.waitBlocked(t, "")
	want := previewCheckBlockedPrefix + `the "api" part of the app stops right after it starts (exit code 1)` + previewCheckBlockedSuffix
	if blocked.Reason != want || len(fixture.fixRequests()) != maxPreviewFixes {
		t.Fatalf("after %d fixes: run=%+v fixes=%d", maxPreviewFixes, blocked, len(fixture.fixRequests()))
	}

	// The user's Continue gives the lead one more try, with its guidance.
	if _, admitted, err := fixture.starter.RecoverBlockerWithDirective(
		t.Context(), fixture.runID, "continue-preview",
		RecoveryDirective{Action: RecoveryActionContinue, Message: "Pin fastapi in requirements.txt."},
	); err != nil || !admitted {
		t.Fatalf("continue the blocked preview setup: admitted=%t err=%v", admitted, err)
	}
	last := fixture.waitForCheck(t, previewFixCommits[maxPreviewFixes])
	fixes := fixture.fixRequests()
	if len(fixes) != maxPreviewFixes+1 || !strings.Contains(fixes[maxPreviewFixes].Instructions, "Pin fastapi in requirements.txt.") ||
		!strings.Contains(fixes[maxPreviewFixes].Instructions, previewCheckLog) {
		t.Fatalf("continue did not hand the evidence and guidance to the lead: %d fixes", len(fixes))
	}
	fixture.finish(t, last, true)
	fixture.waitMerged(t)
}

func TestUnclaimedPreviewCheckBlocksUntilTheAppIsOpen(t *testing.T) {
	fixture := startPreviewSetupRun(t, "run_preview_check_no_desktop", 0)
	check := fixture.waitForCheck(t, leadOnlyApprovedSHA)
	if blocked, err := fixture.starter.BlockOverduePreviewChecks(t.Context()); err != nil || blocked != 0 {
		t.Fatalf("a fresh check blocked its run: %d %v", blocked, err)
	}
	fixture.clock.Advance(previewcheck.ClaimTimeout)
	if blocked, err := fixture.starter.BlockOverduePreviewChecks(t.Context()); err != nil || blocked != 1 {
		t.Fatalf("an overdue check did not block its run: %d %v", blocked, err)
	}
	fixture.waitBlocked(t, previewCheckNoDesktopReason)

	// Continue asks for a new check; a desktop that opens runs it.
	if _, admitted, err := fixture.starter.RecoverBlockerWithDirective(
		t.Context(), fixture.runID, "continue-no-desktop", RecoveryDirective{Action: RecoveryActionContinue},
	); err != nil || !admitted {
		t.Fatalf("continue the unchecked work order: admitted=%t err=%v", admitted, err)
	}
	again := fixture.waitForCheck(t, leadOnlyApprovedSHA)
	expired, _ := fixture.checks.Get(t.Context(), check.ID)
	if again.ID == check.ID || again.Attempt != 2 || expired.Status != previewcheck.StatusExpired {
		t.Fatalf("continue did not replace the overdue check: new=%+v old=%+v", again, expired)
	}
	fixture.finish(t, again, true)
	fixture.waitMerged(t)
}

func TestADesktopThatOpensLateFinishesTheBlockedCheck(t *testing.T) {
	fixture := startPreviewSetupRun(t, "run_preview_check_late_desktop", 0)
	check := fixture.waitForCheck(t, leadOnlyApprovedSHA)
	fixture.clock.Advance(previewcheck.ClaimTimeout)
	if _, err := fixture.starter.BlockOverduePreviewChecks(t.Context()); err != nil {
		t.Fatalf("block overdue checks: %v", err)
	}
	fixture.waitBlocked(t, previewCheckNoDesktopReason)
	fixture.finish(t, check, true)
	fixture.waitMerged(t)
}
