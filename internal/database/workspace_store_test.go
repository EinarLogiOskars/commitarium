package database

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const workspaceTestCommitID = "0123456789abcdef0123456789abcdef01234567"

func TestWorkspaceStoreReservationAndBranchReadyAreIdempotent(t *testing.T) {
	store := newTestWorkspaceStore(t)
	now := time.Date(2026, time.September, 9, 20, 0, 0, 123, time.UTC)
	reservation := workspace.Workspace{
		ID: "wsp_fea_test", ProjectID: "prj_test", FeatureID: "fea_test",
		RepositoryOwner: "owner", RepositoryName: "repository",
		BaseBranch: "main", Branch: "commitarium/fea_test",
		BaseCommitID: workspaceTestCommitID, Status: workspace.StatusPreparing,
		CreatedAt: now, UpdatedAt: now,
	}

	stored, created, err := store.Reserve(t.Context(), reservation)
	if err != nil || !created || stored != reservation {
		t.Fatalf("reserve workspace: created=%t stored=%+v err=%v", created, stored, err)
	}
	retried := reservation
	retried.CreatedAt = now.Add(time.Hour)
	retried.UpdatedAt = retried.CreatedAt
	stored, created, err = store.Reserve(t.Context(), retried)
	if err != nil || created || stored != reservation {
		t.Fatalf("retry reservation: created=%t stored=%+v err=%v", created, stored, err)
	}

	readyAt := now.Add(time.Minute)
	ready, err := store.MarkBranchReady(t.Context(), reservation.FeatureID, readyAt)
	if err != nil || ready.Status != workspace.StatusBranchReady ||
		ready.BranchCreatedAt == nil || !ready.BranchCreatedAt.Equal(readyAt) {
		t.Fatalf("mark branch ready: workspace=%+v err=%v", ready, err)
	}
	retriedReady, err := store.MarkBranchReady(t.Context(), reservation.FeatureID, now.Add(time.Hour))
	if err != nil || !reflect.DeepEqual(retriedReady, ready) {
		t.Fatalf("retry branch ready: workspace=%+v err=%v", retriedReady, err)
	}
	loaded, err := store.GetByFeatureID(t.Context(), reservation.FeatureID)
	if err != nil || !reflect.DeepEqual(loaded, ready) {
		t.Fatalf("reload ready workspace: workspace=%+v err=%v", loaded, err)
	}
}

func TestWorkspaceStoreRejectsChangedReservation(t *testing.T) {
	store := newTestWorkspaceStore(t)
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	reservation := workspace.Workspace{
		ID: "wsp_fea_test", ProjectID: "prj_test", FeatureID: "fea_test",
		RepositoryOwner: "owner", RepositoryName: "repository",
		BaseBranch: "main", Branch: "commitarium/fea_test",
		BaseCommitID: workspaceTestCommitID, Status: workspace.StatusPreparing,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.Reserve(t.Context(), reservation); err != nil {
		t.Fatalf("reserve workspace: %v", err)
	}
	changed := reservation
	changed.BaseCommitID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, _, err := store.Reserve(t.Context(), changed); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("expected %v, got %v", workspace.ErrConflict, err)
	}
}

func TestWorkspaceStoreReturnsNotFound(t *testing.T) {
	store := newTestWorkspaceStore(t)
	if _, err := store.GetByFeatureID(t.Context(), "fea_missing"); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("expected %v, got %v", workspace.ErrNotFound, err)
	}
	if _, err := store.MarkBranchReady(t.Context(), "fea_missing", time.Now().UTC()); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("expected %v, got %v", workspace.ErrNotFound, err)
	}
}

func newTestWorkspaceStore(t *testing.T) *WorkspaceStore {
	t.Helper()
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "coordinator.db"))
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
	now := time.Date(2026, time.September, 9, 19, 0, 0, 0, time.UTC)
	if err := NewProjectStore(db).Create(t.Context(), project.Project{
		ID: "prj_test", Name: "Workspace test", CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := NewFeatureStore(db).Create(t.Context(), feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", Title: "Workspace test",
		State: feature.StateDraft, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	return NewWorkspaceStore(db)
}
