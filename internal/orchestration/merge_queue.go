package orchestration

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// The merge queue (ADR-036): work orders of a project run side by side, and
// those that merge on their own merge one at a time, in the order their gates
// passed. Only the first in line merges or brings main in; the others wait
// their turn instead of racing to update. A work order that blocks, pauses,
// or waits for the user leaves the line and joins at the back when it is
// ready again. A merge the user approves does not wait in line.

type mergeQueue interface {
	JoinMergeQueue(context.Context, string, string) error
	LeaveMergeQueue(context.Context, string) (string, bool, error)
	MergeQueue(context.Context, string) ([]string, error)
	MergeQueueProjects(context.Context) ([]string, error)
}

// mergeQueueCheckEvery is how often every line is looked at again, for a
// run that left it without passing through the coordinator (a closed work
// order) and after a restart.
const mergeQueueCheckEvery = 15 * time.Second

// mergePlace puts the run in its project's line and reports its place: 1 is
// its turn. Without a queue every run's turn is now.
func (starter *RemoteLeadStarter) mergePlace(ctx context.Context, projectID, runID string) (int, error) {
	queue, ok := starter.executions.(mergeQueue)
	if !ok {
		return 1, nil
	}
	starter.queueMu.Lock()
	defer starter.queueMu.Unlock()
	if err := queue.JoinMergeQueue(ctx, projectID, runID); err != nil {
		return 0, err
	}
	line, err := starter.mergeLine(ctx, queue, projectID, runID)
	if err != nil {
		return 0, err
	}
	return slices.Index(line, runID) + 1, nil
}

// mergeLine lists a project's line, taking out the runs no longer in it: one
// that ended, is paused, or waits for anything but its turn. Only the first
// in line may be working, on its update onto main, besides joining: the run
// whose gates just passed. The caller holds queueMu.
func (starter *RemoteLeadStarter) mergeLine(
	ctx context.Context, queue mergeQueue, projectID string, joining string,
) ([]string, error) {
	queued, err := queue.MergeQueue(ctx, projectID)
	if err != nil {
		return nil, err
	}
	line := make([]string, 0, len(queued))
	for _, runID := range queued {
		run, err := starter.executions.GetRun(ctx, runID)
		if err != nil && !errors.Is(err, execution.ErrNotFound) {
			return nil, err
		}
		waiting := run.Status == execution.RunStatusWaitingForUser && run.WaitKind == execution.RunWaitKindMergeGate
		working := run.Status == execution.RunStatusRunning && len(line) == 0
		if err == nil && !run.Paused && (waiting || working || runID == joining) {
			line = append(line, runID)
			continue
		}
		if _, _, err := queue.LeaveMergeQueue(ctx, runID); err != nil {
			return nil, err
		}
	}
	return line, nil
}

// leaveMergeQueue takes a run out of line, and lets the next one take its
// turn.
func (starter *RemoteLeadStarter) leaveMergeQueue(ctx context.Context, runID string) {
	queue, ok := starter.executions.(mergeQueue)
	if !ok {
		return
	}
	starter.queueMu.Lock()
	projectID, left, err := queue.LeaveMergeQueue(ctx, runID)
	starter.queueMu.Unlock()
	if err != nil {
		starter.reportError(err)
	}
	if left {
		go starter.advanceMergeQueue(starter.lifetime, projectID)
	}
}

// advanceMergeQueue tells each run in a project's line its place, and starts
// the merge of the first when it waits for its turn.
func (starter *RemoteLeadStarter) advanceMergeQueue(ctx context.Context, projectID string) {
	queue, ok := starter.executions.(mergeQueue)
	if !ok {
		return
	}
	starter.queueMu.Lock()
	line, err := starter.mergeLine(ctx, queue, projectID, "")
	if err == nil {
		for index, runID := range line[min(1, len(line)):] {
			run, getErr := starter.executions.GetRun(ctx, runID)
			if getErr == nil && execution.InMergeQueue(run.Reason) {
				err = errors.Join(err, starter.waitRun(
					ctx, runID, execution.MergeQueueReason(index+2), execution.RunWaitKindMergeGate,
				))
			}
		}
	}
	starter.queueMu.Unlock()
	if err != nil {
		starter.reportError(fmt.Errorf("advance the merge queue of project %q: %w", projectID, err))
	}
	if len(line) > 0 {
		starter.mergeInTurn(ctx, line[0])
	}
}

// mergeInTurn merges the first run in line when it waits for its turn and
// no other coordinator operation holds it.
func (starter *RemoteLeadStarter) mergeInTurn(ctx context.Context, runID string) {
	if !starter.claim(runID) {
		return
	}
	run, err := starter.executions.GetRun(ctx, runID)
	starter.release(runID)
	if err != nil || run.Paused || run.Status != execution.RunStatusWaitingForUser ||
		run.WaitKind != execution.RunWaitKindMergeGate || !execution.InMergeQueue(run.Reason) {
		return
	}
	if err := starter.mergeAfterGates(ctx, run, false); err != nil && !errors.Is(err, ErrRunAlreadyActive) {
		starter.reportError(fmt.Errorf("merge run %q in its turn: %w", runID, err))
	}
}

// RunMergeQueue looks at every project's line now and then until ctx ends,
// so a line moves on after a restart and after a run left it unseen.
func (starter *RemoteLeadStarter) RunMergeQueue(ctx context.Context) {
	ticker := time.NewTicker(mergeQueueCheckEvery)
	defer ticker.Stop()
	for {
		starter.AdvanceMergeQueues(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// AdvanceMergeQueues advances every project's line.
func (starter *RemoteLeadStarter) AdvanceMergeQueues(ctx context.Context) {
	queue, ok := starter.executions.(mergeQueue)
	if !ok {
		return
	}
	projects, err := queue.MergeQueueProjects(ctx)
	if err != nil {
		starter.reportError(err)
		return
	}
	for _, projectID := range projects {
		starter.advanceMergeQueue(ctx, projectID)
	}
}

// Failed updates (ADR-036 amends ADR-025): being overtaken while in line is
// normal, so only an update that failed counts toward the limit on automatic
// updates. One failed when its turn blocked before it reached the merge gate,
// when the reviewer asked for changes to it, or when Forgejo still refused it
// with main where the lead brought it in from.

func baseUpdateFailedID(attemptID string) string { return attemptID + ":base-update-failed" }

func baseUpdateOntoID(attemptID, mainHead string) string {
	return attemptID + ":base-update-onto:" + mainHead
}

// noteFailedBaseUpdate records that the update onto main in attemptID failed.
func (starter *RemoteLeadStarter) noteFailedBaseUpdate(ctx context.Context, sessionID, attemptID, why string) {
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, baseUpdateFailedID(attemptID), sessionID,
		worker.Event{Type: worker.EventActivity, Text: "The update onto main did not hold up: " + why},
	); err != nil {
		starter.reportError(fmt.Errorf("record failed update onto main: %w", err))
	}
}

// baseUpdatesFailed counts the failed updates onto main of the run's current
// plan version.
func (starter *RemoteLeadStarter) baseUpdatesFailed(ctx context.Context, run execution.Run) (int, error) {
	leadID := remoteLeadSessionID(run.ID)
	events, err := starter.executions.EventsForSession(ctx, leadID)
	if err != nil {
		return 0, err
	}
	failed := 0
	for _, event := range events {
		attemptID, found := strings.CutSuffix(event.ID, ":base-update-failed")
		if !found {
			continue
		}
		if version, _, ok := workflowAttemptVersionAndTurn(leadID, "update", attemptID); ok && version == run.PlanVersion {
			failed++
		}
	}
	return failed, nil
}

// noteUpdateRefusedAgain marks the latest verified update of the plan
// version failed when Forgejo refuses it while main is still where the lead
// brought it in from. Main having moved since means it was overtaken.
func (starter *RemoteLeadStarter) noteUpdateRefusedAgain(ctx context.Context, run execution.Run, mainHead string) {
	leadID := remoteLeadSessionID(run.ID)
	events, err := starter.executions.EventsForSession(ctx, leadID)
	if err != nil || mainHead == "" {
		return
	}
	latest, latestTurn := "", 0
	for _, event := range events {
		attemptID, found := strings.CutSuffix(event.ID, ":publication-verified")
		if !found {
			continue
		}
		if version, turn, ok := workflowAttemptVersionAndTurn(leadID, "update", attemptID); ok &&
			version == run.PlanVersion && turn > latestTurn {
			latest, latestTurn = attemptID, turn
		}
	}
	if latest == "" || !slices.ContainsFunc(events, func(event execution.Event) bool {
		return event.ID == baseUpdateOntoID(latest, mainHead)
	}) {
		return
	}
	starter.noteFailedBaseUpdate(ctx, leadID, latest,
		"Forgejo still refuses to merge it, with main where the lead brought it in from.")
}

// noteBlockedBaseUpdate marks the lead's update onto main failed when the
// run blocks on it before it reached the merge gate.
func (starter *RemoteLeadStarter) noteBlockedBaseUpdate(ctx context.Context, run execution.Run, reason string) {
	leadID := remoteLeadSessionID(run.ID)
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, leadID)
	if err != nil {
		return
	}
	if version, _, updating := workflowAttemptVersionAndTurn(leadID, "update", checkpoint.AttemptID); !updating ||
		version != run.PlanVersion {
		return
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil || storedFeature.State == feature.StateReadyToMerge {
		return
	}
	starter.noteFailedBaseUpdate(ctx, leadID, checkpoint.AttemptID, firstSentence(reason))
}

func firstSentence(text string) string {
	if before, _, found := strings.Cut(text, ". "); found {
		return before + "."
	}
	return text
}
