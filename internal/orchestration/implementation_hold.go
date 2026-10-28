package orchestration

import (
	"context"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// Early planning (ADR-037): a work order that builds on work not merged yet
// plans while that work is built. With its plan published it waits at the
// phase checkpoint, with a reason that says what it waits for, until the
// assistant's records say it may implement. Its first implementation turn
// then brings main in and checks the plan against the merged code before it
// implements. The wait is the run's own durable state, so it survives a
// restart; the assistant's watcher releases it.

// RemoteLeadImplementationHolds says whether a work order that planned early
// still waits before it implements, and why in plain words.
type RemoteLeadImplementationHolds interface {
	ImplementationHold(ctx context.Context, projectID, featureID string) (string, bool)
}

// implementationHeldActivity records on the lead's conversation that the run
// waited with its plan ready, so its implementation brings main in first.
const implementationHeldActivity = "The plan is ready early. Implementation waits until the work it builds on " +
	"has merged; then the lead first brings main in and checks the plan still fits."

func implementationHeldID(runID string) string { return runID + ":implementation-held" }

// holdImplementation keeps a run whose plan is published from implementing
// while the work it builds on has not merged, or no slot is free. It reports
// whether the run waits.
func (starter *RemoteLeadStarter) holdImplementation(
	ctx context.Context, run execution.Run, storedFeature feature.Feature,
) (bool, error) {
	if starter.holds == nil {
		return false, nil
	}
	reason, held := starter.holds.ImplementationHold(ctx, storedFeature.ProjectID, storedFeature.ID)
	if !held {
		return false, nil
	}
	if _, err := starter.executions.RecordSessionEventWithID(
		ctx, implementationHeldID(run.ID), remoteLeadSessionID(run.ID),
		worker.Event{Type: worker.EventActivity, Text: implementationHeldActivity},
	); err != nil {
		return true, err
	}
	return true, starter.waitRun(ctx, run.ID, reason, execution.RunWaitKindPhaseCheckpoint)
}

// ReleaseImplementation looks again at a run that waits to implement: it
// implements once nothing holds it, and its reason follows what it waits
// for. A run that does not wait to implement is left as it is.
func (starter *RemoteLeadStarter) ReleaseImplementation(ctx context.Context, runID string) error {
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Paused || run.Status != execution.RunStatusWaitingForUser ||
		run.WaitKind != execution.RunWaitKindPhaseCheckpoint || !execution.HeldBeforeImplementation(run.Reason) {
		return nil
	}
	return starter.advanceWaitingRun(ctx, runID)
}

// wasHeld reports whether the run waited with its plan ready before it
// implemented.
func (starter *RemoteLeadStarter) wasHeld(ctx context.Context, runID string) bool {
	events, err := starter.executions.EventsForSession(ctx, remoteLeadSessionID(runID))
	if err != nil {
		return false
	}
	for _, event := range events {
		if event.ID == implementationHeldID(runID) {
			return true
		}
	}
	return false
}

// heldImplementationInstructions open the first implementation turn of a
// run that planned early: main now has the work it builds on.
const heldImplementationInstructions = "You planned this work order early, while work it builds on was still " +
	"being built; that work has merged since. Once you have checked Git HEAD and status against the facts " +
	"below, and before you implement, bring main in: fetch the 'commitarium' " +
	"remote and merge (never rebase) its default branch into the feature branch, resolving any conflicts " +
	"keeping both sides' intent. Then check that the agreed plan still fits the merged code. If it does not, " +
	"adjust the plan as little as needed and implement the adjusted plan, and in your publication comment add " +
	"a '## Plan update' section that says exactly what changed and why; the reviewer checks only that change. " +
	"If it fits, say so in one line in that comment.\n\n"

// heldReviewInstructions tell the code reviewer of a run that planned early
// that main was merged in before implementation.
const heldReviewInstructions = "\n\nThis work order planned early and brought main in before implementing, " +
	"because work it builds on merged after its plan was agreed. Changes that came from main are not the " +
	"lead's to justify; review the lead's own changes. If the lead's publication comment has a '## Plan " +
	"update' section, check that change against the merged code and the agreed plan; the rest of the plan " +
	"stands as agreed."
