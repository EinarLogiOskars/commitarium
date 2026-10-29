package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
)

type attentionEnvironmentService struct {
	requests []projectenvironment.Request
	packages []string
}

func (service *attentionEnvironmentService) Get(context.Context, string) (projectenvironment.Request, error) {
	return projectenvironment.Request{}, projectenvironment.ErrNotFound
}
func (service *attentionEnvironmentService) ListByProject(context.Context, string) ([]projectenvironment.Request, error) {
	return service.requests, nil
}
func (service *attentionEnvironmentService) Approve(context.Context, string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) Reject(context.Context, string, string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) BeginProvisioning(context.Context, string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) Complete(context.Context, string, map[string]string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) Fail(context.Context, string, string) (projectenvironment.Request, bool, error) {
	return projectenvironment.Request{}, false, nil
}
func (service *attentionEnvironmentService) ApprovedPackages(context.Context) ([]string, error) {
	return service.packages, nil
}

type attentionValidationService struct {
	jobs []validation.Job
}

func (service *attentionValidationService) Configure(context.Context, string, []string) (validation.Config, error) {
	return validation.Config{}, nil
}
func (service *attentionValidationService) Disable(context.Context, string) (bool, error) {
	return true, nil
}
func (service *attentionValidationService) GetConfig(context.Context, string) (validation.Config, error) {
	return validation.Config{}, nil
}
func (service *attentionValidationService) GetJob(context.Context, string) (validation.Job, error) {
	return validation.Job{}, validation.ErrNotFound
}
func (service *attentionValidationService) JobsForRun(context.Context, string) ([]validation.Job, error) {
	return service.jobs, nil
}
func (service *attentionValidationService) Claim(context.Context, string) (validation.Job, bool, error) {
	return validation.Job{}, false, nil
}
func (service *attentionValidationService) Complete(context.Context, string, []validation.CommandResult, string) (validation.Job, bool, error) {
	return validation.Job{}, false, nil
}
func (service *attentionValidationService) Retry(context.Context, string) (validation.Job, bool, error) {
	return validation.Job{}, false, nil
}

func TestAttentionAggregatesProjectsAndPrefersEnvironmentRequest(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
	features := &recordingFeatureService{listResult: []feature.Feature{{
		ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
	}}}
	executions := &recordingExecutionService{runs: []execution.Run{{
		ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
		WaitKind: execution.RunWaitKindBlocker, Reason: "missing jq", StartedAt: now, UpdatedAt: now,
	}}}
	environments := &attentionEnvironmentService{requests: []projectenvironment.Request{{
		ID: "env_one", ProjectID: "prj_one", FeatureID: "fea_one", RunID: "run_one",
		Status: projectenvironment.StatusRequested, Reason: "Install jq", UpdatedAt: now.Add(time.Minute),
	}}}

	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		projects, features, nil, executions, nil, nil, nil, nil, nil, nil, nil, nil, environments,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Kind != "environment_approval" ||
		response.Items[0].EnvironmentRequestID != "env_one" || response.Items[0].FeatureTitle != "Ship it" {
		t.Fatalf("unexpected attention response: %+v", response)
	}
}

func TestAttentionSurfacesFailedValidation(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
	features := &recordingFeatureService{listResult: []feature.Feature{{
		ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateReadyToMerge,
	}}}
	executions := &recordingExecutionService{runs: []execution.Run{{
		ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
		WaitKind: execution.RunWaitKindMergeGate, StartedAt: now, UpdatedAt: now,
	}}}
	validations := &attentionValidationService{jobs: []validation.Job{{
		ID: "val_one", RunID: "run_one", Status: validation.StatusFailed,
		Error: "tests failed", CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}}}

	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		projects, features, nil, executions, nil, nil, nil, nil, nil, nil, nil, nil, validations,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Kind != "validation_failed" ||
		response.Items[0].ValidationJobID != "val_one" || response.Items[0].Detail != "tests failed" {
		t.Fatalf("unexpected attention response: %+v", response)
	}
}

func TestAttentionRotatesIDWhenEnvironmentRetryFailsAgain(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
	features := &recordingFeatureService{listResult: []feature.Feature{{
		ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
	}}}
	executions := &recordingExecutionService{runs: []execution.Run{{
		ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
		WaitKind: execution.RunWaitKindBlocker, StartedAt: now, UpdatedAt: now,
	}}}
	environments := &attentionEnvironmentService{requests: []projectenvironment.Request{{
		ID: "env_one", ProjectID: "prj_one", FeatureID: "fea_one", RunID: "run_one",
		Status: projectenvironment.StatusFailed, Error: "first failure", UpdatedAt: now.Add(time.Minute),
	}}}
	api := &API{projects: projects, features: features, execution: executions, environments: environments}

	first, _, err := api.collectAttention(t.Context())
	if err != nil || len(first) != 1 {
		t.Fatalf("collect first failure: items=%+v err=%v", first, err)
	}
	environments.requests[0].Error = "second failure"
	environments.requests[0].UpdatedAt = now.Add(2 * time.Minute)
	second, _, err := api.collectAttention(t.Context())
	if err != nil || len(second) != 1 {
		t.Fatalf("collect second failure: items=%+v err=%v", second, err)
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("environment retry reused notification ID %q", first[0].ID)
	}
}

func TestAttentionReportsRunningWorkWithoutInboxItem(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	handler := New(
		&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
		&recordingFeatureService{listResult: []feature.Feature{{
			ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
		}}}, nil,
		&recordingExecutionService{runs: []execution.Run{{
			ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusRunning,
			StartedAt: now, UpdatedAt: now.Add(time.Minute),
		}}}, nil, nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if response.Items == nil || len(response.Items) != 0 || len(response.Running) != 1 {
		t.Fatalf("expected one running work order and no items, got %+v", response)
	}
	if response.Running[0].RunID != "run_one" || response.Running[0].ProjectName != "One" ||
		response.Running[0].FeatureTitle != "Ship it" {
		t.Fatalf("unexpected running work order %+v", response.Running[0])
	}
}

func TestAttentionReportsWaitingRunOnlyAsInboxItem(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	handler := New(
		&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
		&recordingFeatureService{listResult: []feature.Feature{{
			ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
		}}}, nil,
		&recordingExecutionService{runs: []execution.Run{{
			ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
			WaitKind: execution.RunWaitKindClarification, StartedAt: now, UpdatedAt: now,
		}}}, nil, nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 1 || response.Running == nil || len(response.Running) != 0 ||
		response.Items[0].Kind != "clarification" {
		t.Fatalf("expected one waiting item and no running work orders, got %+v", response)
	}
}

func TestAttentionIncludesBoundedAutomaticMergeHistory(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	completed := make([]feature.Feature, 30)
	for index := range completed {
		completed[index] = feature.Feature{
			ID: "fea_" + string(rune('a'+index)), ProjectID: "prj_one", Title: "Done",
			State: feature.StateCompleted, MergePolicy: project.MergePolicyAutoAfterGates,
			UpdatedAt: now.Add(time.Duration(index) * time.Minute),
		}
	}
	handler := New(
		&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
		&recordingFeatureService{listResult: completed}, nil, &recordingExecutionService{}, nil, nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 25 || response.Items[0].UpdatedAt != completed[29].UpdatedAt {
		t.Fatalf("expected newest 25 automatic merges, got %+v", response.Items)
	}
}

func TestAttentionSkipsASessionWaitingForTheNextMessage(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
	features := &recordingFeatureService{listResult: []feature.Feature{{
		ID: "fea_one", ProjectID: "prj_one", Title: "Untitled session 1",
		State: feature.StateImplementing, Kind: feature.KindSession,
	}}}
	executions := &recordingExecutionService{runs: []execution.Run{{
		ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
		WaitKind: execution.RunWaitKindClarification, Reason: "Your turn.", StartedAt: now, UpdatedAt: now,
	}}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		projects, features, nil, executions, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 0 {
		t.Fatalf("an idle session asked for attention: %+v", response.Items)
	}
}

func TestAttentionAsksToAutoRunValidationOnlyUnderAutoMerge(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	for _, policy := range []project.MergePolicy{project.MergePolicyAutoAfterGates, project.MergePolicyRequireUserApproval} {
		projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
		features := &recordingFeatureService{listResult: []feature.Feature{{
			ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateReadyToMerge,
		}}}
		executions := &recordingExecutionService{runs: []execution.Run{{
			ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
			WaitKind: execution.RunWaitKindMergeGate, MergePolicy: policy, StartedAt: now, UpdatedAt: now,
		}}}
		validations := &attentionValidationService{jobs: []validation.Job{{
			ID: "val_one", RunID: "run_one", Status: validation.StatusPending, CreatedAt: now, UpdatedAt: now,
		}}}
		handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
			projects, features, nil, executions, nil, nil, nil, nil, nil, nil, nil, nil, validations,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
		var response attentionResponse
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("decode attention response: %v", err)
		}
		want := policy == project.MergePolicyAutoAfterGates
		if len(response.Items) != 1 || response.Items[0].Kind != "validation_pending" || response.Items[0].AutoRun != want {
			t.Fatalf("%s: unexpected attention response: %+v", policy, response.Items)
		}
	}
}

// A blocker autopilot is still on is shown quietly, under its own kind and ID,
// so the inbox asks (and the desktop notifies) only once autopilot gives up.
// A run that planned early and waits to implement until the work it builds
// on merges goes on by itself: the inbox shows it, but asks nothing.
func TestAttentionQuietsWorkWaitingToImplement(t *testing.T) {
	now := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	for reason, held := range map[string]bool{
		execution.ImplementationHoldReason("Build the app shell"): true,
		execution.ImplementationSlotReason(3):                     true,
		"Review the plan before implementing.":                    false,
	} {
		handler := New(
			&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
			&recordingFeatureService{listResult: []feature.Feature{{
				ID: "fea_one", ProjectID: "prj_one", Title: "Live list", State: feature.StatePlanning,
			}}}, nil,
			&recordingExecutionService{runs: []execution.Run{{
				ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
				WaitKind: execution.RunWaitKindPhaseCheckpoint, Reason: reason, StartedAt: now, UpdatedAt: now,
			}}}, nil, nil,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
		var response attentionResponse
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("decode attention response: %v", err)
		}
		if len(response.Items) != 1 {
			t.Fatalf("%q: items = %+v", reason, response.Items)
		}
		item := response.Items[0]
		if held && (item.Kind != "implementation_held" || item.Actionable || item.Detail != reason) ||
			!held && (item.Kind != "phase_checkpoint" || !item.Actionable) {
			t.Fatalf("%q: item = %+v", reason, item)
		}
	}
}

// A run in its project's merge queue merges when its turn comes: the inbox
// shows its place, but asks nothing. A merge gate that waits for the user's
// approval still asks.
func TestAttentionQuietsWorkInTheMergeQueue(t *testing.T) {
	now := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	for reason, queued := range map[string]bool{
		execution.MergeQueueReason(2):          true,
		"Approve the merge when you're ready.": false,
	} {
		handler := New(
			&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
			&recordingFeatureService{listResult: []feature.Feature{{
				ID: "fea_one", ProjectID: "prj_one", Title: "Live list", State: feature.StateReadyToMerge,
			}}}, nil,
			&recordingExecutionService{runs: []execution.Run{{
				ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
				WaitKind: execution.RunWaitKindMergeGate, Reason: reason, StartedAt: now, UpdatedAt: now,
			}}}, nil, nil,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
		var response attentionResponse
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("decode attention response: %v", err)
		}
		if len(response.Items) != 1 {
			t.Fatalf("%q: items = %+v", reason, response.Items)
		}
		item := response.Items[0]
		if queued && (item.Kind != "merge_queued" || item.Actionable || item.Detail != reason) ||
			!queued && (item.Kind != "merge_approval" || !item.Actionable) {
			t.Fatalf("%q: item = %+v", reason, item)
		}
	}
}

func TestAttentionQuietsABlockerAutopilotIsHandling(t *testing.T) {
	// Blocked just now: autopilot has yet to run out of time.
	now := time.Now().UTC()
	for _, handling := range []bool{true, false} {
		projects := &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}}
		features := &recordingFeatureService{listResult: []feature.Feature{{
			ID: "fea_one", ProjectID: "prj_one", Title: "Polish mobile", State: feature.StateImplementing,
		}}}
		executions := &recordingExecutionService{runs: []execution.Run{{
			ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
			WaitKind: execution.RunWaitKindBlocker, Reason: "The lead ended early.", StartedAt: now, UpdatedAt: now,
		}}}
		stub := &conversationStub{handling: map[string]bool{"fea_one": handling}}
		handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
			projects, features, nil, executions, nil, nil, nil, nil, nil, nil, nil, nil, stub,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
		var response attentionResponse
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatalf("decode attention response: %v", err)
		}
		if len(response.Items) != 1 {
			t.Fatalf("handling=%t: items = %+v", handling, response.Items)
		}
		item := response.Items[0]
		quiet := item.Kind == "autopilot_handling" && !item.Actionable && strings.HasPrefix(item.ID, "autopilot:")
		if quiet != handling || (!handling && (item.Kind != "blocker" || !item.Actionable)) {
			t.Fatalf("handling=%t: item = %+v", handling, item)
		}
	}
}

// stateSinceExecutions keeps when its runs blocked, apart from updated_at.
type stateSinceExecutions struct {
	*recordingExecutionService
	since map[string]time.Time
}

func (service stateSinceExecutions) RunStateSince(_ context.Context, runID string) (time.Time, error) {
	return service.since[runID], nil
}

// Autopilot handles a blocker quietly for autopilotStallAfter from when the
// run blocked (not its updated_at, which a model change moves on); past it,
// the user hears so plainly, under a new kind and ID that sends them to the
// assistant. A blocker autopilot gave up on stays a blocker, worded plainly
// in an app.
func TestAttentionSurfacesAutopilotThatHasNotFinished(t *testing.T) {
	blockedAt := time.Date(2026, time.October, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		handling bool
		app      bool
		now      time.Time
		active   time.Time
		kind     string
		id       string
		title    string
	}{
		{"handling", true, false, blockedAt.Add(autopilotStallAfter - time.Second), time.Time{}, "autopilot_handling", "autopilot:", "Autopilot is handling this"},
		{"stalled", true, false, blockedAt.Add(autopilotStallAfter + time.Second), time.Time{}, "autopilot_stalled", "autopilot-stalled:", "Autopilot hasn't finished this yet"},
		// Still recovering: its last step was five minutes ago.
		{"still recovering", true, false, blockedAt.Add(autopilotStallAfter + time.Second), blockedAt.Add(5 * time.Minute), "autopilot_handling", "autopilot:", "Autopilot is handling this"},
		{"gave up", false, false, blockedAt.Add(time.Hour), time.Time{}, "blocker", "run_wait:", "Run blocked"},
		{"gave up in an app", false, true, blockedAt.Add(time.Hour), time.Time{}, "blocker", "run_wait:", "This needs your help"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The model was changed while it waited.
			updatedAt := blockedAt.Add(8 * time.Minute)
			executions := stateSinceExecutions{
				recordingExecutionService: &recordingExecutionService{runs: []execution.Run{{
					ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
					WaitKind: execution.RunWaitKindBlocker, Reason: "The lead ended early.",
					StartedAt: blockedAt.Add(-time.Hour), UpdatedAt: updatedAt,
				}}},
				since: map[string]time.Time{"run_one": blockedAt},
			}
			stub := &conversationStub{
				handling: map[string]bool{"fea_one": tc.handling}, activeAt: map[string]time.Time{"fea_one": tc.active},
			}
			api := &API{
				projects: &recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One", App: tc.app}}},
				features: &recordingFeatureService{listResult: []feature.Feature{{
					ID: "fea_one", ProjectID: "prj_one", Title: "Polish mobile", State: feature.StateImplementing,
				}}},
				execution: executions, conversations: stub,
				now: func() time.Time { return tc.now },
			}
			items, _, err := api.collectAttention(t.Context())
			if err != nil {
				t.Fatalf("collect attention: %v", err)
			}
			if !stub.blockedAt.Equal(blockedAt) {
				t.Fatalf("asked autopilot about a blocker at %v, want %v", stub.blockedAt, blockedAt)
			}
			if len(items) != 1 {
				t.Fatalf("items = %+v", items)
			}
			item := items[0]
			if item.Kind != tc.kind || !strings.HasPrefix(item.ID, tc.id) || item.Title != tc.title ||
				item.Actionable != (tc.kind != "autopilot_handling") {
				t.Fatalf("item = %+v", item)
			}
			if tc.kind == "autopilot_stalled" &&
				(item.Detail != "Polish mobile has been waiting for 10 minutes. Check in with Mayka." ||
					item.ID != "autopilot-stalled:run_wait:run_one:blocker:"+updatedAt.Format(time.RFC3339Nano)) {
				t.Fatalf("stalled item = %+v", item)
			}
		})
	}
}

type retryingExecutionService struct {
	*recordingExecutionService
	wait execution.RetryWait
}

func (service retryingExecutionService) RetryWaitForRun(context.Context, string) (execution.RetryWait, error) {
	return service.wait, nil
}

func TestAttentionQuietsAnInterruptedTurnThatRetries(t *testing.T) {
	now := time.Date(2026, time.September, 23, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(2 * time.Minute)
	handler := New(
		&recordingProjectService{listResult: []project.Project{{ID: "prj_one", Name: "One"}}},
		&recordingFeatureService{listResult: []feature.Feature{{
			ID: "fea_one", ProjectID: "prj_one", Title: "Ship it", State: feature.StateImplementing,
		}}}, nil,
		retryingExecutionService{
			recordingExecutionService: &recordingExecutionService{runs: []execution.Run{{
				ID: "run_one", FeatureID: "fea_one", Status: execution.RunStatusWaitingForUser,
				WaitKind: execution.RunWaitKindBlocker, StartedAt: now, UpdatedAt: now,
				Reason: "The lead agent's turn was interrupted (the worker restarted). Retrying automatically in 2 minutes (2 of 3).",
			}}},
			wait: execution.RetryWait{RunID: "run_one", Retries: 1, Cause: "the worker restarted", DueAt: &dueAt},
		}, nil, nil,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
	var response attentionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode attention response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].Kind != "retrying" || response.Items[0].Actionable ||
		response.Items[0].ID != "retrying:run_one:2" {
		t.Fatalf("an interrupted turn that retries asked for the user: %+v", response.Items)
	}
}
