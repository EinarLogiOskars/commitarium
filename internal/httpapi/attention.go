package httpapi

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
)

// attentionItem is the coordinator-owned projection used by the global review
// inbox. It deliberately carries navigation identifiers instead of URLs so the
// desktop app remains responsible for its own routing.
type attentionItem struct {
	ID                   string    `json:"id"`
	Kind                 string    `json:"kind"`
	Severity             string    `json:"severity"`
	Actionable           bool      `json:"actionable"`
	ProjectID            string    `json:"project_id"`
	ProjectName          string    `json:"project_name"`
	FeatureID            string    `json:"feature_id"`
	FeatureTitle         string    `json:"feature_title"`
	RunID                string    `json:"run_id,omitempty"`
	EnvironmentRequestID string    `json:"environment_request_id,omitempty"`
	ValidationJobID      string    `json:"validation_job_id,omitempty"`
	Title                string    `json:"title"`
	Detail               string    `json:"detail"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type attentionResponse struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Items       []attentionItem    `json:"items"`
	Running     []runningWorkOrder `json:"running"`
}

type runningWorkOrder struct {
	ProjectID    string    `json:"project_id"`
	ProjectName  string    `json:"project_name"`
	FeatureID    string    `json:"feature_id"`
	FeatureTitle string    `json:"feature_title"`
	RunID        string    `json:"run_id"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (api *API) listAttentionHandler(w http.ResponseWriter, r *http.Request) {
	items, running, err := api.collectAttention(r.Context())
	if err != nil {
		log.Printf("attention API: %v", err)
		writeError(w, http.StatusInternalServerError, "attention_unavailable", "the attention inbox could not be assembled")
		return
	}
	writeJSON(w, http.StatusOK, attentionResponse{
		GeneratedAt: time.Now().UTC(),
		Items:       items,
		Running:     running,
	}, "attention inbox")
}

func (api *API) collectAttention(ctx context.Context) ([]attentionItem, []runningWorkOrder, error) {
	projects, err := api.projects.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	items := make([]attentionItem, 0)
	running := make([]runningWorkOrder, 0)
	for _, currentProject := range projects {
		features, err := api.features.List(ctx, currentProject.ID)
		if err != nil {
			return nil, nil, err
		}
		environmentsByRun, err := api.activeEnvironmentsByRun(ctx, currentProject.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, currentFeature := range features {
			featureItems, runningWork, err := api.attentionForFeature(ctx, currentProject, currentFeature, environmentsByRun)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, featureItems...)
			if runningWork != nil {
				running = append(running, *runningWork)
			}
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Actionable != items[j].Actionable {
			return items[i].Actionable
		}
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].ID < items[j].ID
	})
	sort.SliceStable(running, func(i, j int) bool {
		if !running[i].UpdatedAt.Equal(running[j].UpdatedAt) {
			return running[i].UpdatedAt.After(running[j].UpdatedAt)
		}
		return running[i].RunID < running[j].RunID
	})
	return capCompletedItems(items, 25), running, nil
}

func (api *API) activeEnvironmentsByRun(ctx context.Context, projectID string) (map[string]projectenvironment.Request, error) {
	active := make(map[string]projectenvironment.Request)
	if api.environments == nil {
		return active, nil
	}
	requests, err := api.environments.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, request := range requests {
		if !environmentNeedsAttention(request.Status) {
			continue
		}
		prior, exists := active[request.RunID]
		if !exists || request.UpdatedAt.After(prior.UpdatedAt) {
			active[request.RunID] = request
		}
	}
	return active, nil
}

func (api *API) attentionForFeature(
	ctx context.Context,
	currentProject project.Project,
	currentFeature feature.Feature,
	environmentsByRun map[string]projectenvironment.Request,
) ([]attentionItem, *runningWorkOrder, error) {
	if currentFeature.State == feature.StateCompleted && currentFeature.MergePolicy == project.MergePolicyAutoAfterGates {
		return []attentionItem{{
			ID: "auto_merge_completed:" + currentFeature.ID, Kind: "auto_merge_completed",
			Severity: "info", Actionable: false, ProjectID: currentProject.ID,
			ProjectName: currentProject.Name, FeatureID: currentFeature.ID, FeatureTitle: currentFeature.Title,
			Title: "Work order merged", Detail: "Automatic merge completed after all gates passed.",
			UpdatedAt: currentFeature.UpdatedAt,
		}}, nil, nil
	}
	if currentFeature.State.IsTerminal() {
		return nil, nil, nil
	}

	runs, err := api.execution.RunsForFeature(ctx, currentFeature.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(runs) == 0 {
		return nil, nil, nil
	}
	currentRun := newestRun(runs)
	base := attentionItem{
		ProjectID: currentProject.ID, ProjectName: currentProject.Name,
		FeatureID: currentFeature.ID, FeatureTitle: currentFeature.Title,
		RunID: currentRun.ID, Actionable: true, UpdatedAt: currentRun.UpdatedAt,
	}
	if currentRun.Status == execution.RunStatusRunning {
		return nil, &runningWorkOrder{
			ProjectID: currentProject.ID, ProjectName: currentProject.Name,
			FeatureID: currentFeature.ID, FeatureTitle: currentFeature.Title,
			RunID: currentRun.ID, UpdatedAt: currentRun.UpdatedAt,
		}, nil
	}

	if request, ok := environmentsByRun[currentRun.ID]; ok {
		return []attentionItem{environmentAttention(base, request)}, nil, nil
	}
	if currentRun.Status == execution.RunStatusFailed {
		base.ID = "run_failed:" + currentRun.ID
		base.Kind = "run_failed"
		base.Severity = "error"
		base.Title = "Run failed"
		base.Detail = detailOr(currentRun.Reason, "The run failed and needs inspection.")
		return []attentionItem{base}, nil, nil
	}
	if currentRun.Status != execution.RunStatusWaitingForUser {
		return nil, nil, nil
	}

	if currentRun.WaitKind == execution.RunWaitKindMergeGate && api.validations != nil {
		jobs, err := api.validations.JobsForRun(ctx, currentRun.ID)
		if err != nil {
			return nil, nil, err
		}
		if item, ok := validationAttention(base, jobs); ok {
			return []attentionItem{item}, nil, nil
		}
	}
	return []attentionItem{runWaitAttention(base, currentRun)}, nil, nil
}

func newestRun(runs []execution.Run) execution.Run {
	newest := runs[0]
	for _, candidate := range runs[1:] {
		if candidate.StartedAt.After(newest.StartedAt) ||
			(candidate.StartedAt.Equal(newest.StartedAt) && candidate.ID < newest.ID) {
			newest = candidate
		}
	}
	return newest
}

func environmentNeedsAttention(status projectenvironment.Status) bool {
	return status == projectenvironment.StatusRequested || status == projectenvironment.StatusApproved ||
		status == projectenvironment.StatusProvisioning || status == projectenvironment.StatusFailed
}

func environmentAttention(base attentionItem, request projectenvironment.Request) attentionItem {
	base.ID = "environment:" + request.ID + ":" + string(request.Status) + ":" + request.UpdatedAt.Format(time.RFC3339Nano)
	base.EnvironmentRequestID = request.ID
	base.Kind = "environment_approval"
	base.Severity = "warning"
	base.Title = "Environment approval needed"
	base.Detail = request.Reason
	base.UpdatedAt = request.UpdatedAt
	switch request.Status {
	case projectenvironment.StatusApproved:
		base.Kind = "environment_provisioning"
		base.Title = "Environment change ready to apply"
	case projectenvironment.StatusProvisioning:
		base.Kind = "environment_provisioning"
		base.Actionable = false
		base.Title = "Environment change in progress"
	case projectenvironment.StatusFailed:
		base.Kind = "environment_failed"
		base.Severity = "error"
		base.Title = "Environment change failed"
		base.Detail = detailOr(request.Error, request.Reason)
	}
	return base
}

func validationAttention(base attentionItem, jobs []validation.Job) (attentionItem, bool) {
	if len(jobs) == 0 {
		return attentionItem{}, false
	}
	job := jobs[0]
	for _, candidate := range jobs[1:] {
		if candidate.CreatedAt.After(job.CreatedAt) ||
			(candidate.CreatedAt.Equal(job.CreatedAt) && candidate.ID < job.ID) {
			job = candidate
		}
	}
	base.ID = "validation:" + job.ID + ":" + string(job.Status)
	base.ValidationJobID = job.ID
	base.UpdatedAt = job.UpdatedAt
	switch job.Status {
	case validation.StatusPending:
		base.Kind = "validation_pending"
		base.Severity = "warning"
		base.Title = "Validation ready to run"
		base.Detail = "Run the isolated checks for the approved revision."
		return base, true
	case validation.StatusRunning:
		base.Kind = "validation_running"
		base.Severity = "info"
		base.Actionable = false
		base.Title = "Validation in progress"
		base.Detail = "Isolated checks are running for the approved revision."
		return base, true
	case validation.StatusFailed:
		base.Kind = "validation_failed"
		base.Severity = "error"
		base.Title = "Validation failed"
		base.Detail = detailOr(job.Error, "The isolated checks failed and may be retried.")
		return base, true
	default:
		return attentionItem{}, false
	}
}

func runWaitAttention(base attentionItem, run execution.Run) attentionItem {
	base.ID = "run_wait:" + run.ID + ":" + string(run.WaitKind) + ":" + run.UpdatedAt.Format(time.RFC3339Nano)
	base.Kind = string(run.WaitKind)
	base.Severity = "warning"
	base.Detail = detailOr(run.Reason, "The run is waiting for a decision.")
	switch run.WaitKind {
	case execution.RunWaitKindClarification:
		base.Title = "Clarification needed"
	case execution.RunWaitKindPhaseCheckpoint:
		base.Title = "Phase approval needed"
	case execution.RunWaitKindRoundCap:
		base.Title = "Round limit reached"
	case execution.RunWaitKindMergeGate:
		base.Kind = "merge_approval"
		base.Title = "Merge approval needed"
	case execution.RunWaitKindPaused:
		base.Title = "Run paused"
	default:
		base.Kind = "blocker"
		base.Title = "Run blocked"
	}
	return base
}

func detailOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func capCompletedItems(items []attentionItem, maximum int) []attentionItem {
	completed := 0
	out := make([]attentionItem, 0, len(items))
	for _, item := range items {
		if !item.Actionable {
			if item.Kind != "auto_merge_completed" {
				out = append(out, item)
				continue
			}
			if completed >= maximum {
				continue
			}
			completed++
		}
		out = append(out, item)
	}
	return out
}
