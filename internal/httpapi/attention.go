package httpapi

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
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
	ID                   string `json:"id"`
	Kind                 string `json:"kind"`
	Severity             string `json:"severity"`
	Actionable           bool   `json:"actionable"`
	ProjectID            string `json:"project_id"`
	ProjectName          string `json:"project_name"`
	FeatureID            string `json:"feature_id"`
	FeatureTitle         string `json:"feature_title"`
	RunID                string `json:"run_id,omitempty"`
	EnvironmentRequestID string `json:"environment_request_id,omitempty"`
	ValidationJobID      string `json:"validation_job_id,omitempty"`
	// AutoRun asks the desktop to run a pending validation job itself: the
	// run merges automatically once its gates pass.
	AutoRun   bool      `json:"auto_run,omitempty"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	UpdatedAt time.Time `json:"updated_at"`
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
			Title: mergedTitle(currentFeature), Detail: "Automatic merge completed after all gates passed.",
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
	currentRun := execution.NewestRun(runs)
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
		item, err := api.autopilotAttention(ctx, base, currentRun, currentProject.App)
		if err != nil {
			return nil, nil, err
		}
		return []attentionItem{item}, nil, nil
	}
	if currentRun.Status != execution.RunStatusWaitingForUser {
		return nil, nil, nil
	}
	// A session waiting for the user's next message is idle, not asking for
	// attention: the rail shows its new replies instead (ADR-019).
	if currentFeature.Kind == feature.KindSession && currentRun.WaitKind == execution.RunWaitKindClarification {
		return nil, nil, nil
	}

	if currentRun.WaitKind == execution.RunWaitKindMergeGate && api.validations != nil {
		jobs, err := api.validations.JobsForRun(ctx, currentRun.ID)
		if err != nil {
			return nil, nil, err
		}
		if item, ok := validationAttention(base, jobs); ok {
			item.AutoRun = item.Kind == "validation_pending" &&
				currentRun.MergePolicy == project.MergePolicyAutoAfterGates
			return []attentionItem{item}, nil, nil
		}
	}
	// An interrupted turn retries on its own: worth knowing, nothing to decide.
	if retrying, err := retryingFor(ctx, api.execution, currentRun); err != nil {
		return nil, nil, err
	} else if retrying != nil {
		base.ID = "retrying:" + currentRun.ID + ":" + strconv.Itoa(retrying.Retry)
		base.Kind = "retrying"
		base.Severity = "info"
		base.Actionable = false
		base.Title = "Interrupted, retrying automatically"
		base.Detail = detailOr(currentRun.Reason, "The interrupted turn is retried automatically.")
		return []attentionItem{base}, nil, nil
	}
	// A usage limit resumes on its own: worth knowing, nothing to decide.
	if waits, ok := api.execution.(runLimitWaits); ok && currentRun.WaitKind == execution.RunWaitKindBlocker {
		wait, err := waits.LimitWaitForRun(ctx, currentRun.ID)
		if err != nil {
			return nil, nil, err
		}
		if wait.AgentID != "" {
			base.ID = "usage_limit:" + currentRun.ID + ":" + currentRun.UpdatedAt.Format(time.RFC3339Nano)
			base.Kind = "usage_limit"
			base.Severity = "info"
			base.Actionable = false
			base.Title = "Waiting for " + wait.AgentID + "'s usage limit"
			base.Detail = detailOr(currentRun.Reason, "The work continues when the limit resets.")
			return []attentionItem{base}, nil, nil
		}
	}
	item, err := api.autopilotAttention(ctx, runWaitAttention(base, currentRun), currentRun, currentProject.App)
	if err != nil {
		return nil, nil, err
	}
	return []attentionItem{item}, nil, nil
}

// autopilotStallAfter is how long autopilot handles a blocker quietly
// without a step of its own. Past it, the user hears that autopilot has not
// finished, in plain words.
const autopilotStallAfter = 10 * time.Minute

// runStateSince is implemented by execution services that keep when a run
// entered its current state.
type runStateSince interface {
	RunStateSince(ctx context.Context, runID string) (time.Time, error)
}

// autopilotAttention quiets a blocked or failed run that autopilot is still
// on: the assistant is finding out why and continuing it. The item is not
// actionable and has its own kind and ID, so the inbox asks the user, and the
// desktop notifies, only once autopilot gives up. Should autopilot still be
// on it autopilotStallAfter after the run blocked, the item becomes an
// autopilot_stalled one (a new ID, so it notifies once) that sends the user
// to the assistant rather than to the blocker's details. Each step autopilot
// takes on it (a recovery it proposed or applied, a turn it runs) restarts
// that wait, so work it is still recovering never reads as stalled. In an
// app, a blocker autopilot gave up on reads plainly too.
func (api *API) autopilotAttention(
	ctx context.Context, item attentionItem, run execution.Run, app bool,
) (attentionItem, error) {
	if item.Kind == "blocker" {
		// Recovery ran out: the user hears it plainly, whoever was on it
		// (ADR-032).
		if blocker, exhausted := api.recoveryExhausted(ctx, run.ID); exhausted {
			item.ID = "recovery-exhausted:" + item.ID
			item.Kind = "recovery_exhausted"
			item.Severity = "warning"
			item.Actionable = true
			item.Title = "Commitarium couldn't finish this"
			item.Detail = exhaustedDetail(item.FeatureTitle, blocker.Kind)
			return item, nil
		}
	}
	if api.conversations == nil || (item.Kind != "blocker" && item.Kind != "run_failed") {
		return item, nil
	}
	// When the run blocked: its updated_at moves on while it waits.
	blockedAt := run.UpdatedAt
	if states, ok := api.execution.(runStateSince); ok {
		since, err := states.RunStateSince(ctx, run.ID)
		if err != nil {
			return attentionItem{}, err
		}
		if !since.IsZero() {
			blockedAt = since
		}
	}
	if !api.conversations.AutopilotHandles(ctx, item.ProjectID, item.FeatureID, blockedAt) {
		if app {
			item.Title = "This needs your help"
			item.Detail = item.FeatureTitle + " stopped and needs your help. Check in with the assistant."
		}
		return item, nil
	}
	quietSince := blockedAt
	if active := api.conversations.AutopilotActiveAt(ctx, item.ProjectID, item.FeatureID); active.After(quietSince) {
		quietSince = active
	}
	if stalledAt := quietSince.Add(autopilotStallAfter); api.clock().After(stalledAt) {
		item.ID = "autopilot-stalled:" + item.ID
		item.Kind = "autopilot_stalled"
		item.Severity = "warning"
		item.Actionable = true
		item.Title = "Autopilot hasn't finished this yet"
		item.Detail = fmt.Sprintf("%s has been waiting for %d minutes. Check in with the assistant.",
			item.FeatureTitle, int(autopilotStallAfter/time.Minute))
		item.UpdatedAt = stalledAt
		return item, nil
	}
	item.ID = "autopilot:" + item.ID
	item.Kind = "autopilot_handling"
	item.Severity = "info"
	item.Actionable = false
	item.Title = "Autopilot is handling this"
	return item, nil
}

// clock is the API's time, which tests set.
func (api *API) clock() time.Time {
	if api.now != nil {
		return api.now()
	}
	return time.Now()
}

func mergedTitle(currentFeature feature.Feature) string {
	if currentFeature.Kind == feature.KindSession {
		return "Session merged"
	}
	return "Work order merged"
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
		// A run that planned early waits to implement until the work it
		// builds on merges or a slot frees (ADR-037), and then goes on by
		// itself: nothing waits on the user.
		if execution.HeldBeforeImplementation(run.Reason) {
			base.Kind = "implementation_held"
			base.Severity = "info"
			base.Actionable = false
			base.Title = "Planned, waiting to implement"
		}
	case execution.RunWaitKindRoundCap:
		base.Title = "Round limit reached"
	case execution.RunWaitKindMergeGate:
		base.Kind = "merge_approval"
		base.Title = "Merge approval needed"
		// A run in its project's merge queue merges when its turn comes, on
		// its own: nothing waits on the user.
		if execution.InMergeQueue(run.Reason) {
			base.Kind = "merge_queued"
			base.Severity = "info"
			base.Actionable = false
			base.Title = "Waiting to merge"
		}
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
