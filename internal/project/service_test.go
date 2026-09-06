package project

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordingStore struct {
	createdProject Project
	err            error
}

func (s *recordingStore) Create(
	_ context.Context,
	project Project,
) error {
	s.createdProject = project
	return s.err
}

func TestServiceCreate(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 6, 12, 0, 0, 0, time.UTC)
	store := &recordingStore{}

	service := &Service{
		store: store,
		generateID: func() string {
			return "prj_test"
		},
		now: func() time.Time {
			return fixedTime
		},
	}

	project, err := service.Create(t.Context(), "   Commitarium   ")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if project.ID != "prj_test" {
		t.Errorf("expected ID %q, got %q", "prj_test", project.ID)
	}

	if project.Name != "Commitarium" {
		t.Errorf("expected trimmed name %q, got %q", "Commitarium", project.Name)
	}

	if !project.CreatedAt.Equal(fixedTime) {
		t.Errorf("expected time %v, got %v", fixedTime, project.CreatedAt)
	}

	if store.createdProject != project {
		t.Errorf(
			"expected stored project %+v, got %+v",
			project,
			store.createdProject,
		)
	}
}

func TestServiceCreateRejectsBlankName(t *testing.T) {
	store := &recordingStore{}
	service := NewService(store)

	_, err := service.Create(t.Context(), " ")

	if !errors.Is(err, ErrNameRequired) {
		t.Fatalf("expected error %v, got %v", ErrNameRequired, err)
	}
}

func TestServiceCreateReturnsStoreError(t *testing.T) {
	storeError := errors.New("storage failed")

	store := &recordingStore{
		err: storeError,
	}

	service := NewService(store)

	project, err := service.Create(t.Context(), "Commitarium")

	if !errors.Is(err, storeError) {
		t.Fatalf("expected error %v, got %v", storeError, err)
	}

	if project != (Project{}) {
		t.Errorf("expected empty project, got %+v", project)
	}
}
