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
