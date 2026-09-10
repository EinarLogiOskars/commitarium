package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func TestWorkspacePublicationStorePersistsAndCompletesReceipt(t *testing.T) {
	workspaceStore := newTestWorkspaceStore(t)
	now := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	reservation := workspace.Workspace{
		ID: "wsp_fea_test", ProjectID: "prj_test", FeatureID: "fea_test",
		RepositoryOwner: "owner", RepositoryName: "repository",
		BaseBranch: "main", Branch: "commitarium/fea_test",
		BaseCommitID: workspaceTestCommitID, Status: workspace.StatusPreparing,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := workspaceStore.Reserve(t.Context(), reservation); err != nil {
		t.Fatalf("reserve workspace: %v", err)
	}
	run := execution.Run{
		ID: "run_publication", FeatureID: "fea_test", Status: execution.RunStatusWaitingForUser,
		StartedAt: now, UpdatedAt: now,
	}
	if err := NewExecutionStore(workspaceStore.db).CreateRun(t.Context(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	store := NewWorkspacePublicationStore(workspaceStore.db)
	candidate := workspace.Publication{
		ID: "pub_one", RunID: run.ID, WorkspaceID: reservation.ID,
		IdempotencyKey: "commit-one", CommitMessage: "feat: publish implementation",
		RemoteCommitIDBefore: workspaceTestCommitID,
		LocalCommitIDBefore:  workspaceTestCommitID,
		CommitID:             "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Status:               workspace.PublicationStatusPrepared, CreatedAt: now,
	}
	stored, created, err := store.ReservePublication(t.Context(), candidate)
	if err != nil || !created || stored != candidate {
		t.Fatalf("reserve publication: created=%t stored=%+v err=%v", created, stored, err)
	}
	replayed, created, err := store.ReservePublication(t.Context(), candidate)
	if err != nil || created || replayed != candidate {
		t.Fatalf("replay publication: created=%t stored=%+v err=%v", created, replayed, err)
	}
	completedAt := now.Add(time.Minute)
	completed, err := store.CompletePublication(t.Context(), candidate.ID, completedAt)
	if err != nil || completed.Status != workspace.PublicationStatusCompleted ||
		completed.CompletedAt == nil || !completed.CompletedAt.Equal(completedAt) {
		t.Fatalf("complete publication: stored=%+v err=%v", completed, err)
	}
	if _, err := store.ActivePublication(t.Context(), reservation.ID); !errors.Is(err, workspace.ErrPublicationNotFound) {
		t.Fatalf("expected no active publication, got %v", err)
	}
}

func TestWorkspacePublicationStoreFencesDifferentPreparedPublication(t *testing.T) {
	workspaceStore := newTestWorkspaceStore(t)
	now := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	reservation := workspace.Workspace{
		ID: "wsp_fea_test", ProjectID: "prj_test", FeatureID: "fea_test",
		RepositoryOwner: "owner", RepositoryName: "repository",
		BaseBranch: "main", Branch: "commitarium/fea_test",
		BaseCommitID: workspaceTestCommitID, Status: workspace.StatusPreparing,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := workspaceStore.Reserve(t.Context(), reservation); err != nil {
		t.Fatalf("reserve workspace: %v", err)
	}
	executions := NewExecutionStore(workspaceStore.db)
	for _, runID := range []string{"run_one", "run_two"} {
		if err := executions.CreateRun(t.Context(), execution.Run{
			ID: runID, FeatureID: "fea_test", Status: execution.RunStatusWaitingForUser,
			StartedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("create run %s: %v", runID, err)
		}
	}
	store := NewWorkspacePublicationStore(workspaceStore.db)
	first := workspace.Publication{
		ID: "pub_one", RunID: "run_one", WorkspaceID: reservation.ID,
		IdempotencyKey: "one", CommitMessage: "feat: one",
		RemoteCommitIDBefore: workspaceTestCommitID,
		LocalCommitIDBefore:  workspaceTestCommitID,
		CommitID:             "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Status:               workspace.PublicationStatusPrepared, CreatedAt: now,
	}
	if _, _, err := store.ReservePublication(t.Context(), first); err != nil {
		t.Fatalf("reserve first publication: %v", err)
	}
	second := first
	second.ID, second.RunID, second.IdempotencyKey = "pub_two", "run_two", "two"
	if _, _, err := store.ReservePublication(t.Context(), second); !errors.Is(err, workspace.ErrPublicationConflict) {
		t.Fatalf("expected prepared publication fence, got %v", err)
	}
}
