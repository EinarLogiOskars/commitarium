package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type publicationMemoryStore struct {
	stored Publication
}

func (store *publicationMemoryStore) GetPublication(_ context.Context, runID, key string) (Publication, error) {
	if store.stored.ID == "" || store.stored.RunID != runID || store.stored.IdempotencyKey != key {
		return Publication{}, ErrPublicationNotFound
	}
	return store.stored, nil
}

func TestServiceAdoptsExactPreviouslyPushedCommit(t *testing.T) {
	now := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	prReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 7
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/7"
	stored.PullRequestRecordedAt = &prReadyAt
	stored.UpdatedAt = prReadyAt
	implementation := acceptedTestFeature(now)
	implementation.State = feature.StateImplementing
	commitID := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	prepared := Publication{
		ID: "pub_existing", RunID: "run_one", WorkspaceID: stored.ID,
		IdempotencyKey: "commit-one", CommitMessage: "feat: publish implementation",
		RemoteCommitIDBefore: stored.BaseCommitID,
		LocalCommitIDBefore:  stored.BaseCommitID, CommitID: commitID,
		Status: PublicationStatusPrepared, CreatedAt: now,
	}
	branches := &publicationBranches{results: []Branch{
		{Name: stored.Branch, CommitID: commitID},
	}}
	checkout := &publicationCheckout{}
	pullRequests := &publicationPullRequests{revision: PullRequest{
		Number: 7, URL: stored.PullRequestURL, Title: "WIP: Test", Body: "revision",
		State: "open", Draft: true, BaseBranch: stored.BaseBranch,
		HeadBranch: stored.Branch, HeadCommitID: commitID, CreatedAt: now,
	}}
	publications := &publicationMemoryStore{stored: prepared}
	service := NewServiceWithPublication(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: implementation},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, branches, checkout, pullRequests, publications, checkout,
	)
	service.now = func() time.Time { return now.Add(time.Minute) }

	publication, created, err := service.PublishImplementation(
		t.Context(), "prj_test", "fea_test", "sev_plan", "agreed plan",
		"run_one", "commit-one", "feat: publish implementation",
	)
	if err != nil || created || publication.Status != PublicationStatusCompleted {
		t.Fatalf("adopt pushed implementation: created=%t publication=%+v err=%v", created, publication, err)
	}
	if checkout.applied != 1 || checkout.pushed != 0 || pullRequests.calls != 1 {
		t.Fatalf("adoption repeated side effects: checkout=%+v pr_calls=%d", checkout, pullRequests.calls)
	}
}

func (store *publicationMemoryStore) ActivePublication(context.Context, string) (Publication, error) {
	if store.stored.Status == PublicationStatusPrepared {
		return store.stored, nil
	}
	return Publication{}, ErrPublicationNotFound
}

func (store *publicationMemoryStore) ReservePublication(
	_ context.Context,
	candidate Publication,
) (Publication, bool, error) {
	if store.stored.ID != "" {
		return store.stored, false, nil
	}
	store.stored = candidate
	return candidate, true, nil
}

func (store *publicationMemoryStore) CompletePublication(
	_ context.Context,
	_ string,
	completedAt time.Time,
) (Publication, error) {
	store.stored.Status = PublicationStatusCompleted
	store.stored.CompletedAt = &completedAt
	return store.stored, nil
}

type publicationCheckout struct {
	snapshot CommitSnapshot
	prepared int
	applied  int
	pushed   int
}

func (checkout *publicationCheckout) Ensure(context.Context, CheckoutSpec) error { return nil }

func (checkout *publicationCheckout) PreparePublication(
	context.Context,
	CheckoutSpec,
	string,
	time.Time,
) (CommitSnapshot, error) {
	checkout.prepared++
	return checkout.snapshot, nil
}

func (checkout *publicationCheckout) ApplyPublication(context.Context, CheckoutSpec, CommitSnapshot) error {
	checkout.applied++
	return nil
}

func (checkout *publicationCheckout) PushPublication(context.Context, CheckoutSpec, string) error {
	checkout.pushed++
	return nil
}

type publicationBranches struct {
	results []Branch
	calls   int
}

func (branches *publicationBranches) GetBranch(context.Context, string, string, string) (Branch, error) {
	if branches.calls >= len(branches.results) {
		return Branch{}, errors.New("unexpected branch inspection")
	}
	result := branches.results[branches.calls]
	branches.calls++
	return result, nil
}

func (*publicationBranches) EnsureBranch(context.Context, string, string, string, string) (Branch, error) {
	return Branch{}, errors.New("unexpected branch creation")
}

type publicationPullRequests struct {
	plan     PullRequest
	revision PullRequest
	calls    int
}

func (*publicationPullRequests) EnsureDraftPullRequest(context.Context, string, string, PullRequestSpec) (PullRequest, error) {
	return PullRequest{}, errors.New("unexpected pull request creation")
}

func (*publicationPullRequests) EnsurePullRequestPlan(context.Context, string, string, PlanPublicationSpec) (PullRequest, bool, error) {
	return PullRequest{}, false, errors.New("unexpected plan publication")
}

func (pullRequests *publicationPullRequests) VerifyPullRequestPlan(context.Context, string, string, PlanPublicationSpec) (PullRequest, error) {
	return pullRequests.plan, nil
}

func (pullRequests *publicationPullRequests) EnsurePullRequestRevision(
	_ context.Context,
	_ string,
	_ string,
	spec RevisionPublicationSpec,
) (PullRequest, bool, error) {
	pullRequests.calls++
	if spec.CommitID != pullRequests.revision.HeadCommitID {
		return PullRequest{}, false, ErrPullRequestConflict
	}
	return pullRequests.revision, true, nil
}

func TestServicePublishesExactImplementationOnceAndReplaysReceipt(t *testing.T) {
	now := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	prReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 7
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/7"
	stored.PullRequestRecordedAt = &prReadyAt
	stored.UpdatedAt = prReadyAt
	implementation := acceptedTestFeature(now)
	implementation.State = feature.StateImplementing
	commitID := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	branches := &publicationBranches{results: []Branch{
		{Name: stored.Branch, CommitID: stored.BaseCommitID},
		{Name: stored.Branch, CommitID: stored.BaseCommitID},
		{Name: stored.Branch, CommitID: stored.BaseCommitID},
		{Name: stored.Branch, CommitID: commitID},
	}}
	checkout := &publicationCheckout{snapshot: CommitSnapshot{
		LocalCommitIDBefore: stored.BaseCommitID, CommitID: commitID,
	}}
	pullRequests := &publicationPullRequests{
		plan: PullRequest{
			Number: 7, URL: stored.PullRequestURL, Title: "WIP: Test", Body: "plan",
			State: "open", Draft: true, BaseBranch: stored.BaseBranch,
			HeadBranch: stored.Branch, HeadCommitID: stored.BaseCommitID, CreatedAt: now,
		},
		revision: PullRequest{
			Number: 7, URL: stored.PullRequestURL, Title: "WIP: Test", Body: "revision",
			State: "open", Draft: true, BaseBranch: stored.BaseBranch,
			HeadBranch: stored.Branch, HeadCommitID: commitID, CreatedAt: now,
		},
	}
	publications := &publicationMemoryStore{}
	service := NewServiceWithPublication(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: implementation},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, branches, checkout, pullRequests, publications, checkout,
	)
	service.now = func() time.Time { return now.Add(4 * time.Minute) }

	published, created, err := service.PublishImplementation(
		t.Context(), "prj_test", "fea_test", "sev_plan", "agreed plan",
		"run_one", "commit-one", "feat: publish implementation",
	)
	if err != nil || !created || published.Status != PublicationStatusCompleted ||
		published.CommitID != commitID {
		t.Fatalf("publish implementation: created=%t publication=%+v err=%v", created, published, err)
	}
	replayed, created, err := service.PublishImplementation(
		t.Context(), "prj_test", "fea_test", "sev_plan", "agreed plan",
		"run_one", "commit-one", "feat: publish implementation",
	)
	if err != nil || created || replayed.ID != published.ID {
		t.Fatalf("replay implementation: created=%t publication=%+v err=%v", created, replayed, err)
	}
	if checkout.prepared != 1 || checkout.applied != 1 || checkout.pushed != 1 ||
		pullRequests.calls != 1 {
		t.Fatalf("retry repeated side effects: checkout=%+v pr_calls=%d", checkout, pullRequests.calls)
	}
}
