package orchestration

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// The coordinator's own run of a work order's independent acceptance tests:
// the reviewer reports its run, and Commitarium runs the same tests itself, by
// exit code, so the result does not rest on the reviewer's word. It informs;
// it does not gate the merge.

type remoteLeadAcceptanceJobs interface {
	EnsureAcceptanceJob(
		ctx context.Context,
		projectID, featureID, runID, workspaceID, commitID, testCommitID string,
		testIDs []string,
		commands []string,
	) (validation.Job, bool, error)
}

// runAcceptanceTestsItself queues the coordinator's own run of the acceptance
// tests against an implementation commit under review. Tests without a command
// are left to the reviewer. Failures are reported, never fatal.
func (starter *RemoteLeadStarter) runAcceptanceTestsItself(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	commitID string,
) {
	if !run.IndependentAcceptanceTests || starter.artifacts == nil || starter.validation == nil {
		return
	}
	jobs, queues := starter.validation.(remoteLeadAcceptanceJobs)
	if !queues {
		return
	}
	artifact, err := starter.artifacts.GetFeatureArtifact(ctx, storedFeature.ID, featureartifact.KindAcceptanceTests)
	if err != nil {
		return
	}
	var tests featureartifact.AcceptanceTests
	if err := json.Unmarshal([]byte(artifact.Document), &tests); err != nil || tests.PlanVersion != run.PlanVersion {
		return
	}
	ids, commands := make([]string, 0, len(tests.Tests)), make([]string, 0, len(tests.Tests))
	for _, test := range tests.Tests {
		if test.Command != "" {
			ids, commands = append(ids, test.ID), append(commands, test.Command)
		}
	}
	if len(commands) == 0 {
		return
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		starter.reportError(fmt.Errorf("run acceptance tests for %q: %w", storedFeature.ID, err))
		return
	}
	job, created, err := jobs.EnsureAcceptanceJob(
		ctx, storedFeature.ProjectID, storedFeature.ID, run.ID, prepared.ID, commitID,
		tests.TestCommitID, ids, commands,
	)
	if err != nil {
		starter.reportError(fmt.Errorf("queue acceptance tests for %q: %w", storedFeature.ID, err))
		return
	}
	if created {
		_, _ = starter.executions.RecordSessionEventWithID(
			ctx, job.ID+":queued", remoteLeadSessionID(run.ID),
			worker.Event{Type: worker.EventActivity, Text: fmt.Sprintf(
				"Commitarium is running the %d acceptance tests itself against commit %s.", len(commands), commitID,
			)},
		)
	}
}
