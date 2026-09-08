package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

func TestFeatureStoreCreateAndGetByID(t *testing.T) {
	store, projects := newTestFeatureStore(t)
	createFeatureTestProject(t, projects, "prj_test")

	expected := feature.Feature{
		ID:          "fea_test",
		ProjectID:   "prj_test",
		Title:       "Durable features",
		Description: "Persist feature data",
		State:       feature.StateDraft,
		CreatedAt: time.Date(
			2026,
			time.September,
			8,
			12,
			0,
			0,
			123456789,
			time.UTC,
		),
		UpdatedAt: time.Date(
			2026,
			time.September,
			8,
			13,
			0,
			0,
			987654321,
			time.UTC,
		),
	}

	if err := store.Create(t.Context(), expected); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	actual, err := store.GetByID(t.Context(), expected.ID)
	if err != nil {
		t.Fatalf("get feature: %v", err)
	}

	if actual != expected {
		t.Errorf("expected feature %+v, got %+v", expected, actual)
	}
}

func TestFeatureStoreCreateRejectsDuplicateID(t *testing.T) {
	store, projects := newTestFeatureStore(t)
	createFeatureTestProject(t, projects, "prj_test")

	original := feature.Feature{
		ID:          "fea_same",
		ProjectID:   "prj_test",
		Title:       "Original",
		Description: "Original description",
		State:       feature.StateDraft,
		CreatedAt:   time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
	}

	if err := store.Create(t.Context(), original); err != nil {
		t.Fatalf("create original feature: %v", err)
	}

	duplicate := original
	duplicate.Title = "Replacement"

	err := store.Create(t.Context(), duplicate)
	if !errors.Is(err, feature.ErrAlreadyExists) {
		t.Fatalf(
			"expected error %v, got %v",
			feature.ErrAlreadyExists,
			err,
		)
	}

	stored, err := store.GetByID(t.Context(), original.ID)
	if err != nil {
		t.Fatalf("get original feature: %v", err)
	}

	if stored != original {
		t.Errorf(
			"expected duplicate create to preserve %+v, got %+v",
			original,
			stored,
		)
	}
}

func TestFeatureStoreCreateRejectsUnknownProject(t *testing.T) {
	store, _ := newTestFeatureStore(t)
	createdFeature := feature.Feature{
		ID:        "fea_orphan",
		ProjectID: "prj_missing",
		Title:     "Orphan",
		State:     feature.StateDraft,
		CreatedAt: time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
	}

	if err := store.Create(t.Context(), createdFeature); err == nil {
		t.Fatal("expected unknown project to be rejected")
	}

	_, err := store.GetByID(t.Context(), createdFeature.ID)
	if !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf(
			"expected rejected feature to remain absent, got %v",
			err,
		)
	}
}

func TestFeatureStoreGetByIDReturnsNotFound(t *testing.T) {
	store, _ := newTestFeatureStore(t)

	_, err := store.GetByID(t.Context(), "fea_missing")

	if !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf(
			"expected error %v, got %v",
			feature.ErrNotFound,
			err,
		)
	}
}

func TestFeatureStoreHonorsCanceledContext(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		store, _ := newTestFeatureStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := store.Create(ctx, feature.Feature{
			ID:        "fea_canceled",
			ProjectID: "prj_test",
			Title:     "Canceled",
			State:     feature.StateDraft,
			CreatedAt: time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatalf(
				"expected context cancellation, got %v",
				err,
			)
		}
	})

	t.Run("get by ID", func(t *testing.T) {
		store, _ := newTestFeatureStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := store.GetByID(ctx, "fea_test")

		if !errors.Is(err, context.Canceled) {
			t.Fatalf(
				"expected context cancellation, got %v",
				err,
			)
		}
	})
}

func newTestFeatureStore(t *testing.T) (*FeatureStore, *ProjectStore) {
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

	return NewFeatureStore(db), NewProjectStore(db)
}

func createFeatureTestProject(
	t *testing.T,
	store *ProjectStore,
	id string,
) {
	t.Helper()

	if err := store.Create(t.Context(), project.Project{
		ID:        id,
		Name:      "Feature test project",
		CreatedAt: time.Date(2026, time.September, 8, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("create parent project: %v", err)
	}
}
