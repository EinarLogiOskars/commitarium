package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// Bringing approved work up to date (ADR-025): when Forgejo refuses to merge
// an approved work order because main moved past it, the lead merges main
// into the feature branch in an "update" turn. The updated commit is a new
// revision: it is verified, reviewed again when the work order has a
// reviewer, validated, and only then merged.

type remoteLeadBaseUpdateWorkspace interface {
	VerifyImplementationBaseUpdate(context.Context, string, string, string, string, string, string, string, int64, string) (workspace.Workspace, error)
}

// baseUpdateRequested is the lead activity that records the coordinator's
// decision to bring main in. Its ID names the lead turn it follows, so a
// restart resumes the same update instead of checking the old commit again.
const baseUpdateRequested = "Main moved past the approved revision and it no longer merges cleanly. " +
	"The lead is bringing main into the feature branch."

const baseUpdateGuidancePrefix = baseUpdateRequested + " The user's guidance: "

// baseUpdateAssistantGuidancePrefix marks guidance the assistant wrote (a
// continue_work_order action), so the update shows who gave it.
const baseUpdateAssistantGuidancePrefix = baseUpdateRequested + " The assistant's guidance: "

func baseUpdateIntentID(attemptID string) string {
	return attemptID + ":base-update-requested"
}

// mergeBlockedByMovedBase reports whether a refused merge is one the lead can
// fix by bringing main in: Forgejo refused it and main moved past the
// checkout. Sessions catch up in conversation instead (ADR-019).
func (starter *RemoteLeadStarter) mergeBlockedByMovedBase(ctx context.Context, run execution.Run, cause error) bool {
	if !errors.Is(cause, workspace.ErrPullRequestNotMergeable) {
		return false
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil || storedFeature.Kind == feature.KindSession {
		return false
	}
	_, behind := starter.behindMain(ctx, storedFeature)
	return behind
}

// leadAttemptPlanVersion reads the plan version from a lead turn that can
// precede the merge gate.
func leadAttemptPlanVersion(sessionID, attemptID string) (int, bool) {
	for _, kind := range []string{"implementation", "correction", "readiness", "update"} {
		if version, _, ok := workflowAttemptVersionAndTurn(sessionID, kind, attemptID); ok {
			return version, true
		}
	}
	return 0, false
}

// baseUpdateIntent returns the recorded request to bring main in after a lead
// turn, with the guidance it carried and whether the assistant wrote it.
func (starter *RemoteLeadStarter) baseUpdateIntent(
	ctx context.Context,
	sessionID string,
	attemptID string,
) (string, bool, bool, error) {
	events, err := starter.executions.EventsForSession(ctx, sessionID)
	if err != nil {
		return "", false, false, err
	}
	for _, event := range events {
		if event.ID != baseUpdateIntentID(attemptID) {
			continue
		}
		if guidance, found := strings.CutPrefix(event.Text, baseUpdateAssistantGuidancePrefix); found {
			return guidance, true, true, nil
		}
		guidance, _ := strings.CutPrefix(event.Text, baseUpdateGuidancePrefix)
		if guidance == event.Text {
			guidance = ""
		}
		return guidance, false, true, nil
	}
	return "", false, false, nil
}

// nextBaseUpdateTurn numbers a new update after every earlier one of the plan
// version. Each update the run returned from was verified, and its recovery
// successors never outnumber it, so the highest verified update bounds them.
func nextBaseUpdateTurn(
	events []execution.Event,
	sessionID string,
	version int,
	checkpoint execution.WorkerAttemptCheckpoint,
) int {
	highest := 0
	if checkpointVersion, turn, ok := workflowAttemptVersionAndTurn(sessionID, "update", checkpoint.AttemptID); ok && checkpointVersion == version {
		highest = turn
	}
	for _, event := range events {
		attemptID, found := strings.CutSuffix(event.ID, ":publication-verified")
		if !found {
			continue
		}
		if eventVersion, turn, ok := workflowAttemptVersionAndTurn(sessionID, "update", attemptID); ok &&
			eventVersion == version && turn > highest {
			highest = turn
		}
	}
	return highest + 1
}

// beginBaseUpdate starts the lead's update onto main. owned says the caller
// already holds the run and runs the turn itself; otherwise the run is
// claimed and the turn runs in the background. fromAssistant says the
// assistant, not the user, wrote the guidance.
func (starter *RemoteLeadStarter) beginBaseUpdate(
	ctx context.Context, runID string, guidance string, fromAssistant bool, owned bool,
) error {
	if !owned && !starter.claim(runID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, runID)
	}
	request, admitted, err := starter.startBaseUpdate(ctx, runID, guidance, fromAssistant)
	if err != nil || !admitted {
		if !owned {
			starter.release(runID)
		}
		return err
	}
	if owned {
		starter.launchAdmitted(request)
	} else {
		go starter.launch(request)
	}
	return nil
}

// resumeBaseUpdate continues an update onto main that was requested after
// the lead's current turn but not started before a restart. It reports
// whether there was one.
func (starter *RemoteLeadStarter) resumeBaseUpdate(ctx context.Context, run execution.Run) (bool, error) {
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return false, fmt.Errorf("load lead checkpoint: %w", err)
	}
	_, _, requested, err := starter.baseUpdateIntent(ctx, checkpoint.SessionID, checkpoint.AttemptID)
	if err != nil || !requested {
		return false, err
	}
	return true, starter.beginBaseUpdate(ctx, run.ID, "", false, false)
}

// startBaseUpdate admits the lead's turn that merges the moved default branch
// into the approved feature branch. It records the request first, then
// leaves the merge gate: a reviewed work order goes back to review, one
// without a reviewer back to implementation. The approved commit stays
// pinned, because the update is verified against it; it cannot be merged
// meanwhile, because the work order is no longer ready to merge. Retrying is
// safe: a recorded request keeps its guidance, and an admitted turn is not
// admitted again. The admitted turn shows the guidance in the lead's
// transcript, as a guided Continue does.
func (starter *RemoteLeadStarter) startBaseUpdate(
	ctx context.Context,
	runID string,
	guidance string,
	fromAssistant bool,
) (remoteLeadRequest, bool, error) {
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if run.Status.IsTerminal() {
		return remoteLeadRequest{}, false, ErrImplementationNotAllowed
	}
	if run.Paused {
		return remoteLeadRequest{}, false, ErrRunControlNotAllowed
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	target := feature.StateReviewing
	if !run.Steps.CodeReview() {
		target = feature.StateImplementing
	}
	if storedFeature.Kind == feature.KindSession ||
		(storedFeature.State != feature.StateReadyToMerge && storedFeature.State != target) {
		return remoteLeadRequest{}, false, fmt.Errorf(
			"%w: an update onto main cannot start while the work order is %q",
			ErrImplementationNotAllowed, storedFeature.State,
		)
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if lead.Status != execution.SessionStatusWaitingForUser ||
		lead.AgentID != agentID(run.AgentProviders.LeadAgent, worker.RoleLead) || lead.Role != worker.RoleLead ||
		lead.ProviderSessionID == "" {
		return remoteLeadRequest{}, false, errors.New("lead conversation is not safely resumable for an update onto main")
	}
	if version, ok := leadAttemptPlanVersion(lead.ID, checkpoint.AttemptID); !ok || version != run.PlanVersion {
		return remoteLeadRequest{}, false, errors.New("lead checkpoint does not belong to the current plan")
	}
	storedGuidance, storedFromAssistant, requested, err := starter.baseUpdateIntent(ctx, lead.ID, checkpoint.AttemptID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if requested {
		guidance, fromAssistant = storedGuidance, storedFromAssistant
	} else if storedFeature.State != feature.StateReadyToMerge {
		return remoteLeadRequest{}, false, fmt.Errorf(
			"%w: no update onto main was requested", ErrImplementationNotAllowed,
		)
	}
	if err := starter.confirmCompletedTurn(ctx, lead, checkpoint); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("confirm lead before an update onto main: %w", err)
	}
	prepared, err := starter.workspaceForCurrentPlan(ctx, run, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	if !workspace.ValidCommitID(prepared.ApprovedCommitID) || prepared.MergeCommitID != "" {
		return remoteLeadRequest{}, false, errors.New("the work order has no unmerged approved commit to update")
	}
	events, err := starter.executions.EventsForSession(ctx, lead.ID)
	if err != nil {
		return remoteLeadRequest{}, false, err
	}
	attemptID := baseUpdateAttemptForVersion(
		lead.ID, run.PlanVersion, nextBaseUpdateTurn(events, lead.ID, run.PlanVersion, checkpoint),
	)
	mainHead, _ := starter.behindMain(ctx, storedFeature)
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent", waitingReason: implementationBaseUpdateRunningReason,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				Model:          run.AgentModels.Lead, ProjectID: storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions: implementationBaseUpdateInstructions(
				prepared, prepared.ApprovedCommitID, mainHead, guidance, attemptID,
			),
			OutputContract: workerhttp.OutputContractImplementationLead,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return remoteLeadRequest{}, false, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	if !requested {
		text := baseUpdateRequested
		if strings.TrimSpace(guidance) != "" {
			prefix := baseUpdateGuidancePrefix
			if fromAssistant {
				prefix = baseUpdateAssistantGuidancePrefix
			}
			text = prefix + strings.TrimSpace(guidance)
		}
		if _, err := starter.executions.RecordSessionEventWithID(
			ctx, baseUpdateIntentID(checkpoint.AttemptID), lead.ID,
			worker.Event{Type: worker.EventActivity, Text: text},
		); err != nil {
			return remoteLeadRequest{}, false, fmt.Errorf("record update onto main: %w", err)
		}
	}
	if storedFeature.State == feature.StateReadyToMerge {
		if _, err := starter.planning.TransitionFeature(
			ctx, storedFeature.ID, target,
			workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
			attemptID+":base-update",
		); err != nil {
			return remoteLeadRequest{}, false, fmt.Errorf("leave the merge gate for an update onto main: %w", err)
		}
	}
	var admitted bool
	if run.Status == execution.RunStatusRunning {
		admitted, err = starter.executions.BeginChainedTurnInState(
			ctx, lead.ID, checkpoint, attemptID, target, implementationBaseUpdateRunningReason,
		)
	} else {
		admitted, err = starter.executions.BeginAutonomousTurn(
			ctx, lead.ID, checkpoint, attemptID, target, implementationBaseUpdateRunningReason,
		)
	}
	if err == nil && admitted && strings.TrimSpace(guidance) != "" {
		starter.recordGuidance(ctx, lead.ID, attemptID, strings.TrimSpace(guidance), fromAssistant)
	}
	if err == nil && admitted && mainHead != "" {
		// Where main was, so a refusal after the update can tell whether it
		// was overtaken or failed (ADR-036).
		if _, recordErr := starter.executions.RecordSessionEventWithID(
			ctx, baseUpdateOntoID(attemptID, mainHead), lead.ID,
			worker.Event{Type: worker.EventActivity, Text: "The lead is bringing in main at commit " + mainHead + "."},
		); recordErr != nil {
			starter.reportError(fmt.Errorf("record where main was for an update: %w", recordErr))
		}
	}
	return request, admitted, err
}

func (starter *RemoteLeadStarter) launchBaseUpdateVerification(
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) {
	defer starter.release(request.runID)
	if err := starter.verifyImplementationBaseUpdate(starter.lifetime, request, result); err != nil {
		starter.requireReview(starter.lifetime, request, err)
	}
}

// verifyImplementationBaseUpdate checks the lead's updated commit and sends
// it through the gates again: a reviewed work order to a review of the
// merge, one without a reviewer straight to the merge gate. The approved
// commit is released only after the update is verified against it.
func (starter *RemoteLeadStarter) verifyImplementationBaseUpdate(
	ctx context.Context,
	request remoteLeadRequest,
	result workerhttp.TerminalResult,
) error {
	if result.Publication == nil {
		return fmt.Errorf("lead completed its update onto main without structured publication facts: %w: %s",
			errPublicationResult, publicationFactsGuidance)
	}
	version, _, updating := workflowAttemptVersionAndTurn(
		request.identity.SessionID, "update", request.identity.AttemptID,
	)
	if !updating {
		return errors.New("lead update onto main has an invalid attempt identity")
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return err
	}
	if version != run.PlanVersion {
		return errors.New("lead update onto main does not belong to the current plan")
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	messages, err := starter.currentPlanningMessages(ctx, run)
	if err != nil {
		return err
	}
	if len(messages) == 0 || messages[len(messages)-1].Event.Type != worker.EventPlanSubmitted {
		return errors.New("update onto main has no durable agreed plan")
	}
	plan := messages[len(messages)-1].Event
	commitID := result.Publication.CommitID
	verifiedEventID := request.identity.AttemptID + ":publication-verified"
	verified, err := starter.sessionActivityRecorded(ctx, request.identity.SessionID, verifiedEventID)
	if err != nil {
		return err
	}
	if !verified {
		verifier, ok := starter.workspaces.(remoteLeadBaseUpdateWorkspace)
		if !ok {
			return errors.New("updates onto main cannot be verified")
		}
		prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
		if err != nil {
			return err
		}
		if _, err := verifier.VerifyImplementationBaseUpdate(
			ctx, storedFeature.ProjectID, storedFeature.ID, plan.ID, plan.Text,
			request.identity.AttemptID, result.Summary, commitID,
			result.Publication.PullRequestNumber, starter.forgejoAuthorFor(run, worker.RoleLead),
		); err != nil {
			return fmt.Errorf("verify lead update onto main: %w", rejectPublication(err))
		}
		if _, err := starter.executions.RecordSessionEventWithID(
			ctx, verifiedEventID, request.identity.SessionID,
			worker.Event{Type: worker.EventActivity, Text: fmt.Sprintf(
				"Verified lead update onto main: commit %s descends from approved commit %s, is the head of Forgejo pull request #%d, and its audit comment is attributed to %s.",
				commitID, prepared.ApprovedCommitID, result.Publication.PullRequestNumber,
				starter.forgejoAuthorFor(run, worker.RoleLead),
			)},
		); err != nil {
			return fmt.Errorf("record verified update onto main: %w", err)
		}
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return err
	}
	if prepared.ApprovedCommitID != "" && prepared.ApprovedCommitID != commitID {
		// The updated commit replaces the approved one; it is pinned again
		// only when it reaches the merge gate.
		clearer, ok := starter.workspaces.(remoteLeadMergeReadyClearer)
		if !ok {
			return errors.New("the approved commit cannot be released for an update onto main")
		}
		if err := clearer.ClearMergeReady(ctx, storedFeature.ProjectID, storedFeature.ID); err != nil {
			return fmt.Errorf("release the approved commit after an update onto main: %w", err)
		}
	}
	if !run.Steps.CodeReview() {
		return starter.enterMergeGate(
			ctx, request, run, storedFeature, commitID,
			result.Publication.PullRequestNumber, implementationLeadOnlyReadyReason,
		)
	}
	if err := starter.resetAcceptanceTests(
		ctx, run, commitID, request.identity.AttemptID+":reset-acceptance-tests",
	); err != nil {
		return err
	}
	if paused, err := starter.stopAtPauseBoundary(
		ctx, run.ID, "The update onto main is published and ready for another independent review.",
	); err != nil || paused {
		return err
	}
	reviewer, err := starter.executions.GetSession(ctx, remoteReviewerSessionID(run.ID))
	if err != nil {
		return err
	}
	reviewerCheckpoint, err := starter.executions.GetWorkerAttempt(ctx, reviewer.ID)
	if err != nil {
		return err
	}
	reviewVersion, reviewRound, reviewing := workflowAttemptVersionAndTurn(
		reviewer.ID, "review", reviewerCheckpoint.AttemptID,
	)
	if !reviewing || reviewVersion != run.PlanVersion {
		return errors.New("update onto main has no preceding review")
	}
	next, admitted, err := starter.startImplementationReview(
		ctx, run, storedFeature, plan, result.Summary, *result.Publication, reviewRound+1,
	)
	if err != nil {
		return err
	}
	if admitted {
		starter.launchAdmitted(next)
	}
	return nil
}

// baseUpdatePrecedesReview reports whether the lead's last turn is a verified
// update onto main of the current plan, and that the review answered it.
func (starter *RemoteLeadStarter) baseUpdatePrecedesReview(
	ctx context.Context,
	run execution.Run,
	lead execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	review workerhttp.TerminalResult,
) (bool, error) {
	version, _, updating := workflowAttemptVersionAndTurn(lead.ID, "update", checkpoint.AttemptID)
	if !updating || version != run.PlanVersion {
		return false, nil
	}
	verified, err := starter.sessionActivityRecorded(ctx, lead.ID, checkpoint.AttemptID+":publication-verified")
	if err != nil {
		return false, err
	}
	attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
	})
	if err != nil {
		return false, fmt.Errorf("load the lead's update onto main: %w", err)
	}
	if !verified || attempt.Result == nil || attempt.Result.Publication == nil || review.Review == nil ||
		attempt.Result.Publication.CommitID != review.Review.CommitID {
		return false, errors.New("the review does not answer the lead's verified update onto main")
	}
	return true, nil
}

// recoverIdleBaseUpdate continues a reviewed work order whose lead finished an
// update onto main. It reports false once the reviewer has reviewed the
// updated commit, whose review the caller then verifies.
func (starter *RemoteLeadStarter) recoverIdleBaseUpdate(
	ctx context.Context,
	run execution.Run,
	lead execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	reviewer execution.Session,
	reviewerCheckpoint execution.WorkerAttemptCheckpoint,
	directive RecoveryDirective,
) (bool, error) {
	if err := starter.confirmCompletedTurn(ctx, lead, checkpoint); err != nil {
		return false, fmt.Errorf("confirm completed update onto main: %w", err)
	}
	attempt, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
		SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
	})
	if err != nil {
		return false, fmt.Errorf("load completed update onto main: %w", err)
	}
	if attempt.Result == nil {
		return false, errors.New("completed update onto main has no result")
	}
	if attempt.Result.Publication != nil {
		reviewVersion, _, reviewing := workflowAttemptVersionAndTurn(reviewer.ID, "review", reviewerCheckpoint.AttemptID)
		if reviewing && reviewVersion == run.PlanVersion {
			review, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
				SessionID: reviewer.ID, AttemptID: reviewerCheckpoint.AttemptID,
			})
			if err != nil {
				return false, fmt.Errorf("load the review before an update onto main: %w", err)
			}
			if review.Result != nil && review.Result.Review != nil &&
				review.Result.Review.CommitID == attempt.Result.Publication.CommitID {
				return false, nil
			}
		}
	}
	return true, starter.continueOrVerifyLeadTurn(ctx, run, lead, checkpoint, attempt, directive)
}

// continueOrVerifyLeadTurn handles a lead's finished implementation or
// update onto main. The user's Continue answers a lead that asked for help,
// and Continue with guidance goes to a lead whose finished turn could not be
// accepted; otherwise the turn's publication is checked again.
func (starter *RemoteLeadStarter) continueOrVerifyLeadTurn(
	ctx context.Context,
	run execution.Run,
	lead execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	attempt workerhttp.Attempt,
	directive RecoveryDirective,
) error {
	guided := directive.answersBlocker() && (directive.Message != "" || directive.previewCheck != "") &&
		attempt.Result.Disposition != workerhttp.DispositionInputRequired
	if attempt.Result.Disposition == workerhttp.DispositionInputRequired || guided {
		if !guided && !directive.answersBlocker() {
			return starter.handleInputRequired(ctx, remoteLeadRequest{runID: run.ID, identity: workerhttp.MutationIdentity{AttemptReference: attempt.AttemptReference}}, *attempt.Result)
		}
		return starter.answerLeadBlocker(
			ctx, run, lead, checkpoint, attempt, directive, guided, workerhttp.OutputContractImplementationLead,
		)
	}
	request := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent",
		identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
		}},
	}
	if !starter.claim(run.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	if _, updating := baseUpdateTurnNumber(lead.ID, checkpoint.AttemptID); updating {
		go starter.launchBaseUpdateVerification(request, *attempt.Result)
	} else {
		go starter.launchImplementationPublicationVerification(request, *attempt.Result)
	}
	return nil
}

// answerLeadBlocker has the user's Continue answer a lead whose finished turn
// asked for help or could not be accepted: its next turn runs in the same
// conversation, as the next try of that turn.
func (starter *RemoteLeadStarter) answerLeadBlocker(
	ctx context.Context,
	run execution.Run,
	lead execution.Session,
	checkpoint execution.WorkerAttemptCheckpoint,
	attempt workerhttp.Attempt,
	directive RecoveryDirective,
	guided bool,
	contract workerhttp.OutputContract,
) error {
	if !starter.claim(run.ID) {
		return fmt.Errorf("%w: %q", ErrRunAlreadyActive, run.ID)
	}
	answer := remoteLeadRequest{
		runID: run.ID, agentName: "lead agent", allowRecoveryContinuation: true,
		answerBlocker: true, guided: guided, forceFreshRecovery: directive.Action == RecoveryActionReplace,
		recoveryMessage: directive.Message, guidanceFromAssistant: directive.FromAssistant,
		previewCheck: directive.previewCheck, selfCorrection: directive.selfCorrection,
		identity: workerhttp.MutationIdentity{AttemptReference: workerhttp.AttemptReference{
			SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
		}},
		request: workerhttp.PutAttemptRequest{OutputContract: contract},
	}
	go starter.launchBlockerAnswer(answer, attempt)
	return nil
}

// launchBlockerAnswer starts the next turn of an agent the user answered with
// Continue, in the same conversation. The caller has claimed the run.
func (starter *RemoteLeadStarter) launchBlockerAnswer(answer remoteLeadRequest, attempt workerhttp.Attempt) {
	defer starter.release(answer.runID)
	ctx := starter.lifetime
	continued, err := starter.continueFailedAttempt(ctx, answer, attempt)
	if err == nil && !continued {
		err = errors.New("the " + answer.agentName + " could not be continued in a new turn")
	}
	if err != nil {
		starter.requireReview(ctx, answer, err)
	}
}
