package httpapi

import (
	"context"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

// The coordinator's own runs of the reviewer's acceptance tests. The desktop
// runs them in isolation like validation; their results are recorded next to
// the reviewer's on the work order's acceptance tests.

type pendingAcceptanceJobs interface {
	PendingAcceptanceJobs(context.Context) ([]validation.Job, error)
}

type coordinatorAcceptanceRecorder interface {
	RecordCoordinatorAcceptanceRun(context.Context, string, string, map[string]bool, string, workflow.Actor, string) (workflow.FeatureArtifact, error)
}

// listPendingAcceptanceJobsHandler lists the acceptance runs waiting for the
// desktop to run them.
func (api *API) listPendingAcceptanceJobsHandler(w http.ResponseWriter, r *http.Request) {
	pending, ok := api.validations.(pendingAcceptanceJobs)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": []validation.Job{}}, "pending acceptance jobs")
		return
	}
	jobs, err := pending.PendingAcceptanceJobs(r.Context())
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs}, "pending acceptance jobs")
}

// recordAcceptanceRun records each test's result from the coordinator's run on
// the work order's acceptance tests, by the exit code of its command. A test
// without a result did not run and counts as failed, unless the run could not
// run the tests at all: then the run's error is recorded and no verdict.
func (api *API) recordAcceptanceRun(ctx context.Context, job validation.Job) {
	recorder, ok := api.artifacts.(coordinatorAcceptanceRecorder)
	if !ok {
		return
	}
	passed := make(map[string]bool, len(job.Labels))
	for index, testID := range job.Labels {
		passed[testID] = index < len(job.Results) && job.Results[index].ExitCode == 0
	}
	if _, err := recorder.RecordCoordinatorAcceptanceRun(
		ctx, job.FeatureID, job.CommitID, passed, job.Error,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: "commitarium"}, job.ID+":results",
	); err != nil {
		log.Printf("record acceptance run %q: %v", job.ID, err)
	}
}
