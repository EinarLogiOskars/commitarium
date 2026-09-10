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
		ID:             "prj_test",
		Name:           "Test project",
		RecoveryPolicy: RecoveryPolicyApprovalRequired,
		CreatedAt:      time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC),
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

func TestMemoryStoreListsAndBindsForgejoRepository(t *testing.T) {
	store := NewMemoryStore()
	createdAt := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	for _, value := range []Project{
		{ID: "prj_b", Name: "B", CreatedAt: createdAt},
		{ID: "prj_a", Name: "A", CreatedAt: createdAt},
	} {
		if err := store.Create(t.Context(), value); err != nil {
			t.Fatalf("create project: %v", err)
		}
	}
	projects, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 2 || projects[0].ID != "prj_a" || projects[1].ID != "prj_b" {
		t.Fatalf("unexpected projects %+v", projects)
	}
	repository := ForgejoRepository{
		Owner: "owner", Name: "repository", DefaultBranch: "main",
		BoundAt: time.Date(2026, time.September, 9, 13, 0, 0, 0, time.UTC),
	}
	bound, err := store.BindForgejoRepository(t.Context(), "prj_a", repository)
	if err != nil {
		t.Fatalf("bind repository: %v", err)
	}
	if bound.ForgejoRepository == nil || *bound.ForgejoRepository != repository {
		t.Fatalf("unexpected binding %+v", bound)
	}
	bound.ForgejoRepository.Name = "mutated copy"
	loaded, err := store.GetByID(t.Context(), "prj_a")
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if loaded.ForgejoRepository == nil || loaded.ForgejoRepository.Name != repository.Name {
		t.Fatal("caller mutated repository stored in memory")
	}
	_, err = store.BindForgejoRepository(t.Context(), "prj_a", ForgejoRepository{
		Owner: "owner", Name: "other", DefaultBranch: "main", BoundAt: repository.BoundAt,
	})
	if !errors.Is(err, ErrForgejoRepositoryAlreadyBound) {
		t.Fatalf("expected %v, got %v", ErrForgejoRepositoryAlreadyBound, err)
	}
}

func TestMemoryStoreUpdatesDialogueLimits(t *testing.T) {
	store := NewMemoryStore()
	created := Project{
		ID: "prj_test", Name: "Test project",
		DialogueLimits: DefaultDialogueLimits(), CreatedAt: time.Now(),
	}
	if err := store.Create(t.Context(), created); err != nil {
		t.Fatalf("create project: %v", err)
	}
	limits := DialogueLimits{PlanningRounds: 0, ImplementationReviewRounds: 4}
	updated, err := store.UpdateDialogueLimits(t.Context(), created.ID, limits)
	if err != nil {
		t.Fatalf("update dialogue limits: %v", err)
	}
	if updated.DialogueLimits != limits {
		t.Fatalf("unexpected limits %+v", updated.DialogueLimits)
	}
	if _, err := store.UpdateDialogueLimits(t.Context(), "prj_missing", limits); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected %v, got %v", ErrNotFound, err)
	}
}
