package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type validationResultPublisher interface {
	PublishValidationResult(context.Context, string, string, workspace.ValidationPublicationSpec) (bool, error)
}

type validationEventRecorder interface {
	RecordSessionEventWithID(context.Context, string, string, worker.Event) (execution.Event, error)
}

type validationConfigRequest struct {
	Commands []string `json:"commands"`
}
type validationCompletionRequest struct {
	Results []validation.CommandResult `json:"results"`
	Error   string                     `json:"error"`
}

func (api *API) getValidationConfigHandler(w http.ResponseWriter, r *http.Request) {
	config, err := api.validations.GetConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, config, "validation config")
}

func (api *API) configureValidationHandler(w http.ResponseWriter, r *http.Request) {
	var body validationConfigRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	config, err := api.validations.Configure(r.Context(), r.PathValue("id"), body.Commands)
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, config, "validation config")
}

func (api *API) listValidationJobsHandler(w http.ResponseWriter, r *http.Request) {
	jobs, err := api.validations.JobsForRun(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs}, "validation jobs")
}

func (api *API) ensureValidationJobHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	ensurer, ok := api.realWorkflow.(validationJobEnsurer)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "validation_unavailable", "isolated validation is unavailable")
		return
	}
	job, created, err := ensurer.EnsureValidationJob(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, job, "validation job")
}

func (api *API) getValidationJobHandler(w http.ResponseWriter, r *http.Request) {
	job, err := api.validations.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job, "validation job")
}

func (api *API) claimValidationJobHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	job, _, err := api.validations.Claim(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job, "claimed validation job")
}

func (api *API) retryValidationJobHandler(w http.ResponseWriter, r *http.Request) {
	if !emptyBody(w, r) {
		return
	}
	job, created, err := api.validations.Retry(r.Context(), r.PathValue("id"))
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, job, "validation retry job")
}

func (api *API) completeValidationJobHandler(w http.ResponseWriter, r *http.Request) {
	var body validationCompletionRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	job, _, err := api.validations.Complete(r.Context(), r.PathValue("id"), body.Results, body.Error)
	if err != nil {
		api.writeValidationError(w, err)
		return
	}
	statusSummary := validationSummary(job)
	if publisher, ok := api.workspaces.(validationResultPublisher); ok {
		prepared, workspaceErr := api.workspaces.Get(r.Context(), job.ProjectID, job.FeatureID)
		if workspaceErr != nil {
			writeError(w, http.StatusServiceUnavailable, "validation_publication_unavailable", "validation completed but its Forgejo result could not be published yet; retry this operation")
			return
		}
		if _, publishErr := publisher.PublishValidationResult(r.Context(), job.ProjectID, job.FeatureID, workspace.ValidationPublicationSpec{
			JobID: job.ID, PullRequestNumber: prepared.PullRequestNumber, CommitID: job.CommitID,
			Status: string(job.Status), Summary: statusSummary,
		}); publishErr != nil {
			log.Printf("publish validation job %q to Forgejo: %v", job.ID, publishErr)
			writeError(w, http.StatusServiceUnavailable, "validation_publication_unavailable", "validation completed but its Forgejo result could not be published yet; retry this operation")
			return
		}
	}
	if recorder, ok := api.execution.(validationEventRecorder); ok {
		sessions, listErr := api.execution.SessionsForRun(r.Context(), job.RunID)
		if listErr == nil {
			for _, session := range sessions {
				if session.Role == worker.RoleLead {
					_, _ = recorder.RecordSessionEventWithID(r.Context(), job.ID+":completed", session.ID,
						worker.Event{Type: worker.EventActivity, Text: statusSummary})
					break
				}
			}
		}
	}
	if job.Status == validation.StatusPassed && api.realWorkflow != nil {
		run, getErr := api.execution.GetRun(r.Context(), job.RunID)
		if getErr == nil && run.MergePolicy == project.MergePolicyAutoAfterGates {
			if _, _, mergeErr := api.realWorkflow.Merge(r.Context(), run.ID, run.ID+":automatic-validation-merge"); mergeErr != nil {
				log.Printf("automatic merge after validation job %q: %v", job.ID, mergeErr)
			}
		}
	}
	writeJSON(w, http.StatusOK, job, "completed validation job")
}

func validationSummary(job validation.Job) string {
	if job.Status == validation.StatusPassed {
		return fmt.Sprintf("Isolated validation passed %d configured command(s) for approved commit %s.", len(job.Results), job.CommitID)
	}
	if len(job.Results) > 0 {
		last := job.Results[len(job.Results)-1]
		return fmt.Sprintf("Isolated validation failed command %q with exit code %d for approved commit %s.", last.Command, last.ExitCode, job.CommitID)
	}
	return "Isolated validation could not run for approved commit " + job.CommitID + "."
}

func (api *API) writeValidationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, validation.ErrNotFound):
		writeError(w, http.StatusNotFound, "validation_not_found", "validation configuration or job not found")
	case errors.Is(err, validation.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_validation", "validation commands or results are invalid")
	case errors.Is(err, validation.ErrConflict):
		writeError(w, http.StatusConflict, "validation_conflict", "validation job is not in the required state")
	default:
		log.Printf("validation API: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
