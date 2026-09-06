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
