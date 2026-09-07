package project

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCreateRejectsDuplicateID(t *testing.T) {
	memoryStore := NewMemoryStore()

	project := Project{
		ID:        "prj_same",
		Name:      "Test project",
		CreatedAt: time.Now(),
	}

	if err := memoryStore.Create(t.Context(), project); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err := memoryStore.Create(t.Context(), project)

	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected error %v, got %v", ErrAlreadyExists, err)
	}
}

func TestMemoryStoreGetByID(t *testing.T) {
	store := NewMemoryStore()

	expected := Project{
		ID:        "prj_test",
		Name:      "Test project",
		CreatedAt: time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC),
	}

	if err := store.Create(t.Context(), expected); err != nil {
		t.Fatalf("create project: %v", err)
	}

	actual, err := store.GetByID(t.Context(), expected.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	if actual != expected {
		t.Errorf("expected project %+v, got %+v", expected, actual)
	}
}

func TestMemoryStoreGetByIDReturnsNotFound(t *testing.T) {
	store := NewMemoryStore()

	_, err := store.GetByID(t.Context(), "prj_missing")

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected error %v, got %v", ErrNotFound, err)
	}
}
