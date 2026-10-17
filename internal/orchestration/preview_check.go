package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

// Verified preview setup (ADR-028): a work order that sets the preview up
// merges only once Commitarium's own preview check of its exact commit
// passed. The desktop builds and starts the app's compose file for that
// commit on the host and reports what each container did. A failed check
// goes back to the same lead conversation with that evidence, up to
// maxPreviewFixes times; these fixes are the work order's own steps, not
// autopilot's continues. After that the work order blocks with one plain
// reason.

// RemoteLeadPreviewChecks records preview checks for the desktop to run.
type RemoteLeadPreviewChecks interface {
	Request(ctx context.Context, projectID, featureID, runID, commitID string) (previewcheck.Check, bool, error)
	Latest(ctx context.Context, runID, commitID string) (previewcheck.Check, bool, error)
	Get(ctx context.Context, id string) (previewcheck.Check, error)
	Failures(ctx context.Context, runID string) (int, error)
	Unfinished(ctx context.Context) ([]previewcheck.Check, error)
	Overdue(previewcheck.Check) bool
}

// ErrPreviewCheckRequired refuses to merge a preview-setup work order whose
// exact commit has no passing preview check.
var ErrPreviewCheckRequired = errors.New("the app's preview check must pass for the approved revision")

// maxPreviewFixes bounds the fixes a failed preview check hands back to the
// lead before the work order waits for the user.
const maxPreviewFixes = 3

const (
	previewCheckPendingReason = "Commitarium is checking that the app runs: it builds and starts the app's setup " +
		"for the published commit before merging."
	previewCheckFixingReason = "Commitarium's preview check found that the app does not run. The lead is fixing " +
		"the app's setup with what the check saw."
	// previewCheckNoDesktopReason blocks a check no desktop picked up: the
	// check runs Docker on the user's computer, in the Commitarium app.
	previewCheckNoDesktopReason = "Open Commitarium to finish checking the app's setup. The check runs on this " +
		"computer while the Commitarium app is open."
	previewCheckBlockedPrefix = "The app's setup couldn't be made to run. Here's what's failing: "
	previewCheckBlockedSuffix = ". Continue to let the agent try again; the details are on the work order page."
)

// previewCheckBlockedReason is the one plain reason a work order blocks with
// once its fixes are used up.
func previewCheckBlockedReason(check previewcheck.Check) string {
	plain := "the app did not start"
	if check.Evidence != nil {
		plain = check.Evidence.Plain()
	}
	return previewCheckBlockedPrefix + plain + previewCheckBlockedSuffix
}

// previewCheckGuidance is what the lead hears when a check of its commit
// failed.
func previewCheckGuidance(check previewcheck.Check) string {
	technical := "The check recorded no details."
	plain := "the app did not start"
	if check.Evidence != nil {
		technical, plain = check.Evidence.Technical(), check.Evidence.Plain()
	}
	const budget = 24 * 1024
	if len(technical) > budget {
		technical = strings.ToValidUTF8(technical[:budget], "") + "\n[cut]"
	}
	return "Commitarium's preview check built and started the app's compose setup for your published commit " +
		check.CommitID + " in an isolated copy on the user's computer, and it failed: " + plain + ".\n\n" +
		technical + "\n\nFix the compose file, Dockerfiles, or app so every service builds, starts, and stays up, " +
		"healthchecks pass, and the open target answers HTTP, keeping the preview rules. Do not rebuild or " +
		"emulate the containers by hand to check it, and do not stop because you cannot run Docker: commit the " +
		"fix, publish it to the same pull request, and finish the turn as before. Commitarium checks the new " +
		"commit the same way."
}

// previewCheckApplies reports whether a work order merges only after a
// passing preview check.
func (starter *RemoteLeadStarter) previewCheckApplies(storedFeature feature.Feature) bool {
	return starter.previewChecks != nil && storedFeature.PreviewCheck && storedFeature.Kind != feature.KindSession
}

// previewCheckGate runs at the merge gate, before isolated validation. It
// reports true when it decided the run's next state: waiting for the check,
// a fix by the lead, or a block. It reports false once the commit's check
// passed. fresh says the lead just published commitID, so an earlier failed
// check of the same commit means the fix changed nothing.
func (starter *RemoteLeadStarter) previewCheckGate(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	commitID string,
	fresh bool,
	directive RecoveryDirective,
) (bool, error) {
	if !starter.previewCheckApplies(storedFeature) {
		return false, nil
	}
	check, created, err := starter.previewChecks.Request(ctx, storedFeature.ProjectID, storedFeature.ID, run.ID, commitID)
	if err != nil {
		return true, fmt.Errorf("request the preview check: %w", err)
	}
	switch check.Status {
	case previewcheck.StatusPassed:
		return false, nil
	case previewcheck.StatusFailed:
		if fresh {
			starter.noteBlocker(ctx, run.ID, execution.BlockerCheck, "preview", previewCheckBlockedReason(check), previewCheckBlockedReason(check))
			return true, starter.waitRun(ctx, run.ID, previewCheckBlockedReason(check), execution.RunWaitKindBlocker)
		}
		return true, starter.previewCheckFailed(ctx, run, storedFeature, check, directive)
	}
	if created {
		if _, err := starter.executions.RecordSessionEventWithID(
			ctx, check.ID+":requested", remoteLeadSessionID(run.ID),
			worker.Event{Type: worker.EventActivity, Text: "Preview check " + check.ID +
				" is waiting for the Commitarium app to build and start commit " + commitID + "."},
		); err != nil {
			return true, fmt.Errorf("record the preview check: %w", err)
		}
	}
	return true, starter.waitRun(ctx, run.ID, previewCheckPendingReason, execution.RunWaitKindMergeGate)
}

// previewCheckFailed hands a failed check's evidence to the lead, in the
// same conversation, or blocks the work order once its fixes are used up.
// The user's Continue on that block is the user's choice, so it gives the
// lead one more fix whatever the count.
func (starter *RemoteLeadStarter) previewCheckFailed(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	check previewcheck.Check,
	directive RecoveryDirective,
) error {
	blocked := previewCheckBlockedReason(check)
	failures, err := starter.previewChecks.Failures(ctx, run.ID)
	if err != nil {
		return err
	}
	chosen := directive.answersBlocker() && strings.HasPrefix(directive.blockedReason, previewCheckBlockedPrefix)
	if failures > maxPreviewFixes && !chosen {
		starter.noteBlocker(ctx, run.ID, execution.BlockerCheck, "preview", blocked, blocked)
		return starter.waitRun(ctx, run.ID, blocked, execution.RunWaitKindBlocker)
	}
	if paused, err := starter.stopAtPauseBoundary(ctx, run.ID, previewCheckFixingReason); err != nil || paused {
		return err
	}
	// Only a lead whose last turn published the implementation is continued;
	// any other turn (an update onto main) waits for the user.
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return err
	}
	if _, implementing := implementationTurnNumber(checkpoint.SessionID, checkpoint.AttemptID); !implementing {
		starter.noteBlocker(ctx, run.ID, execution.BlockerCheck, "preview", blocked, blocked)
		return starter.waitRun(ctx, run.ID, blocked, execution.RunWaitKindBlocker)
	}
	clearer, ok := starter.workspaces.(remoteLeadMergeReadyClearer)
	if !ok {
		return errors.New("the checked commit cannot be released for a fix")
	}
	if err := clearer.ClearMergeReady(ctx, storedFeature.ProjectID, storedFeature.ID); err != nil {
		return fmt.Errorf("release the checked commit for a fix: %w", err)
	}
	if storedFeature.State == feature.StateReadyToMerge {
		if _, err := starter.planning.TransitionFeature(
			ctx, storedFeature.ID, feature.StateImplementing,
			workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
			check.ID+":preview-fix",
		); err != nil {
			return fmt.Errorf("leave the merge gate for a preview fix: %w", err)
		}
	}
	text := fmt.Sprintf("Preview check %s of commit %s failed: %s. The lead is fixing it (fix %d of %d).",
		check.ID, check.CommitID, strings.TrimPrefix(strings.TrimSuffix(blocked, previewCheckBlockedSuffix), previewCheckBlockedPrefix),
		min(failures, maxPreviewFixes), maxPreviewFixes)
	if chosen {
		text = fmt.Sprintf("Preview check %s of commit %s failed. The user continued, so the lead tries again.",
			check.ID, check.CommitID)
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, check.ID+":fix-requested", remoteLeadSessionID(run.ID),
		worker.Event{Type: worker.EventActivity, Text: text},
	); err != nil {
		return fmt.Errorf("record the preview fix: %w", err)
	}
	current, err := starter.executions.GetRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if current.Status == execution.RunStatusWaitingForUser {
		if current, err = starter.executions.TransitionRun(
			ctx, current.ID, current.Status, execution.RunStatusRunning, previewCheckFixingReason,
		); err != nil {
			return err
		}
	}
	implementing, err := starter.features.GetByID(ctx, storedFeature.ID)
	if err != nil {
		return err
	}
	fix := RecoveryDirective{
		Action: RecoveryActionContinue, previewCheck: previewCheckGuidance(check), blockedReason: directive.blockedReason,
	}
	if chosen {
		fix.Message, fix.FromAssistant = directive.Message, directive.FromAssistant
	}
	if err := starter.recoverWithDirective(ctx, current, implementing, project.RecoveryPolicyAutomatic, fix); err != nil {
		if errors.Is(err, ErrRunAlreadyActive) {
			return err
		}
		starter.reportError(fmt.Errorf("hand the preview check of run %q to its lead: %w", run.ID, err))
		starter.noteBlocker(ctx, run.ID, execution.BlockerCheck, "preview", blocked, blocked)
		return starter.waitRun(ctx, run.ID, blocked, execution.RunWaitKindBlocker)
	}
	return nil
}

// PreviewCheckCompleted moves a run on once the desktop finished its preview
// check: a pass goes on to validation and the merge, a failure to the lead.
// A check of a commit the run no longer waits on changes nothing.
func (starter *RemoteLeadStarter) PreviewCheckCompleted(ctx context.Context, checkID string) error {
	if starter.previewChecks == nil {
		return nil
	}
	check, err := starter.previewChecks.Get(ctx, checkID)
	if err != nil {
		return err
	}
	if check.Status != previewcheck.StatusPassed && check.Status != previewcheck.StatusFailed {
		return nil
	}
	run, err := starter.executions.GetRun(ctx, check.RunID)
	if err != nil {
		return err
	}
	if !starter.waitsOnPreviewCheck(run) {
		return nil
	}
	return starter.continueAfterPreviewCheck(ctx, run, check)
}

func (starter *RemoteLeadStarter) continueAfterPreviewCheck(
	ctx context.Context, run execution.Run, check previewcheck.Check,
) error {
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return err
	}
	if storedFeature.State != feature.StateReadyToMerge {
		return nil
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return err
	}
	if prepared.ApprovedCommitID != check.CommitID || prepared.MergeCommitID != "" {
		return nil
	}
	result := "passed: the app builds, starts, and answers."
	if check.Status == previewcheck.StatusFailed {
		result = "failed."
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, check.ID+":completed", remoteLeadSessionID(run.ID),
		worker.Event{Type: worker.EventActivity, Text: "Preview check " + check.ID + " of commit " + check.CommitID + " " + result},
	); err != nil {
		return fmt.Errorf("record the preview check result: %w", err)
	}
	// The gates continue as recovery takes them from the merge gate: the
	// preview check again (now finished), validation, and the merge.
	return starter.recoverIdleReadyToMergeRun(ctx, run, RecoveryDirective{})
}

// waitsOnPreviewCheck reports whether a run waits for its preview check:
// at the merge gate, or blocked because no desktop ran the check.
func (starter *RemoteLeadStarter) waitsOnPreviewCheck(run execution.Run) bool {
	if run.Status != execution.RunStatusWaitingForUser || run.Paused {
		return false
	}
	return (run.WaitKind == execution.RunWaitKindMergeGate && run.Reason == previewCheckPendingReason) ||
		(run.WaitKind == execution.RunWaitKindBlocker && run.Reason == previewCheckNoDesktopReason)
}

// previewCheckWatchEvery is how often overdue preview checks are looked for.
const previewCheckWatchEvery = 30 * time.Second

// BlockOverduePreviewChecks blocks the runs whose preview check no desktop
// picked up or finished in time, with a reason that says to open the app.
// The check stays, so a desktop that opens later still runs it; the user's
// Continue asks for a new one. It returns how many runs it blocked.
func (starter *RemoteLeadStarter) BlockOverduePreviewChecks(ctx context.Context) (int, error) {
	if starter.previewChecks == nil {
		return 0, nil
	}
	checks, err := starter.previewChecks.Unfinished(ctx)
	if err != nil {
		return 0, err
	}
	blocked := 0
	var errs []error
	for _, check := range checks {
		if !starter.previewChecks.Overdue(check) {
			continue
		}
		run, err := starter.executions.GetRun(ctx, check.RunID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if run.Status != execution.RunStatusWaitingForUser || run.Paused ||
			run.WaitKind != execution.RunWaitKindMergeGate || run.Reason != previewCheckPendingReason {
			continue
		}
		if latest, found, err := starter.previewChecks.Latest(ctx, run.ID, check.CommitID); err != nil || !found || latest.ID != check.ID {
			continue
		}
		starter.noteBlocker(ctx, run.ID, execution.BlockerEnvironment, "preview",
			previewCheckNoDesktopReason, previewCheckNoDesktopReason)
		if err := starter.waitRun(ctx, run.ID, previewCheckNoDesktopReason, execution.RunWaitKindBlocker); err != nil {
			errs = append(errs, err)
			continue
		}
		blocked++
	}
	return blocked, errors.Join(errs...)
}

// RunPreviewCheckWatcher blocks overdue preview checks until ctx ends.
func (starter *RemoteLeadStarter) RunPreviewCheckWatcher(ctx context.Context) {
	ticker := time.NewTicker(previewCheckWatchEvery)
	defer ticker.Stop()
	for {
		if _, err := starter.BlockOverduePreviewChecks(ctx); err != nil {
			starter.reportError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
