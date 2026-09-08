package orchestration

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestRunnerDrivesPlanningImplementationAndReviewLoop(t *testing.T) {
	workflowService, featureStore := orchestrationDatabase(t)
	codex := autoAdvance(newTestCodex())
	claude := autoAdvance(newTestClaude(
		worker.DispositionChangesRequested,
		worker.DispositionSucceeded,
	))
	sink := &recordingEventSink{}
	runner := NewRunner(workflowService, sink)
	request := testRunRequest(codex, claude, 2)

	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("run feature: %v", err)
	}
	if result.Status != RunStatusReadyToMerge {
		t.Fatalf("expected status %q, got %+v", RunStatusReadyToMerge, result)
	}
	if result.PlanningRounds != 1 || result.ReviewRounds != 2 {
		t.Errorf("unexpected round counts %+v", result)
	}
	if len(result.Sessions) != 6 {
		t.Errorf("expected six sessions, got %d", len(result.Sessions))
	}
	if len(sink.events) != 6 {
		t.Errorf("expected six observable session events, got %d", len(sink.events))
	}
	if !strings.Contains(claude.requests[0].Instructions, "proposed an accepted plan") {
		t.Errorf("consultant did not receive lead proposal: %q", claude.requests[0].Instructions)
	}
	if !strings.Contains(codex.requests[1].Instructions, "accepted the refined plan") {
		t.Errorf("coder did not receive accepted plan: %q", codex.requests[1].Instructions)
	}
	if !strings.Contains(codex.requests[2].Instructions, "changes_requested") {
		t.Errorf("fix session did not receive review findings: %q", codex.requests[2].Instructions)
	}
	if !strings.Contains(claude.requests[2].Instructions, "addressed the review findings") {
		t.Errorf("second review did not receive fix summary: %q", claude.requests[2].Instructions)
	}

	storedFeature, err := featureStore.GetByID(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if storedFeature.State != feature.StateReadyToMerge {
		t.Errorf("expected state %q, got %q", feature.StateReadyToMerge, storedFeature.State)
	}
	retried, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if retried.Status != RunStatusReadyToMerge || len(retried.Sessions) != 6 {
		t.Errorf("unexpected retried result %+v", retried)
	}
	events, err := workflowService.EventsForFeature(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("list workflow events: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected four workflow events, got %d", len(events))
	}
	expectedStates := []feature.State{
		feature.StatePlanning,
		feature.StateImplementing,
		feature.StateReviewing,
		feature.StateReadyToMerge,
	}
	for index, event := range events {
		payload, err := workflow.DecodeFeatureStateChangedPayload(
			event.PayloadVersion,
			event.Payload,
		)
		if err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		if payload.State != expectedStates[index] {
			t.Errorf("expected event %d state %q, got %q", index, expectedStates[index], payload.State)
		}
		if event.Actor.Kind != workflow.ActorKindCoordinator || event.Actor.ID != coordinatorActorID {
			t.Errorf("unexpected transition actor %+v", event.Actor)
		}
	}
}

func TestRunnerWaitsForUserWhenReviewLimitIsReached(t *testing.T) {
	workflowService, featureStore := orchestrationDatabase(t)
	codex := autoAdvance(newTestCodex())
	claude := autoAdvance(newTestClaude(worker.DispositionChangesRequested))
	runner := NewRunner(workflowService, nil)

	result, err := runner.Run(t.Context(), testRunRequest(codex, claude, 1))
	if err != nil {
		t.Fatalf("run feature: %v", err)
	}
	if result.Status != RunStatusWaiting ||
		result.Reason != "review round limit reached with unresolved findings" {
		t.Errorf("unexpected run result %+v", result)
	}
	storedFeature, err := featureStore.GetByID(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if storedFeature.State != feature.StateReviewing {
		t.Errorf("expected state %q, got %q", feature.StateReviewing, storedFeature.State)
	}
}

func TestRunnerWaitsForUserWhenPlanningLimitIsReached(t *testing.T) {
	workflowService, featureStore := orchestrationDatabase(t)
	codex := autoAdvance(worker.NewQueuedScriptedAdapter("fake-codex", map[worker.Role][]worker.Script{
		worker.RoleLead: {
			{
				Events:      []worker.Event{{Type: worker.EventMessage, Text: "plan still has a material disagreement"}},
				Disposition: worker.DispositionChangesRequested,
				Summary:     "planning disagreement",
			},
		},
	}))
	claude := autoAdvance(newTestClaude(worker.DispositionSucceeded))
	runner := NewRunner(workflowService, nil)
	request := testRunRequest(codex, claude, 1)
	request.MaxPlanningRounds = 1

	result, err := runner.Run(t.Context(), request)
	if err != nil {
		t.Fatalf("run feature: %v", err)
	}
	if result.Status != RunStatusWaiting ||
		result.Reason != "planning round limit reached without agent agreement" {
		t.Errorf("unexpected run result %+v", result)
	}
	storedFeature, err := featureStore.GetByID(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}
	if storedFeature.State != feature.StatePlanning {
		t.Errorf("expected state %q, got %q", feature.StatePlanning, storedFeature.State)
	}
}

func newTestCodex() *worker.ScriptedAdapter {
	return worker.NewQueuedScriptedAdapter("fake-codex", map[worker.Role][]worker.Script{
		worker.RoleLead: {
			completedScript("proposed an accepted plan"),
		},
		worker.RoleCoder: {
			completedScript("implemented the plan"),
			completedScript("addressed the review findings"),
		},
	})
}

func newTestClaude(reviewDispositions ...worker.Disposition) *worker.ScriptedAdapter {
	reviews := make([]worker.Script, 0, len(reviewDispositions))
	for _, disposition := range reviewDispositions {
		reviews = append(reviews, worker.Script{
			Events:      []worker.Event{{Type: worker.EventMessage, Text: "completed independent review"}},
			Disposition: disposition,
			Summary:     string(disposition),
		})
	}
	return worker.NewQueuedScriptedAdapter("fake-claude", map[worker.Role][]worker.Script{
		worker.RoleConsultant: {
			completedScript("accepted the refined plan"),
		},
		worker.RoleReviewer: reviews,
	})
}

type autoAdvancingAdapter struct {
	inner    *worker.ScriptedAdapter
	requests []worker.SessionRequest
}

func autoAdvance(inner *worker.ScriptedAdapter) *autoAdvancingAdapter {
	return &autoAdvancingAdapter{inner: inner}
}

func (a *autoAdvancingAdapter) Start(
	ctx context.Context,
	request worker.SessionRequest,
) (worker.Session, error) {
	a.requests = append(a.requests, request)
	session, err := a.inner.Start(ctx, request)
	if err != nil {
		return nil, err
	}
	go a.advanceUntilFinished(request.SessionID)
	return session, nil
}

func (a *autoAdvancingAdapter) Resume(
	ctx context.Context,
	request worker.ResumeRequest,
) (worker.Session, error) {
	return a.inner.Resume(ctx, request)
}

func (a *autoAdvancingAdapter) advanceUntilFinished(sessionID string) {
	for {
		err := a.inner.Advance(context.Background(), sessionID)
		if errors.Is(err, worker.ErrSessionFinished) {
			return
		}
		if err != nil {
			return
		}
	}
}

type recordingEventSink struct {
	events []SessionEvent
}

func (s *recordingEventSink) RecordSessionEvent(
	_ context.Context,
	event SessionEvent,
) error {
	s.events = append(s.events, event)
	return nil
}

func completedScript(message string) worker.Script {
	return worker.Script{
		Events:      []worker.Event{{Type: worker.EventMessage, Text: message}},
		Disposition: worker.DispositionSucceeded,
		Summary:     message,
	}
}

func testRunRequest(
	codex worker.Adapter,
	claude worker.Adapter,
	maxReviewRounds int,
) RunRequest {
	return RunRequest{
		ID:        "run_test",
		FeatureID: "fea_test",
		Goal:      "Add a deterministic feature",
		Assignment: Assignment{
			Lead:       Agent{ID: "agt_codex", Adapter: codex},
			Consultant: Agent{ID: "agt_claude", Adapter: claude},
			Coder:      Agent{ID: "agt_codex", Adapter: codex},
			Reviewer:   Agent{ID: "agt_claude", Adapter: claude},
		},
		MaxPlanningRounds: 2,
		MaxReviewRounds:   maxReviewRounds,
	}
}

func orchestrationDatabase(
	t *testing.T,
) (*workflow.Service, *database.FeatureStore) {
	t.Helper()
	db, err := database.OpenSQLite(
		t.Context(),
		filepath.Join(t.TempDir(), "coordinator.db"),
	)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, time.September, 8, 21, 0, 0, 0, time.UTC)
	projectStore := database.NewProjectStore(db)
	if err := projectStore.Create(t.Context(), project.Project{
		ID: "prj_test", Name: "Test", CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	featureStore := database.NewFeatureStore(db)
	if err := featureStore.Create(t.Context(), feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", Title: "Test",
		State: feature.StateDraft, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	return workflow.NewService(database.NewWorkflowStore(db)), featureStore
}
