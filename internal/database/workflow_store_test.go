package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestWorkflowStoreAppliesFeatureTransitionAtomically(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	transition := testFeatureTransition(
		"evt_planning",
		created.ID,
		feature.StatePlanning,
		"cmd_planning",
	)

	event, err := store.ApplyFeatureTransition(t.Context(), transition)
	if err != nil {
		t.Fatalf("apply feature transition: %v", err)
	}

	updated, err := features.GetByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("get updated feature: %v", err)
	}
	if updated.State != feature.StatePlanning {
		t.Errorf("expected state %q, got %q", feature.StatePlanning, updated.State)
	}
	if updated.UpdatedAt != transition.OccurredAt {
		t.Errorf(
			"expected update time %v, got %v",
			transition.OccurredAt,
			updated.UpdatedAt,
		)
	}

	events, err := store.ListEvents(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("list workflow events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one event, got %d", len(events))
	}
	if events[0] != event {
		t.Errorf("expected stored event %+v, got %+v", event, events[0])
	}
	if event.Sequence != 1 {
		t.Errorf("expected sequence 1, got %d", event.Sequence)
	}

	payload, err := workflow.DecodeFeatureStateChangedPayload(
		event.PayloadVersion,
		event.Payload,
	)
	if err != nil {
		t.Fatalf("decode stored payload: %v", err)
	}
	if payload.PreviousState != feature.StateDraft ||
		payload.State != feature.StatePlanning {
		t.Errorf("unexpected state-change payload %+v", payload)
	}
}

func TestWorkflowStoreSequencesEventsAndHandlesIdempotentRetry(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	firstRequest := testFeatureTransition(
		"evt_planning",
		created.ID,
		feature.StatePlanning,
		"cmd_planning",
	)

	firstEvent, err := store.ApplyFeatureTransition(t.Context(), firstRequest)
	if err != nil {
		t.Fatalf("apply first transition: %v", err)
	}

	retry := firstRequest
	retry.EventID = "evt_retry_is_ignored"
	retry.OccurredAt = retry.OccurredAt.Add(time.Hour)
	retriedEvent, err := store.ApplyFeatureTransition(t.Context(), retry)
	if err != nil {
		t.Fatalf("retry transition: %v", err)
	}
	if retriedEvent != firstEvent {
		t.Errorf("expected original event %+v, got %+v", firstEvent, retriedEvent)
	}

	secondRequest := testFeatureTransition(
		"evt_implementing",
		created.ID,
		feature.StateImplementing,
		"cmd_implementing",
	)
	secondRequest.OccurredAt = firstRequest.OccurredAt.Add(time.Minute)
	secondEvent, err := store.ApplyFeatureTransition(t.Context(), secondRequest)
	if err != nil {
		t.Fatalf("apply second transition: %v", err)
	}
	if secondEvent.Sequence != 2 {
		t.Errorf("expected sequence 2, got %d", secondEvent.Sequence)
	}

	events, err := store.ListEvents(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("list workflow events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two events, got %d", len(events))
	}
}

func TestWorkflowStoreRejectsIdempotencyKeyReuse(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	first := testFeatureTransition(
		"evt_planning",
		created.ID,
		feature.StatePlanning,
		"cmd_same",
	)
	if _, err := store.ApplyFeatureTransition(t.Context(), first); err != nil {
		t.Fatalf("apply first transition: %v", err)
	}

	conflicting := testFeatureTransition(
		"evt_conflict",
		created.ID,
		feature.StateImplementing,
		"cmd_same",
	)
	_, err := store.ApplyFeatureTransition(t.Context(), conflicting)
	if !errors.Is(err, workflow.ErrIdempotencyConflict) {
		t.Fatalf("expected error %v, got %v", workflow.ErrIdempotencyConflict, err)
	}
}

func TestWorkflowStoreRollsBackFeatureWhenEventInsertFails(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	first := testFeatureTransition(
		"evt_same",
		created.ID,
		feature.StatePlanning,
		"cmd_planning",
	)
	if _, err := store.ApplyFeatureTransition(t.Context(), first); err != nil {
		t.Fatalf("apply first transition: %v", err)
	}

	duplicateEventID := testFeatureTransition(
		"evt_same",
		created.ID,
		feature.StateImplementing,
		"cmd_implementing",
	)
	_, err := store.ApplyFeatureTransition(t.Context(), duplicateEventID)
	if err == nil {
		t.Fatal("expected duplicate event ID to fail")
	}

	stored, err := features.GetByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("get feature after rollback: %v", err)
	}
	if stored.State != feature.StatePlanning {
		t.Errorf(
			"expected rolled-back state %q, got %q",
			feature.StatePlanning,
			stored.State,
		)
	}

	events, err := store.ListEvents(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("list events after rollback: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected one committed event, got %d", len(events))
	}
}

func TestWorkflowStoreRejectsInvalidTransitionWithoutWriting(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	invalid := testFeatureTransition(
		"evt_invalid",
		created.ID,
		feature.StateReviewing,
		"cmd_invalid",
	)

	_, err := store.ApplyFeatureTransition(t.Context(), invalid)
	if !errors.Is(err, feature.ErrInvalidTransition) {
		t.Fatalf("expected error %v, got %v", feature.ErrInvalidTransition, err)
	}

	stored, err := features.GetByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("get feature after rejection: %v", err)
	}
	if stored.State != feature.StateDraft {
		t.Errorf("expected unchanged state %q, got %q", feature.StateDraft, stored.State)
	}

	events, err := store.ListEvents(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("list events after rejection: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no events, got %d", len(events))
	}
}

func TestWorkflowStoreReturnsNotFoundForUnknownFeature(t *testing.T) {
	store, _ := newTestWorkflowStore(t)
	transition := testFeatureTransition(
		"evt_missing",
		"fea_missing",
		feature.StatePlanning,
		"cmd_missing",
	)

	_, err := store.ApplyFeatureTransition(t.Context(), transition)
	if !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf("expected error %v, got %v", feature.ErrNotFound, err)
	}
}

func TestWorkflowStoreHonorsCanceledContext(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.ApplyFeatureTransition(
		ctx,
		testFeatureTransition(
			"evt_cancelled",
			created.ID,
			feature.StatePlanning,
			"cmd_cancelled",
		),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func newTestWorkflowStore(
	t *testing.T,
) (*WorkflowStore, *FeatureStore) {
	t.Helper()

	db, err := OpenSQLite(
		t.Context(),
		filepath.Join(t.TempDir(), "coordinator.db"),
	)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close SQLite database: %v", err)
		}
	})

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	projects := NewProjectStore(db)
	if err := projects.Create(t.Context(), project.Project{
		ID:        "prj_workflow_test",
		Name:      "Workflow test project",
		CreatedAt: time.Date(2026, time.September, 8, 14, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("create workflow test project: %v", err)
	}

	return NewWorkflowStore(db), NewFeatureStore(db)
}

func createWorkflowTestFeature(
	t *testing.T,
	store *FeatureStore,
) feature.Feature {
	t.Helper()

	created := feature.Feature{
		ID:          "fea_workflow_test",
		ProjectID:   "prj_workflow_test",
		Title:       "Atomic transitions",
		Description: "Persist state and event together",
		State:       feature.StateDraft,
		CreatedAt:   time.Date(2026, time.September, 8, 15, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, time.September, 8, 15, 0, 0, 0, time.UTC),
	}
	if err := store.Create(t.Context(), created); err != nil {
		t.Fatalf("create workflow test feature: %v", err)
	}
	return created
}

func testFeatureTransition(
	eventID string,
	featureID string,
	state feature.State,
	idempotencyKey string,
) workflow.FeatureTransition {
	return workflow.FeatureTransition{
		EventID:   eventID,
		FeatureID: featureID,
		State:     state,
		Actor: workflow.Actor{
			Kind: workflow.ActorKindAgent,
			ID:   "agt_test",
		},
		OccurredAt:     time.Date(2026, time.September, 8, 16, 0, 0, 0, time.UTC),
		IdempotencyKey: idempotencyKey,
	}
}
