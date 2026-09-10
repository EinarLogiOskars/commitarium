package feature

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCreateAndGetByID(t *testing.T) {
	store := NewMemoryStore()
	expected := Feature{
		ID:          "fea_test",
		ProjectID:   "prj_test",
		Title:       "Durable features",
		Description: "Persist feature data",
		State:       StateDraft,
		CreatedAt:   time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
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

func TestMemoryStoreCreateRejectsDuplicateID(t *testing.T) {
	store := NewMemoryStore()
	createdFeature := Feature{ID: "fea_same"}

	if err := store.Create(t.Context(), createdFeature); err != nil {
		t.Fatalf("create feature: %v", err)
	}

	err := store.Create(t.Context(), createdFeature)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected error %v, got %v", ErrAlreadyExists, err)
	}
}

func TestMemoryStoreGetByIDReturnsNotFound(t *testing.T) {
	store := NewMemoryStore()

	_, err := store.GetByID(t.Context(), "fea_missing")

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected error %v, got %v", ErrNotFound, err)
	}
}

func TestMemoryStoreListsProjectFeaturesByRecentActivity(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	features := []Feature{
		{ID: "fea_older", ProjectID: "prj_test", CreatedAt: now, UpdatedAt: now},
		{ID: "fea_newer", ProjectID: "prj_test", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Hour)},
		{ID: "fea_other", ProjectID: "prj_other", CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Hour)},
	}
	for _, createdFeature := range features {
		if err := store.Create(t.Context(), createdFeature); err != nil {
			t.Fatalf("create feature %q: %v", createdFeature.ID, err)
		}
	}

	listed, err := store.ListByProjectID(t.Context(), "prj_test")
	if err != nil {
		t.Fatalf("list features: %v", err)
	}
	if len(listed) != 2 || listed[0].ID != "fea_newer" || listed[1].ID != "fea_older" {
		t.Errorf("unexpected feature order %+v", listed)
	}
	empty, err := store.ListByProjectID(t.Context(), "prj_empty")
	if err != nil {
		t.Fatalf("list empty project: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("expected non-nil empty list, got %#v", empty)
	}
}
