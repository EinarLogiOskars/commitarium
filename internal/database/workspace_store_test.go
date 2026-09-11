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
const workspaceTestMergeCommitID = "89abcdef0123456789abcdef0123456789abcdef"

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
	checkoutAt := readyAt.Add(time.Minute)
	checkout, err := store.MarkCheckoutReady(
		t.Context(), reservation.FeatureID, reservation.ID, checkoutAt,
	)
	if err != nil || checkout.CheckoutRelativePath != reservation.ID ||
		checkout.CheckoutCreatedAt == nil || !checkout.CheckoutCreatedAt.Equal(checkoutAt) {
		t.Fatalf("mark checkout ready: workspace=%+v err=%v", checkout, err)
	}
	retriedCheckout, err := store.MarkCheckoutReady(
		t.Context(), reservation.FeatureID, reservation.ID, checkoutAt.Add(time.Hour),
	)
	if err != nil || !reflect.DeepEqual(retriedCheckout, checkout) {
		t.Fatalf("retry checkout ready: workspace=%+v err=%v", retriedCheckout, err)
	}
	if _, err := store.MarkCheckoutReady(
		t.Context(), reservation.FeatureID, "wsp_other", checkoutAt,
	); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("expected changed checkout path to conflict, got %v", err)
	}
	pullRequestAt := checkoutAt.Add(time.Minute)
	pullRequestURL := "http://localhost:3001/owner/repository/pulls/7"
	pullRequestReady, err := store.MarkPullRequestReady(
		t.Context(), reservation.FeatureID, 7, pullRequestURL, pullRequestAt,
	)
	if err != nil || pullRequestReady.PullRequestNumber != 7 ||
		pullRequestReady.PullRequestURL != pullRequestURL ||
		pullRequestReady.PullRequestRecordedAt == nil ||
		!pullRequestReady.PullRequestRecordedAt.Equal(pullRequestAt) {
		t.Fatalf("mark pull request ready: workspace=%+v err=%v", pullRequestReady, err)
	}
	retriedPullRequest, err := store.MarkPullRequestReady(
		t.Context(), reservation.FeatureID, 7, pullRequestURL, pullRequestAt.Add(time.Hour),
	)
	if err != nil || !reflect.DeepEqual(retriedPullRequest, pullRequestReady) {
		t.Fatalf("retry pull request ready: workspace=%+v err=%v", retriedPullRequest, err)
	}
	if _, err := store.MarkPullRequestReady(
		t.Context(), reservation.FeatureID, 8,
		"http://localhost:3001/owner/repository/pulls/8", pullRequestAt,
	); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("expected changed pull request identity to conflict, got %v", err)
	}
	loaded, err = store.GetByFeatureID(t.Context(), reservation.FeatureID)
	if err != nil || !reflect.DeepEqual(loaded, pullRequestReady) {
		t.Fatalf("reload pull-request-ready workspace: workspace=%+v err=%v", loaded, err)
	}
	mergeReadyAt := pullRequestAt.Add(time.Minute)
	mergeReady, err := store.MarkMergeReady(
		t.Context(), reservation.FeatureID, workspaceTestCommitID, mergeReadyAt,
	)
	if err != nil || mergeReady.ApprovedCommitID != workspaceTestCommitID ||
		mergeReady.MergeReadyAt == nil || !mergeReady.MergeReadyAt.Equal(mergeReadyAt) {
		t.Fatalf("mark workspace merge ready: workspace=%+v err=%v", mergeReady, err)
	}
	retriedMergeReady, err := store.MarkMergeReady(
		t.Context(), reservation.FeatureID, workspaceTestCommitID, mergeReadyAt.Add(time.Hour),
	)
	if err != nil || !reflect.DeepEqual(retriedMergeReady, mergeReady) {
		t.Fatalf("retry merge readiness: workspace=%+v err=%v", retriedMergeReady, err)
	}
	if _, err := store.MarkMergeReady(
		t.Context(), reservation.FeatureID, workspaceTestMergeCommitID, mergeReadyAt,
	); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("expected changed approved commit to conflict, got %v", err)
	}
	mergedAt := mergeReadyAt.Add(time.Minute)
	merged, err := store.MarkMerged(
		t.Context(), reservation.FeatureID, workspaceTestCommitID,
		workspaceTestMergeCommitID, mergedAt, mergedAt,
	)
	if err != nil || merged.MergeCommitID != workspaceTestMergeCommitID ||
		merged.MergedAt == nil || !merged.MergedAt.Equal(mergedAt) {
		t.Fatalf("mark workspace merged: workspace=%+v err=%v", merged, err)
	}
	retriedMerged, err := store.MarkMerged(
		t.Context(), reservation.FeatureID, workspaceTestCommitID,
		workspaceTestMergeCommitID, mergedAt, mergedAt.Add(time.Hour),
	)
	if err != nil || !reflect.DeepEqual(retriedMerged, merged) {
		t.Fatalf("retry merged result: workspace=%+v err=%v", retriedMerged, err)
	}
	if _, err := store.MarkMerged(
		t.Context(), reservation.FeatureID, workspaceTestCommitID,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", mergedAt, mergedAt,
	); !errors.Is(err, workspace.ErrConflict) {
		t.Fatalf("expected changed merge result to conflict, got %v", err)
	}
	loaded, err = store.GetByFeatureID(t.Context(), reservation.FeatureID)
	if err != nil || !reflect.DeepEqual(loaded, merged) {
		t.Fatalf("reload merged workspace: workspace=%+v err=%v", loaded, err)
	}
}

func TestWorkspaceStorePersistsCheckoutBeforeFeatureBranch(t *testing.T) {
	store := newTestWorkspaceStore(t)
	now := time.Date(2026, time.September, 11, 14, 0, 0, 0, time.UTC)
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
	checkoutAt := now.Add(time.Minute)
	checkedOut, err := store.MarkCheckoutReady(
		t.Context(), reservation.FeatureID, reservation.ID, checkoutAt,
	)
	if err != nil || checkedOut.Status != workspace.StatusPreparing || !checkedOut.CheckoutReady() {
		t.Fatalf("mark early checkout ready: workspace=%+v err=%v", checkedOut, err)
	}
	loaded, err := store.GetByFeatureID(t.Context(), reservation.FeatureID)
	if err != nil || !reflect.DeepEqual(loaded, checkedOut) {
		t.Fatalf("reload early checkout: workspace=%+v err=%v", loaded, err)
	}
	branchAt := checkoutAt.Add(time.Minute)
	ready, err := store.MarkBranchReady(t.Context(), reservation.FeatureID, branchAt)
	if err != nil || ready.Status != workspace.StatusBranchReady ||
		ready.BranchCreatedAt == nil || !ready.BranchCreatedAt.Equal(branchAt) ||
		ready.CheckoutRelativePath != reservation.ID ||
		ready.CheckoutCreatedAt == nil || !ready.CheckoutCreatedAt.Equal(checkoutAt) {
		t.Fatalf("promote stored checkout to branch-ready: workspace=%+v err=%v", ready, err)
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
	if _, err := store.MarkCheckoutReady(
		t.Context(), "fea_missing", "wsp_missing", time.Now().UTC(),
	); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("expected %v, got %v", workspace.ErrNotFound, err)
	}
	if _, err := store.MarkPullRequestReady(
		t.Context(), "fea_missing", 1, "http://localhost/pulls/1", time.Now().UTC(),
	); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("expected %v, got %v", workspace.ErrNotFound, err)
	}
	if _, err := store.MarkMergeReady(
		t.Context(), "fea_missing", workspaceTestCommitID, time.Now().UTC(),
	); !errors.Is(err, workspace.ErrNotFound) {
		t.Fatalf("expected %v, got %v", workspace.ErrNotFound, err)
	}
	if _, err := store.MarkMerged(
		t.Context(), "fea_missing", workspaceTestCommitID,
		workspaceTestMergeCommitID, time.Now().UTC(), time.Now().UTC(),
	); !errors.Is(err, workspace.ErrNotFound) {
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
