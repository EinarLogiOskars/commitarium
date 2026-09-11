package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

func TestProjectStoreCreateAndGetByID(t *testing.T) {
	store := newTestProjectStore(t)
	expected := project.Project{
		ID:             "prj_test",
		Name:           "Commitarium",
		RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		MergePolicy:    project.DefaultMergePolicy(),
		DialogueLimits: project.DialogueLimits{PlanningRounds: 0, ImplementationReviewRounds: 4},
		AgentProviders: project.DefaultAgentProviders(),
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

func TestProjectStoreUpdatesDialogueLimits(t *testing.T) {
	store := newTestProjectStore(t)
	created := project.Project{
		ID: "prj_test", Name: "Commitarium",
		RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		MergePolicy:    project.DefaultMergePolicy(),
		AgentProviders: project.DefaultAgentProviders(),
		DialogueLimits: project.DefaultDialogueLimits(),
		CreatedAt:      time.Now().UTC(),
	}
	if err := store.Create(t.Context(), created); err != nil {
		t.Fatalf("create project: %v", err)
	}
	limits := project.DialogueLimits{PlanningRounds: 2, ImplementationReviewRounds: 0}
	updated, err := store.UpdateDialogueLimits(t.Context(), created.ID, limits)
	if err != nil {
		t.Fatalf("update dialogue limits: %v", err)
	}
	if updated.DialogueLimits != limits {
		t.Fatalf("unexpected updated project %+v", updated)
	}
	repeated, err := store.UpdateDialogueLimits(t.Context(), created.ID, limits)
	if err != nil || repeated.DialogueLimits != limits {
		t.Fatalf("repeat update was not idempotent: project=%+v err=%v", repeated, err)
	}
	reloaded, err := store.GetByID(t.Context(), created.ID)
	if err != nil || reloaded.DialogueLimits != limits {
		t.Fatalf("dialogue limits did not persist: project=%+v err=%v", reloaded, err)
	}
	if _, err := store.UpdateDialogueLimits(t.Context(), "prj_missing", limits); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("expected %v, got %v", project.ErrNotFound, err)
	}
}

func TestProjectStoreUpdatesAgentProviders(t *testing.T) {
	store := newTestProjectStore(t)
	created := project.Project{
		ID: "prj_agents", Name: "Agent choices", CreatedAt: time.Now().UTC(),
		AgentProviders: project.DefaultAgentProviders(),
	}
	if err := store.Create(t.Context(), created); err != nil {
		t.Fatalf("create project: %v", err)
	}
	want := project.AgentProviders{
		Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex,
	}
	updated, err := store.UpdateAgentProviders(t.Context(), created.ID, want)
	if err != nil {
		t.Fatalf("update agent providers: %v", err)
	}
	if updated.AgentProviders != want {
		t.Fatalf("agent providers = %+v, want %+v", updated.AgentProviders, want)
	}
	if _, err := store.UpdateAgentProviders(t.Context(), "prj_missing", want); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("missing project error = %v", err)
	}
}

func TestProjectStoreUpdatesMergePolicy(t *testing.T) {
	store := newTestProjectStore(t)
	created := project.Project{
		ID: "prj_merge", Name: "Merge policy", CreatedAt: time.Now().UTC(),
		MergePolicy: project.MergePolicyRequireUserApproval,
	}
	if err := store.Create(t.Context(), created); err != nil {
		t.Fatalf("create project: %v", err)
	}
	updated, err := store.UpdateMergePolicy(
		t.Context(), created.ID, project.MergePolicyAutoAfterGates,
	)
	if err != nil || updated.MergePolicy != project.MergePolicyAutoAfterGates {
		t.Fatalf("update merge policy: project=%+v err=%v", updated, err)
	}
	reloaded, err := store.GetByID(t.Context(), created.ID)
	if err != nil || reloaded.MergePolicy != project.MergePolicyAutoAfterGates {
		t.Fatalf("reload merge policy: project=%+v err=%v", reloaded, err)
	}
	if _, err := store.UpdateMergePolicy(
		t.Context(), "prj_missing", project.MergePolicyAutoAfterGates,
	); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("missing project error = %v", err)
	}
}

func TestProjectStoreCreateRejectsDuplicateID(t *testing.T) {
	store := newTestProjectStore(t)
	original := project.Project{
		ID:             "prj_same",
		Name:           "Original",
		RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		MergePolicy:    project.DefaultMergePolicy(),
		AgentProviders: project.DefaultAgentProviders(),
		CreatedAt:      time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
	}

	if err := store.Create(t.Context(), original); err != nil {
		t.Fatalf("create original project: %v", err)
	}

	duplicate := original
	duplicate.Name = "Replacement"

	err := store.Create(t.Context(), duplicate)
	if !errors.Is(err, project.ErrAlreadyExists) {
		t.Fatalf(
			"expected error %v, got %v",
			project.ErrAlreadyExists,
			err,
		)
	}

	stored, err := store.GetByID(t.Context(), original.ID)
	if err != nil {
		t.Fatalf("get original project: %v", err)
	}

	if stored != original {
		t.Errorf(
			"expected duplicate create to preserve %+v, got %+v",
			original,
			stored,
		)
	}
}

func TestProjectStoreGetByIDReturnsNotFound(t *testing.T) {
	store := newTestProjectStore(t)

	_, err := store.GetByID(t.Context(), "prj_missing")

	if !errors.Is(err, project.ErrNotFound) {
		t.Fatalf(
			"expected error %v, got %v",
			project.ErrNotFound,
			err,
		)
	}
}

func TestProjectStoreListsProjectsInCreationOrder(t *testing.T) {
	store := newTestProjectStore(t)
	for _, created := range []project.Project{
		{ID: "prj_later", Name: "Later", CreatedAt: time.Date(2026, time.September, 9, 12, 0, 1, 0, time.UTC)},
		{ID: "prj_first_b", Name: "First B", CreatedAt: time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)},
		{ID: "prj_first_a", Name: "First A", CreatedAt: time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)},
	} {
		if err := store.Create(t.Context(), created); err != nil {
			t.Fatalf("create project %q: %v", created.ID, err)
		}
	}
	projects, err := store.List(t.Context())
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	wantIDs := []string{"prj_first_a", "prj_first_b", "prj_later"}
	if len(projects) != len(wantIDs) {
		t.Fatalf("expected %d projects, got %+v", len(wantIDs), projects)
	}
	for index, wantID := range wantIDs {
		if projects[index].ID != wantID {
			t.Errorf("project %d: expected %q, got %q", index, wantID, projects[index].ID)
		}
	}
}

func TestProjectStoreBindsForgejoRepository(t *testing.T) {
	store := newTestProjectStore(t)
	created := project.Project{
		ID: "prj_test", Name: "Commitarium",
		CreatedAt: time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Create(t.Context(), created); err != nil {
		t.Fatalf("create project: %v", err)
	}
	repository := project.ForgejoRepository{
		Owner: "commitarium", Name: "application", DefaultBranch: "main",
		BoundAt: time.Date(2026, time.September, 9, 13, 0, 0, 123, time.UTC),
	}

	bound, err := store.BindForgejoRepository(t.Context(), created.ID, repository)
	if err != nil {
		t.Fatalf("bind repository: %v", err)
	}
	if bound.ForgejoRepository == nil || *bound.ForgejoRepository != repository {
		t.Fatalf("unexpected bound project %+v", bound)
	}
	loaded, err := store.GetByID(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if loaded.ForgejoRepository == nil || *loaded.ForgejoRepository != repository {
		t.Fatalf("repository binding did not persist: %+v", loaded)
	}

	repeated, err := store.BindForgejoRepository(t.Context(), created.ID, project.ForgejoRepository{
		Owner: repository.Owner, Name: repository.Name, DefaultBranch: "different",
		BoundAt: repository.BoundAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("repeat repository binding: %v", err)
	}
	if repeated.ForgejoRepository == nil || *repeated.ForgejoRepository != repository {
		t.Fatalf("repeat binding changed stored metadata: %+v", repeated)
	}

	_, err = store.BindForgejoRepository(t.Context(), created.ID, project.ForgejoRepository{
		Owner: "commitarium", Name: "different", DefaultBranch: "main", BoundAt: repository.BoundAt,
	})
	if !errors.Is(err, project.ErrForgejoRepositoryAlreadyBound) {
		t.Fatalf("expected %v, got %v", project.ErrForgejoRepositoryAlreadyBound, err)
	}
}

func TestProjectStoreBindForgejoRepositoryReturnsNotFound(t *testing.T) {
	store := newTestProjectStore(t)
	_, err := store.BindForgejoRepository(t.Context(), "prj_missing", project.ForgejoRepository{
		Owner: "owner", Name: "repository", DefaultBranch: "main", BoundAt: time.Now().UTC(),
	})
	if !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("expected %v, got %v", project.ErrNotFound, err)
	}
}

func TestProjectStoreHonorsCanceledContext(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		store := newTestProjectStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err := store.Create(ctx, project.Project{
			ID:        "prj_canceled",
			Name:      "Canceled",
			CreatedAt: time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC),
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatalf(
				"expected context cancellation, got %v",
				err,
			)
		}
	})

	t.Run("get by ID", func(t *testing.T) {
		store := newTestProjectStore(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := store.GetByID(ctx, "prj_test")

		if !errors.Is(err, context.Canceled) {
			t.Fatalf(
				"expected context cancellation, got %v",
				err,
			)
		}
	})
}

func newTestProjectStore(t *testing.T) *ProjectStore {
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

	return NewProjectStore(db)
}
