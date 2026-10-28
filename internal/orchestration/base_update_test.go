package orchestration

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

var (
	baseUpdateMainHead  = strings.Repeat("c", 40)
	baseUpdateCommits   = []string{strings.Repeat("e", 40), strings.Repeat("f", 40), strings.Repeat("1", 40), strings.Repeat("2", 40)}
	baseUpdateNoMerge   = workspace.ErrPullRequestNotMergeable
	baseUpdateGuidance  = "Keep the new date format from main."
	reviewedApprovedSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	leadOnlyApprovedSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// baseUpdateFixture is a work order run end to end against scripted agents,
// up to the point where Forgejo is asked to merge it.
type baseUpdateFixture struct {
	db         *sql.DB
	executions *execution.Service
	features   *database.FeatureStore
	workflow   *workflow.Service
	validation *validation.Service
	starter    *RemoteLeadStarter
	worker     *conversationalRemoteLeadWorker
	workspace  *remoteLeadWorkspaceStub
	project    project.Project
	runID      string
	featureID  string
}

// startBaseUpdateRun starts a run-to-completion work order. A reviewed one is
// approved at commit b after one correction; one without a reviewer
// publishes commit a. updates scripts that many updates onto main.
func startBaseUpdateRun(
	t *testing.T,
	runID string,
	reviewed bool,
	policy project.MergePolicy,
	updates int,
	prepare func(*baseUpdateFixture),
) *baseUpdateFixture {
	t.Helper()
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	storedProject, err := database.NewProjectStore(db).UpdateAutonomyPolicy(
		t.Context(), storedProject.ID, project.AutonomyPolicyRunToCompletion,
	)
	if err != nil {
		t.Fatalf("enable run-to-completion: %v", err)
	}
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	if reviewed {
		addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
		addCompletedPlanningCorrectionAttempts(stub, runID, storedProject.ID, storedFeature.ID)
	} else {
		leadSessionID := remoteLeadSessionID(runID)
		planning := workerhttp.AttemptReference{SessionID: leadSessionID, AttemptID: planningAttemptID(leadSessionID)}
		stub.events[planning][1].Type = workerhttp.EventPlanSubmitted
		stub.events[planning][1].Text = "Complete final implementation plan"
	}
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	if reviewed {
		addCompletedImplementationReviewAttempt(stub, runID, storedProject.ID, storedFeature.ID)
		addCompletedImplementationCorrectionAttempt(
			stub, runID, storedProject.ID, storedFeature.ID, 1, reviewedApprovedSHA,
			"Published the corrected implementation and added the missing failure-path test.",
		)
		addCompletedLaterImplementationReviewAttempt(
			stub, runID, storedProject.ID, storedFeature.ID, 2, workerhttp.DispositionSucceeded,
			reviewedApprovedSHA, 12, "The correction addresses every finding and passes the relevant tests.",
		)
		addCompletedImplementationReadinessAttempt(
			stub, runID, storedProject.ID, storedFeature.ID, 2, workerhttp.DispositionSucceeded,
			"I agree with the reviewer; this exact commit is ready to merge.",
		)
	}
	for turn := 1; turn <= updates; turn++ {
		addCompletedBaseUpdateAttempt(
			stub, runID, storedProject.ID, storedFeature.ID, turn, baseUpdateCommits[turn-1],
			"Merged main; resolved the conflict in the exporter by keeping both changes. Tests pass.",
		)
	}

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.October, 12, 11, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_base_update", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "base-update-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_base_update",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	features := database.NewFeatureStore(db)
	validations := validation.NewService(database.NewValidationStore(db))
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: features, Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:       &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Validation: validations,
		Lifetime:   t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if !reviewed {
		chosen, err := features.GetByID(t.Context(), storedFeature.ID)
		if err != nil {
			t.Fatalf("load ready feature: %v", err)
		}
		chosen.Steps = project.WorkSteps{SkipReviewer: true}
		if err := features.UpdateReadySettings(t.Context(), chosen); err != nil {
			t.Fatalf("choose steps: %v", err)
		}
	}
	fixture := &baseUpdateFixture{
		db: db, executions: executions, features: features, workflow: workflowService,
		validation: validations, starter: starter, worker: stub, workspace: workspaceStub,
		project: storedProject, runID: runID, featureID: storedFeature.ID,
	}
	if prepare != nil {
		prepare(fixture)
	}
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		policy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start: %v", err)
	}
	return fixture
}

// waitFor waits until the run and its work order settle in the wanted state
// with no coordinator goroutine still holding the run.
func (fixture *baseUpdateFixture) waitFor(
	t *testing.T,
	what string,
	done func(execution.Run, feature.Feature) bool,
) execution.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, runErr := fixture.executions.GetRun(t.Context(), fixture.runID)
		current, featureErr := fixture.features.GetByID(t.Context(), fixture.featureID)
		if runErr == nil && featureErr == nil && done(run, current) && fixture.starter.claim(fixture.runID) {
			fixture.starter.release(fixture.runID)
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not reach %s: run=%+v feature=%+v errors=%v %v", what, run, current.State, runErr, featureErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (fixture *baseUpdateFixture) waitAtMergeGate(t *testing.T) execution.Run {
	t.Helper()
	return fixture.waitFor(t, "the merge gate", func(run execution.Run, current feature.Feature) bool {
		return current.State == feature.StateReadyToMerge && run.Status == execution.RunStatusWaitingForUser &&
			run.WaitKind == execution.RunWaitKindMergeGate
	})
}

func (fixture *baseUpdateFixture) waitBlocked(t *testing.T, reason string) execution.Run {
	t.Helper()
	return fixture.waitFor(t, "a blocker", func(run execution.Run, _ feature.Feature) bool {
		return run.Status == execution.RunStatusWaitingForUser && run.WaitKind == execution.RunWaitKindBlocker &&
			(reason == "" || run.Reason == reason)
	})
}

func (fixture *baseUpdateFixture) waitMerged(t *testing.T) execution.Run {
	t.Helper()
	return fixture.waitFor(t, "the merge", func(run execution.Run, current feature.Feature) bool {
		return run.Status == execution.RunStatusSucceeded && current.State == feature.StateCompleted
	})
}

// requests returns the instructions of every turn sent to the agents.
func (fixture *baseUpdateFixture) requests() []workerhttp.PutAttemptRequest {
	fixture.worker.mu.Lock()
	defer fixture.worker.mu.Unlock()
	return append([]workerhttp.PutAttemptRequest(nil), fixture.worker.putRequests...)
}

// updateRequests returns the update-onto-main turns sent to the lead.
func (fixture *baseUpdateFixture) updateRequests() []workerhttp.PutAttemptRequest {
	var updates []workerhttp.PutAttemptRequest
	for _, request := range fixture.requests() {
		if strings.Contains(request.Instructions, "<!-- commitarium-base-update: ") {
			updates = append(updates, request)
		}
	}
	return updates
}

func (fixture *baseUpdateFixture) leadCheckpoint(t *testing.T) execution.WorkerAttemptCheckpoint {
	t.Helper()
	checkpoint, err := fixture.executions.GetWorkerAttempt(t.Context(), remoteLeadSessionID(fixture.runID))
	if err != nil {
		t.Fatalf("load lead checkpoint: %v", err)
	}
	return checkpoint
}

func addCompletedBaseUpdateAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
	turn int,
	commitID string,
	summary string,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: baseUpdateAttemptForVersion(sessionID, 1, turn),
	}
	now := time.Date(2026, time.October, 12, 12, turn, 0, 0, time.UTC)
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
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 2
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary:     summary,
		Publication: &workerhttp.ImplementationPublication{CommitID: commitID, PullRequestNumber: 7},
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage, Text: summary, OccurredAt: now},
		{AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal, Text: summary, OccurredAt: endedAt},
	}
}

func TestReviewedWorkOrderBringsMainInAndMergesTheReviewedUpdate(t *testing.T) {
	updated := baseUpdateCommits[0]
	fixture := startBaseUpdateRun(t, "run_reviewed_base_update", true, project.MergePolicyAutoAfterGates, 1,
		func(fixture *baseUpdateFixture) {
			fixture.workspace.mainHead = baseUpdateMainHead
			fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}
			addCompletedLaterImplementationReviewAttempt(
				fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 3,
				workerhttp.DispositionSucceeded, updated, 13,
				"The merge kept every approved change and the tests pass.",
			)
			addCompletedImplementationReadinessAttempt(
				fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 3,
				workerhttp.DispositionSucceeded, "I agree; the updated commit is ready to merge.",
			)
		})
	fixture.waitMerged(t)

	stub := fixture.workspace
	if stub.mergeCalls != 2 || len(stub.mergedCommits) != 1 || stub.mergedCommits[0] != updated {
		t.Fatalf("expected the updated commit to merge after one refusal: calls=%d merged=%v", stub.mergeCalls, stub.mergedCommits)
	}
	if stub.baseUpdateVerifyCalls != 1 || stub.baseUpdateVerifyCommit != updated ||
		stub.mergeReadyClears != 1 || stub.reviewVerifyCalls != 3 || stub.readinessVerifyCalls != 2 {
		t.Fatalf("the update did not pass every gate once: %+v", stub)
	}
	updates := fixture.updateRequests()
	if len(updates) != 1 || !strings.Contains(updates[0].Instructions, "Exact approved commit: "+reviewedApprovedSHA) ||
		updates[0].OutputContract != workerhttp.OutputContractImplementationLead {
		t.Fatalf("lead was not asked to bring main into the approved commit: %+v", updates)
	}
	var rereview workerhttp.PutAttemptRequest
	for _, request := range fixture.requests() {
		if request.Assignment.Role == workerhttp.RoleReviewer && strings.Contains(request.Instructions, "Exact implementation commit: "+updated) {
			rereview = request
		}
	}
	if !strings.Contains(rereview.Instructions, "git show --remerge-diff "+updated) {
		t.Fatalf("reviewer was not asked to review only the merge: %q", rereview.Instructions)
	}
	events, err := fixture.executions.EventsForSession(t.Context(), remoteLeadSessionID(fixture.runID))
	if err != nil {
		t.Fatalf("load lead events: %v", err)
	}
	intent := baseUpdateIntentID(implementationReadinessAttemptForVersion(remoteLeadSessionID(fixture.runID), 1, 2))
	found := false
	for _, event := range events {
		found = found || event.ID == intent
	}
	if !found {
		t.Fatal("the update onto main was not recorded on the lead's approved turn")
	}
}

func TestMergeOfALeadOnlyWorkOrderBehindMainStartsAnUpdate(t *testing.T) {
	updated := baseUpdateCommits[0]
	fixture := startBaseUpdateRun(t, "run_lead_only_base_update", false, project.MergePolicyRequireUserApproval, 1, nil)
	fixture.waitAtMergeGate(t)
	fixture.workspace.mainHead = baseUpdateMainHead
	fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}

	started, merged, err := fixture.starter.Merge(t.Context(), fixture.runID, "merge-behind-main")
	if err != nil || merged || started.Status == execution.RunStatusSucceeded {
		t.Fatalf("merge behind main did not start an update: run=%+v merged=%t err=%v", started, merged, err)
	}
	gated := fixture.waitAtMergeGate(t)
	if gated.Reason != implementationLeadOnlyReadyReason || fixture.workspace.prepared.ApprovedCommitID != updated ||
		fixture.workspace.publicationVerifyCalls != 1 || fixture.workspace.baseUpdateVerifyCalls != 1 ||
		fixture.leadCheckpoint(t).AttemptID != baseUpdateAttemptForVersion(remoteLeadSessionID(fixture.runID), 1, 1) {
		t.Fatalf("the update did not return to the merge gate at the new commit: run=%+v workspace=%+v", gated, fixture.workspace)
	}

	if _, merged, err := fixture.starter.Merge(t.Context(), fixture.runID, "merge-updated"); err != nil || !merged {
		t.Fatalf("merge updated commit: merged=%t err=%v", merged, err)
	}
	if len(fixture.workspace.mergedCommits) != 1 || fixture.workspace.mergedCommits[0] != updated {
		t.Fatalf("merged %v, want the updated commit", fixture.workspace.mergedCommits)
	}
}

func TestAutomaticUpdatesStopAtTheLimitAndContinueTakesGuidance(t *testing.T) {
	fixture := startBaseUpdateRun(t, "run_base_update_limit", false, project.MergePolicyAutoAfterGates, 4,
		func(fixture *baseUpdateFixture) {
			fixture.workspace.mainHead = baseUpdateMainHead
			fixture.workspace.stillBehind = true
			fixture.workspace.mergeErrs = []error{baseUpdateNoMerge, baseUpdateNoMerge, baseUpdateNoMerge, baseUpdateNoMerge}
		})
	blocked := fixture.waitBlocked(t, implementationMergeBehindMainReason)
	if fixture.workspace.mergeCalls != 4 || fixture.workspace.baseUpdateVerifyCalls != maxAutomaticBaseUpdates ||
		len(fixture.updateRequests()) != maxAutomaticBaseUpdates || blocked.Reason != implementationMergeBehindMainReason {
		t.Fatalf("automatic updates did not stop after %d: run=%+v workspace=%+v", maxAutomaticBaseUpdates, blocked, fixture.workspace)
	}
	for _, update := range fixture.updateRequests() {
		if strings.Contains(update.Instructions, "User guidance") {
			t.Fatalf("an automatic update carried user guidance: %q", update.Instructions)
		}
	}

	if _, admitted, err := fixture.starter.RecoverBlockerWithDirective(
		t.Context(), fixture.runID, "continue-behind-main",
		RecoveryDirective{Action: RecoveryActionContinue, Message: baseUpdateGuidance},
	); err != nil || !admitted {
		t.Fatalf("continue behind main: admitted=%t err=%v", admitted, err)
	}
	fixture.waitMerged(t)
	updates := fixture.updateRequests()
	if len(updates) != 4 || !strings.Contains(updates[3].Instructions, "User guidance: "+baseUpdateGuidance) {
		t.Fatalf("continue did not start a guided update: %d updates", len(updates))
	}
	if merged := fixture.workspace.mergedCommits; len(merged) != 1 || merged[0] != baseUpdateCommits[3] {
		t.Fatalf("merged %v, want the guided update's commit", merged)
	}
}

// Guidance given with Continue on the behind-main blocker shows in the lead's
// transcript as a message from whoever wrote it, as a guided Continue does.
func TestContinueBehindMainShowsItsGuidance(t *testing.T) {
	for _, author := range []string{"user", "assistant"} {
		t.Run(author, func(t *testing.T) {
			fixture := startBaseUpdateRun(t, "run_base_update_guidance_"+author, false, project.MergePolicyAutoAfterGates, 4,
				func(fixture *baseUpdateFixture) {
					fixture.workspace.mainHead = baseUpdateMainHead
					fixture.workspace.stillBehind = true
					fixture.workspace.mergeErrs = []error{baseUpdateNoMerge, baseUpdateNoMerge, baseUpdateNoMerge, baseUpdateNoMerge}
				})
			fixture.waitBlocked(t, implementationMergeBehindMainReason)
			if _, admitted, err := fixture.starter.RecoverBlockerWithDirective(
				t.Context(), fixture.runID, "continue-behind-main-"+author,
				RecoveryDirective{Action: RecoveryActionContinue, Message: baseUpdateGuidance, FromAssistant: author == "assistant"},
			); err != nil || !admitted {
				t.Fatalf("continue behind main: admitted=%t err=%v", admitted, err)
			}
			fixture.waitMerged(t)
			events, err := fixture.executions.EventsForSession(t.Context(), remoteLeadSessionID(fixture.runID))
			if err != nil {
				t.Fatalf("lead events: %v", err)
			}
			shown := 0
			for _, event := range events {
				if event.Type == worker.EventUserMessage {
					shown++
					if event.Text != baseUpdateGuidance || !strings.HasSuffix(event.ID, ":guidance:"+author) {
						t.Fatalf("guidance shown as %+v", event)
					}
				}
			}
			if shown != 1 {
				t.Fatalf("guidance shown %d times, want once", shown)
			}
		})
	}
}

func TestRefusedMergeWithoutMainMovingKeepsThePlainBlocker(t *testing.T) {
	t.Run("not mergeable while up to date", func(t *testing.T) {
		fixture := startBaseUpdateRun(t, "run_not_mergeable_up_to_date", false, project.MergePolicyAutoAfterGates, 0,
			func(fixture *baseUpdateFixture) {
				fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}
			})
		fixture.waitBlocked(t, implementationMergeBlockedReason)
		// Continue with guidance retries the merge; main has not moved.
		if _, admitted, err := fixture.starter.RecoverBlockerWithDirective(
			t.Context(), fixture.runID, "continue-up-to-date",
			RecoveryDirective{Action: RecoveryActionContinue, Message: baseUpdateGuidance},
		); err != nil || !admitted {
			t.Fatalf("continue: admitted=%t err=%v", admitted, err)
		}
		fixture.waitMerged(t)
		if len(fixture.updateRequests()) != 0 || fixture.workspace.mergedCommits[0] != leadOnlyApprovedSHA {
			t.Fatalf("an up-to-date work order was updated: merged=%v", fixture.workspace.mergedCommits)
		}
	})
	t.Run("branch conflict while behind", func(t *testing.T) {
		fixture := startBaseUpdateRun(t, "run_branch_conflict_behind", false, project.MergePolicyAutoAfterGates, 0,
			func(fixture *baseUpdateFixture) {
				fixture.workspace.mainHead = baseUpdateMainHead
				fixture.workspace.mergeErrs = []error{workspace.ErrBranchConflict}
			})
		fixture.waitBlocked(t, implementationMergeBlockedReason)
		if len(fixture.updateRequests()) != 0 || fixture.workspace.baseUpdateVerifyCalls != 0 {
			t.Fatal("a branch conflict started an update onto main")
		}
	})
}

func TestBaseUpdateResumesAfterARestartWithoutTheOldCommit(t *testing.T) {
	updated := baseUpdateCommits[0]
	recordIntent := func(t *testing.T, fixture *baseUpdateFixture) string {
		t.Helper()
		checkpoint := fixture.leadCheckpoint(t)
		if _, err := fixture.executions.RecordSessionEventWithID(
			t.Context(), baseUpdateIntentID(checkpoint.AttemptID), checkpoint.SessionID,
			worker.Event{Type: worker.EventActivity, Text: baseUpdateGuidancePrefix + baseUpdateGuidance},
		); err != nil {
			t.Fatalf("record update request: %v", err)
		}
		return checkpoint.AttemptID
	}
	recoverRun := func(t *testing.T, fixture *baseUpdateFixture) {
		t.Helper()
		run, err := fixture.executions.GetRun(t.Context(), fixture.runID)
		if err != nil {
			t.Fatalf("load run: %v", err)
		}
		current, err := fixture.features.GetByID(t.Context(), fixture.featureID)
		if err != nil {
			t.Fatalf("load feature: %v", err)
		}
		if err := fixture.starter.Recover(t.Context(), run, current, fixture.project.RecoveryPolicy); err != nil {
			t.Fatalf("recover: %v", err)
		}
	}
	expectUpdated := func(t *testing.T, fixture *baseUpdateFixture) {
		t.Helper()
		fixture.waitAtMergeGate(t)
		if fixture.workspace.prepared.ApprovedCommitID != updated || fixture.workspace.publicationVerifyCalls != 1 {
			t.Fatalf("recovery went back to the old commit: workspace=%+v", fixture.workspace)
		}
		updates := fixture.updateRequests()
		if len(updates) != 1 || !strings.Contains(updates[0].Instructions, "User guidance: "+baseUpdateGuidance) {
			t.Fatalf("recovery did not resume the requested update with its guidance: %d updates", len(updates))
		}
	}

	t.Run("requested at the merge gate", func(t *testing.T) {
		fixture := startBaseUpdateRun(t, "run_restart_requested", false, project.MergePolicyRequireUserApproval, 1, nil)
		fixture.waitAtMergeGate(t)
		recordIntent(t, fixture)
		recoverRun(t, fixture)
		expectUpdated(t, fixture)
	})
	t.Run("left the merge gate", func(t *testing.T) {
		fixture := startBaseUpdateRun(t, "run_restart_left_gate", false, project.MergePolicyRequireUserApproval, 1, nil)
		fixture.waitAtMergeGate(t)
		attemptID := recordIntent(t, fixture)
		if _, err := fixture.workflow.TransitionFeature(
			t.Context(), fixture.featureID, feature.StateImplementing,
			workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
			baseUpdateAttemptForVersion(remoteLeadSessionID(fixture.runID), 1, 1)+":base-update",
		); err != nil {
			t.Fatalf("leave merge gate after %s: %v", attemptID, err)
		}
		recoverRun(t, fixture)
		expectUpdated(t, fixture)
	})
	t.Run("update published but not verified", func(t *testing.T) {
		fixture := startBaseUpdateRun(t, "run_restart_unverified", false, project.MergePolicyRequireUserApproval, 1, nil)
		fixture.waitAtMergeGate(t)
		fixture.workspace.mainHead = baseUpdateMainHead
		fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}
		fixture.workspace.baseUpdateVerifyErr = workspace.ErrBranchConflict
		if _, _, err := fixture.starter.Merge(t.Context(), fixture.runID, "merge-unverified"); err != nil {
			t.Fatalf("merge behind main: %v", err)
		}
		fixture.waitBlocked(t, "")
		fixture.workspace.baseUpdateVerifyErr = nil
		if _, _, err := fixture.starter.RecoverBlockerWithDirective(
			t.Context(), fixture.runID, "recheck-update", RecoveryDirective{Action: RecoveryActionRecheck},
		); err != nil {
			t.Fatalf("recheck update: %v", err)
		}
		fixture.waitAtMergeGate(t)
		if fixture.workspace.prepared.ApprovedCommitID != updated || fixture.workspace.publicationVerifyCalls != 1 ||
			fixture.workspace.baseUpdateVerifyCalls != 2 || len(fixture.updateRequests()) != 1 {
			t.Fatalf("recheck did not verify the published update: workspace=%+v", fixture.workspace)
		}
	})
	t.Run("reviewed update and its review", func(t *testing.T) {
		fixture := startBaseUpdateRun(t, "run_restart_reviewed", true, project.MergePolicyAutoAfterGates, 1,
			func(fixture *baseUpdateFixture) {
				fixture.workspace.mainHead = baseUpdateMainHead
				fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}
				fixture.workspace.baseUpdateVerifyErr = workspace.ErrBranchConflict
				fixture.workspace.failReviewVerifyCall = 3
				addCompletedLaterImplementationReviewAttempt(
					fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 3,
					workerhttp.DispositionSucceeded, updated, 13, "The merge kept every approved change.",
				)
				addCompletedImplementationReadinessAttempt(
					fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 3,
					workerhttp.DispositionSucceeded, "I agree; the updated commit is ready to merge.",
				)
			})
		fixture.waitBlocked(t, "")
		fixture.workspace.baseUpdateVerifyErr = nil
		if _, _, err := fixture.starter.RecoverBlockerWithDirective(
			t.Context(), fixture.runID, "recheck-reviewed-update", RecoveryDirective{Action: RecoveryActionRecheck},
		); err != nil {
			t.Fatalf("recheck reviewed update: %v", err)
		}
		fixture.waitBlocked(t, "")
		if fixture.workspace.reviewVerifyCalls != 3 {
			t.Fatalf("the review of the update was not reached: %+v", fixture.workspace)
		}
		if _, _, err := fixture.starter.RecoverBlockerWithDirective(
			t.Context(), fixture.runID, "recheck-update-review", RecoveryDirective{Action: RecoveryActionRecheck},
		); err != nil {
			t.Fatalf("recheck update review: %v", err)
		}
		fixture.waitMerged(t)
		if fixture.workspace.baseUpdateVerifyCalls != 2 || len(fixture.updateRequests()) != 1 ||
			len(fixture.workspace.mergedCommits) != 1 || fixture.workspace.mergedCommits[0] != updated {
			t.Fatalf("recovery of the reviewed update merged the wrong commit: %+v", fixture.workspace)
		}
	})
}

func TestUpdateOntoMainResetsAcceptanceTestsForTheNewCommit(t *testing.T) {
	updated := baseUpdateCommits[0]
	fixture := startBaseUpdateRun(t, "run_base_update_acceptance", true, project.MergePolicyRequireUserApproval, 1,
		func(fixture *baseUpdateFixture) {
			addCompletedLaterImplementationReviewAttempt(
				fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 3,
				workerhttp.DispositionSucceeded, updated, 13, "The merge kept every approved change.",
			)
			addCompletedImplementationReadinessAttempt(
				fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 3,
				workerhttp.DispositionSucceeded, "I agree; the updated commit is ready to merge.",
			)
		})
	fixture.waitAtMergeGate(t)
	// The approved work order had private acceptance tests, all passing on
	// the approved commit.
	if _, err := fixture.db.ExecContext(
		t.Context(), `UPDATE runs SET independent_acceptance_tests = 1 WHERE id = ?`, fixture.runID,
	); err != nil {
		t.Fatalf("enable acceptance tests: %v", err)
	}
	tests := featureartifact.AcceptanceTests{
		PlanVersion: 1, TestCommitID: strings.Repeat("9", 40), ImplementationCommitID: reviewedApprovedSHA,
		Tests: []featureartifact.AcceptanceTest{
			{ID: "at-1", Position: 1, Title: "Exports the visible columns", Status: featureartifact.AcceptanceTestPassed},
			{ID: "at-2", Position: 2, Title: "Exports to Excel", Status: featureartifact.AcceptanceTestNotApplicable, Note: "Out of scope."},
		},
	}
	actor := workflow.Actor{Kind: workflow.ActorKindAgent, ID: remoteReviewerSessionID(fixture.runID)}
	if _, err := fixture.workflow.UpsertAcceptanceTests(t.Context(), fixture.featureID, tests, actor, "seed-acceptance"); err != nil {
		t.Fatalf("seed acceptance tests: %v", err)
	}
	fixture.starter.artifacts = fixture.workflow
	fixture.workspace.mainHead = baseUpdateMainHead
	fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}
	// Its refusal blocks, as once Commitarium's corrections are spent.
	fixture.spendSelfCorrections(t, errAcceptanceResultsIncomplete)
	if _, _, err := fixture.starter.Merge(t.Context(), fixture.runID, "merge-with-acceptance"); err != nil {
		t.Fatalf("merge behind main: %v", err)
	}
	// The reviewer has not recorded results for the updated commit yet, so
	// its review cannot be accepted.
	if blocked := fixture.waitBlocked(t, ""); !strings.Contains(blocked.Reason, "has no final result") {
		t.Fatalf("the review of the update was not held for acceptance results: %q", blocked.Reason)
	}
	artifact, err := fixture.workflow.GetFeatureArtifact(t.Context(), fixture.featureID, featureartifact.KindAcceptanceTests)
	if err != nil {
		t.Fatalf("load acceptance tests: %v", err)
	}
	var reset featureartifact.AcceptanceTests
	if err := json.Unmarshal([]byte(artifact.Document), &reset); err != nil {
		t.Fatalf("decode acceptance tests: %v", err)
	}
	if reset.ImplementationCommitID != updated || reset.Tests[0].Status != featureartifact.AcceptanceTestPending ||
		reset.Tests[1].Status != featureartifact.AcceptanceTestNotApplicable {
		t.Fatalf("acceptance tests were not reset for the updated commit: %+v", reset)
	}

	reset.Tests[0].Status = featureartifact.AcceptanceTestPassed
	if _, err := fixture.workflow.UpsertAcceptanceTests(t.Context(), fixture.featureID, reset, actor, "record-acceptance"); err != nil {
		t.Fatalf("record acceptance results: %v", err)
	}
	if _, _, err := fixture.starter.RecoverBlockerWithDirective(
		t.Context(), fixture.runID, "recheck-acceptance", RecoveryDirective{Action: RecoveryActionRecheck},
	); err != nil {
		t.Fatalf("recheck review: %v", err)
	}
	fixture.waitAtMergeGate(t)
	if fixture.workspace.prepared.ApprovedCommitID != updated {
		t.Fatalf("the updated commit did not reach the merge gate: %+v", fixture.workspace.prepared)
	}
}

func TestUpdateOntoMainIsValidatedBeforeItMerges(t *testing.T) {
	updated := baseUpdateCommits[0]
	commands := []string{"go test ./..."}
	fixture := startBaseUpdateRun(t, "run_base_update_validation", false, project.MergePolicyAutoAfterGates, 1,
		func(fixture *baseUpdateFixture) {
			if _, err := fixture.validation.Configure(t.Context(), fixture.project.ID, commands); err != nil {
				t.Fatalf("configure validation: %v", err)
			}
		})
	pass := func(t *testing.T, commitID string) {
		t.Helper()
		jobs, err := fixture.validation.JobsForRun(t.Context(), fixture.runID)
		if err != nil {
			t.Fatalf("list validation jobs: %v", err)
		}
		for _, job := range jobs {
			if job.CommitID != commitID || job.Status != validation.StatusPending {
				continue
			}
			if _, _, err := fixture.validation.Claim(t.Context(), job.ID); err != nil {
				t.Fatalf("claim validation job: %v", err)
			}
			if _, _, err := fixture.validation.Complete(t.Context(), job.ID, []validation.CommandResult{
				{Command: commands[0], ExitCode: 0, Output: "ok"},
			}, ""); err != nil {
				t.Fatalf("complete validation job: %v", err)
			}
			if err := fixture.starter.ValidationPassed(t.Context(), fixture.runID); err != nil {
				t.Fatalf("record passed validation: %v", err)
			}
			if err := fixture.starter.MergeAfterValidation(t.Context(), fixture.runID); err != nil {
				t.Fatalf("merge after validation: %v", err)
			}
			return
		}
		t.Fatalf("no pending validation job for %s: %+v", commitID, jobs)
	}

	if gated := fixture.waitAtMergeGate(t); gated.Reason != validationPendingReason {
		t.Fatalf("approved commit did not wait for validation: %+v", gated)
	}
	fixture.workspace.mainHead = baseUpdateMainHead
	fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}
	pass(t, leadOnlyApprovedSHA)
	gated := fixture.waitFor(t, "validation of the update", func(run execution.Run, current feature.Feature) bool {
		return current.State == feature.StateReadyToMerge && run.Reason == validationPendingReason &&
			fixture.workspace.prepared.ApprovedCommitID == updated
	})
	if gated.WaitKind != execution.RunWaitKindMergeGate || fixture.workspace.mergeCalls != 1 {
		t.Fatalf("the update did not wait for its own validation: run=%+v workspace=%+v", gated, fixture.workspace)
	}
	if _, _, err := fixture.starter.Merge(t.Context(), fixture.runID, "merge-unvalidated"); err == nil {
		t.Fatal("the update merged before its validation passed")
	}
	pass(t, updated)
	fixture.waitMerged(t)
	if merged := fixture.workspace.mergedCommits; len(merged) != 1 || merged[0] != updated {
		t.Fatalf("merged %v, want the validated update", merged)
	}
}

func TestReviewerCanRequestChangesToAnUpdateOntoMain(t *testing.T) {
	updated, corrected := baseUpdateCommits[0], baseUpdateCommits[1]
	fixture := startBaseUpdateRun(t, "run_base_update_changes", true, project.MergePolicyAutoAfterGates, 1,
		func(fixture *baseUpdateFixture) {
			fixture.workspace.mainHead = baseUpdateMainHead
			fixture.workspace.mergeErrs = []error{baseUpdateNoMerge}
			addCompletedLaterImplementationReviewAttempt(
				fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 3,
				workerhttp.DispositionChangesRequested, updated, 13,
				"F3: the conflict resolution dropped the approved empty-state message.",
			)
			addCompletedImplementationCorrectionAttempt(
				fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 3, corrected,
				"F3 fixed: restored the empty-state message on top of main's layout.",
			)
			addCompletedLaterImplementationReviewAttempt(
				fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 4,
				workerhttp.DispositionSucceeded, corrected, 14, "F3 is fixed.",
			)
			addCompletedImplementationReadinessAttempt(
				fixture.worker, fixture.runID, fixture.project.ID, fixture.featureID, 4,
				workerhttp.DispositionSucceeded, "I agree; the corrected commit is ready to merge.",
			)
		})
	fixture.waitMerged(t)
	if merged := fixture.workspace.mergedCommits; len(merged) != 1 || merged[0] != corrected {
		t.Fatalf("merged %v, want the corrected update", merged)
	}
	if fixture.workspace.responseReviewedCommit != updated || fixture.workspace.responseCommit != corrected {
		t.Fatalf("the correction did not answer the review of the update: %+v", fixture.workspace)
	}
}
