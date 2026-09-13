package workorder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type deletionStoreStub struct {
	deletion workorderDeletionAlias
	err      error
	finished bool
}

type workorderDeletionAlias = Deletion

func (stub *deletionStoreStub) BeginDeletion(context.Context, string, string) (Deletion, error) {
	return stub.deletion, stub.err
}

func (stub *deletionStoreStub) FinishDeletion(context.Context, string, string) error {
	stub.finished = true
	return stub.err
}

type forgejoCleanerStub struct {
	closed        int
	deleted       int
	deletedBranch string
}

func (stub *forgejoCleanerStub) ClosePullRequest(context.Context, string, string, int64, string, string, string) error {
	stub.closed++
	return nil
}

func (stub *forgejoCleanerStub) DeleteBranch(_ context.Context, _, _, branch string) error {
	stub.deleted++
	stub.deletedBranch = branch
	return nil
}

type checkoutCleanerStub struct{ removed []string }

func (stub *checkoutCleanerStub) Remove(_ context.Context, workspaceID string) error {
	stub.removed = append(stub.removed, workspaceID)
	return nil
}

func TestDeleteRemovesOnlyIsolatedUnmergedArtifacts(t *testing.T) {
	deletion := testDeletion(feature.StateReviewing)
	store := &deletionStoreStub{deletion: deletion}
	forgejo := &forgejoCleanerStub{}
	checkouts := &checkoutCleanerStub{}
	result, err := NewService(store, forgejo, checkouts).Delete(
		t.Context(), deletion.Feature.ProjectID, deletion.Feature.ID,
	)
	if err != nil {
		t.Fatalf("delete work order: %v", err)
	}
	if !result.Deleted || result.MergedChangesRemain || !store.finished || forgejo.closed != 1 || forgejo.deleted != 1 ||
		forgejo.deletedBranch != "commitarium/fea_test" || len(checkouts.removed) != 1 {
		t.Fatalf("unexpected deletion result=%+v store=%+v forgejo=%+v checkouts=%+v", result, store, forgejo, checkouts)
	}
}

func TestDeleteCompletedWorkOrderKeepsMergedChangesAndSkipsPRMutation(t *testing.T) {
	deletion := testDeletion(feature.StateCompleted)
	mergedAt := deletion.Workspace.UpdatedAt
	deletion.Workspace.ApprovedCommitID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	deletion.Workspace.MergeReadyAt = deletion.Workspace.PullRequestRecordedAt
	deletion.Workspace.MergeCommitID = "cccccccccccccccccccccccccccccccccccccccc"
	deletion.Workspace.MergedAt = &mergedAt
	store := &deletionStoreStub{deletion: deletion}
	forgejo := &forgejoCleanerStub{}
	result, err := NewService(store, forgejo, &checkoutCleanerStub{}).Delete(
		t.Context(), deletion.Feature.ProjectID, deletion.Feature.ID,
	)
	if err != nil {
		t.Fatalf("delete completed work order: %v", err)
	}
	if !result.Deleted || !result.MergedChangesRemain || forgejo.closed != 0 || forgejo.deleted != 1 ||
		forgejo.deletedBranch == deletion.Workspace.BaseBranch {
		t.Fatalf("completed deletion touched wrong artifacts: result=%+v forgejo=%+v", result, forgejo)
	}
}

func TestDeleteRejectsActiveOrDefaultBranchArtifact(t *testing.T) {
	active := &deletionStoreStub{err: ErrActive}
	if _, err := NewService(active, &forgejoCleanerStub{}, &checkoutCleanerStub{}).Delete(
		t.Context(), "prj_test", "fea_test",
	); !errors.Is(err, ErrActive) {
		t.Fatalf("expected active error, got %v", err)
	}

	deletion := testDeletion(feature.StatePlanning)
	deletion.Workspace.Branch = deletion.Workspace.BaseBranch
	store := &deletionStoreStub{deletion: deletion}
	forgejo := &forgejoCleanerStub{}
	if _, err := NewService(store, forgejo, &checkoutCleanerStub{}).Delete(
		t.Context(), "prj_test", "fea_test",
	); !errors.Is(err, ErrUnsafeArtifacts) {
		t.Fatalf("expected unsafe-artifact error, got %v", err)
	}
	if forgejo.closed != 0 || forgejo.deleted != 0 || store.finished {
		t.Fatalf("unsafe deletion mutated artifacts: store=%+v forgejo=%+v", store, forgejo)
	}
}

func testDeletion(state feature.State) Deletion {
	created := time.Date(2026, time.September, 13, 10, 0, 0, 0, time.UTC)
	checkoutAt := created.Add(time.Second)
	branchAt := created.Add(2 * time.Second)
	prAt := created.Add(3 * time.Second)
	updated := created.Add(4 * time.Second)
	return Deletion{
		Feature: feature.Feature{ID: "fea_test", ProjectID: "prj_test", State: state},
		Workspace: &workspace.Workspace{
			ID: "wsp_fea_test", ProjectID: "prj_test", FeatureID: "fea_test",
			RepositoryOwner: "owner", RepositoryName: "repository",
			BaseBranch: "main", Branch: "commitarium/fea_test",
			BaseCommitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Status:       workspace.StatusBranchReady, BranchCreatedAt: &branchAt,
			CheckoutRelativePath: "wsp_fea_test", CheckoutCreatedAt: &checkoutAt,
			PullRequestNumber: 7, PullRequestURL: "http://forgejo/owner/repository/pulls/7",
			PullRequestRecordedAt: &prAt, CreatedAt: created, UpdatedAt: updated,
		},
	}
}
