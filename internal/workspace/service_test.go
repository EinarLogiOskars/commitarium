package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

const testCommitID = "0123456789abcdef0123456789abcdef01234567"

type memoryStore struct {
	stored              Workspace
	reserve             []Workspace
	markedAt            []time.Time
	checkoutMarkedAt    []time.Time
	pullRequestMarkedAt []time.Time
	getErr              error
	reserveErr          error
}

func (store *memoryStore) MarkPullRequestReady(
	_ context.Context,
	_ string,
	number int64,
	pullRequestURL string,
	readyAt time.Time,
) (Workspace, error) {
	store.pullRequestMarkedAt = append(store.pullRequestMarkedAt, readyAt)
	store.stored.PullRequestNumber = number
	store.stored.PullRequestURL = pullRequestURL
	store.stored.PullRequestRecordedAt = &readyAt
	store.stored.UpdatedAt = readyAt
	return store.stored, nil
}

func (store *memoryStore) MarkCheckoutReady(
	_ context.Context,
	_ string,
	relativePath string,
	readyAt time.Time,
) (Workspace, error) {
	store.checkoutMarkedAt = append(store.checkoutMarkedAt, readyAt)
	store.stored.CheckoutRelativePath = relativePath
	store.stored.CheckoutCreatedAt = &readyAt
	store.stored.UpdatedAt = readyAt
	return store.stored, nil
}

func (store *memoryStore) GetByFeatureID(_ context.Context, _ string) (Workspace, error) {
	if store.stored.ID == "" {
		if store.getErr != nil {
			return Workspace{}, store.getErr
		}
		return Workspace{}, ErrNotFound
	}
	return store.stored, nil
}

func (store *memoryStore) Reserve(_ context.Context, candidate Workspace) (Workspace, bool, error) {
	store.reserve = append(store.reserve, candidate)
	if store.reserveErr != nil {
		return Workspace{}, false, store.reserveErr
	}
	if store.stored.ID != "" {
		return store.stored, false, nil
	}
	store.stored = candidate
	return candidate, true, nil
}

func (store *memoryStore) MarkBranchReady(_ context.Context, _ string, readyAt time.Time) (Workspace, error) {
	store.markedAt = append(store.markedAt, readyAt)
	store.stored.Status = StatusBranchReady
	store.stored.BranchCreatedAt = &readyAt
	store.stored.UpdatedAt = readyAt
	return store.stored, nil
}

type fixedFeatureFinder struct {
	stored feature.Feature
	err    error
}

func (finder fixedFeatureFinder) GetByID(context.Context, string, string) (feature.Feature, error) {
	return finder.stored, finder.err
}

type fixedProjectFinder struct {
	stored project.Project
	err    error
}

func (finder fixedProjectFinder) GetByID(context.Context, string) (project.Project, error) {
	return finder.stored, finder.err
}

type recordingBranches struct {
	base        Branch
	getCalls    int
	ensureCalls int
	ensured     Branch
	ensureErr   error
}

type recordingCheckout struct {
	specs []CheckoutSpec
	err   error
}

type recordingPullRequests struct {
	specs  []PullRequestSpec
	result PullRequest
	err    error
}

func (pullRequests *recordingPullRequests) EnsureDraftPullRequest(
	_ context.Context,
	_ string,
	_ string,
	spec PullRequestSpec,
) (PullRequest, error) {
	pullRequests.specs = append(pullRequests.specs, spec)
	return pullRequests.result, pullRequests.err
}

func (checkout *recordingCheckout) Ensure(_ context.Context, spec CheckoutSpec) error {
	checkout.specs = append(checkout.specs, spec)
	return checkout.err
}

func (branches *recordingBranches) GetBranch(context.Context, string, string, string) (Branch, error) {
	branches.getCalls++
	return branches.base, nil
}

func (branches *recordingBranches) EnsureBranch(
	_ context.Context,
	_ string,
	_ string,
	branch string,
	commitID string,
) (Branch, error) {
	branches.ensureCalls++
	if branches.ensureErr != nil {
		return Branch{}, branches.ensureErr
	}
	if branches.ensured.Name != "" {
		return branches.ensured, nil
	}
	return Branch{Name: branch, CommitID: commitID}, nil
}

func TestServicePreparesExactFeatureBranch(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	branches := &recordingBranches{base: Branch{Name: "main", CommitID: testCommitID}}
	service := newTestService(store, branches, now)

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil {
		t.Fatalf("prepare branch: %v", err)
	}
	if !created || prepared.Status != StatusBranchReady {
		t.Fatalf("unexpected preparation result created=%t workspace=%+v", created, prepared)
	}
	if len(store.reserve) != 1 || store.reserve[0].BaseCommitID != testCommitID ||
		store.reserve[0].Branch != "commitarium/fea_test" {
		t.Fatalf("unexpected durable reservation %+v", store.reserve)
	}
	if branches.getCalls != 1 || branches.ensureCalls != 1 || len(store.markedAt) != 1 {
		t.Fatalf("unexpected calls: branches=%+v marked=%+v", branches, store.markedAt)
	}
}

func TestServiceRetriesPreparingReservationWithoutReadingMovingDefaultBranch(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{base: Branch{
		Name: "main", CommitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	service := newTestService(store, branches, now.Add(time.Minute))

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil {
		t.Fatalf("retry branch preparation: %v", err)
	}
	if created || prepared.Status != StatusBranchReady {
		t.Fatalf("unexpected retry result created=%t workspace=%+v", created, prepared)
	}
	if branches.getCalls != 0 || branches.ensureCalls != 1 {
		t.Fatalf("retry did not use reservation: %+v", branches)
	}
	if prepared.BaseCommitID != testCommitID {
		t.Fatalf("base commit changed to %q", prepared.BaseCommitID)
	}
}

func TestServiceReadyRetryDoesNotCallForgejo(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	stored.Status = StatusBranchReady
	readyAt := now.Add(time.Minute)
	stored.BranchCreatedAt = &readyAt
	stored.UpdatedAt = readyAt
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{}
	service := newTestService(store, branches, readyAt.Add(time.Minute))

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil || created || prepared != stored {
		t.Fatalf("unexpected ready retry result created=%t workspace=%+v err=%v", created, prepared, err)
	}
	if branches.getCalls != 0 || branches.ensureCalls != 0 || len(store.markedAt) != 0 {
		t.Fatalf("ready retry contacted Forgejo or changed storage")
	}
}

func TestServiceCreatesCheckoutAfterBranchIsReady(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	stored.Status = StatusBranchReady
	branchReadyAt := now.Add(time.Minute)
	stored.BranchCreatedAt = &branchReadyAt
	stored.UpdatedAt = branchReadyAt
	store := &memoryStore{stored: stored}
	checkout := &recordingCheckout{}
	service := NewServiceWithCheckout(
		store,
		fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		&recordingBranches{},
		checkout,
	)
	checkoutReadyAt := branchReadyAt.Add(time.Minute)
	service.now = func() time.Time { return checkoutReadyAt }

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil || created || !prepared.CheckoutReady() {
		t.Fatalf("prepare checkout: created=%t workspace=%+v err=%v", created, prepared, err)
	}
	if len(checkout.specs) != 1 || checkout.specs[0].AlreadyReady ||
		checkout.specs[0].WorkspaceID != stored.ID {
		t.Fatalf("unexpected checkout request %+v", checkout.specs)
	}
	if prepared.CheckoutRelativePath != stored.ID || len(store.checkoutMarkedAt) != 1 {
		t.Fatalf("checkout readiness was not persisted: %+v", prepared)
	}
}

func TestServiceReconcilesReadyCheckoutWithoutChangingItsRecord(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	stored.Status = StatusBranchReady
	branchReadyAt := now.Add(time.Minute)
	checkoutReadyAt := branchReadyAt.Add(time.Minute)
	stored.BranchCreatedAt = &branchReadyAt
	stored.CheckoutRelativePath = stored.ID
	stored.CheckoutCreatedAt = &checkoutReadyAt
	stored.UpdatedAt = checkoutReadyAt
	store := &memoryStore{stored: stored}
	checkout := &recordingCheckout{}
	service := NewServiceWithCheckout(
		store,
		fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		&recordingBranches{},
		checkout,
	)

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil || created || prepared != stored {
		t.Fatalf("reconcile checkout: created=%t workspace=%+v err=%v", created, prepared, err)
	}
	if len(checkout.specs) != 1 || !checkout.specs[0].AlreadyReady ||
		len(store.checkoutMarkedAt) != 0 {
		t.Fatalf("ready checkout was not only reconciled: %+v", checkout.specs)
	}
}

func TestServiceCreatesDraftPullRequestAfterCheckoutIsReady(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	store := &memoryStore{stored: stored}
	pullRequest := PullRequest{
		Number: 7, URL: "http://localhost:3001/owner/repository/pulls/7",
		Title: "WIP: Test feature", Body: "<!-- commitarium-feature: fea_test -->",
		State: "open", Draft: true,
		BaseBranch: "main", HeadBranch: "commitarium/fea_test",
		HeadCommitID: testCommitID, CreatedAt: now.Add(3 * time.Minute),
	}
	pullRequests := &recordingPullRequests{result: pullRequest}
	service := NewServiceWithPreparation(
		store,
		fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		&recordingBranches{},
		&recordingCheckout{},
		pullRequests,
	)
	readyAt := now.Add(4 * time.Minute)
	service.now = func() time.Time { return readyAt }

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil || created || !prepared.PullRequestReady() {
		t.Fatalf("prepare pull request: created=%t workspace=%+v err=%v", created, prepared, err)
	}
	if prepared.PullRequestNumber != 7 || prepared.PullRequestURL != pullRequest.URL ||
		prepared.PullRequestRecordedAt == nil || !prepared.PullRequestRecordedAt.Equal(readyAt) {
		t.Fatalf("unexpected pull request readiness %+v", prepared)
	}
	if len(pullRequests.specs) != 1 {
		t.Fatalf("expected one pull request request, got %+v", pullRequests.specs)
	}
	spec := pullRequests.specs[0]
	if spec.Title != "WIP: Test feature" ||
		!strings.Contains(spec.Body, "Ship it") ||
		!strings.Contains(spec.Body, spec.FeatureMarker) || spec.ExistingNumber != 0 {
		t.Fatalf("unexpected pull request request %+v", spec)
	}
}

func TestServiceReconcilesRecordedPullRequest(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 7
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/7"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	store := &memoryStore{stored: stored}
	pullRequests := &recordingPullRequests{result: PullRequest{
		Number: 7, URL: stored.PullRequestURL, Title: "Changed WIP title",
		Body: "<!-- commitarium-feature: fea_test -->", State: "open", Draft: true,
		BaseBranch: "main", HeadBranch: "commitarium/fea_test", HeadCommitID: testCommitID,
		CreatedAt: pullRequestReadyAt,
	}}
	service := NewServiceWithPreparation(
		store,
		fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		&recordingBranches{}, &recordingCheckout{}, pullRequests,
	)

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil || created || prepared != stored {
		t.Fatalf("reconcile pull request: created=%t workspace=%+v err=%v", created, prepared, err)
	}
	if len(pullRequests.specs) != 1 || pullRequests.specs[0].ExistingNumber != 7 ||
		len(store.pullRequestMarkedAt) != 0 {
		t.Fatalf("recorded pull request was not only reconciled: %+v", pullRequests.specs)
	}
}

func TestServiceRejectsPullRequestManagerResultForAnotherFeature(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequests := &recordingPullRequests{result: PullRequest{
		Number: 7, URL: "http://localhost:3001/owner/repository/pulls/7",
		Title: "WIP: Test feature", Body: "<!-- commitarium-feature: fea_other -->",
		State: "open", Draft: true, BaseBranch: "main",
		HeadBranch: "commitarium/fea_test", HeadCommitID: testCommitID,
		CreatedAt: now.Add(3 * time.Minute),
	}}
	store := &memoryStore{stored: stored}
	service := NewServiceWithPreparation(
		store,
		fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		&recordingBranches{}, &recordingCheckout{}, pullRequests,
	)

	_, _, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if !errors.Is(err, ErrPullRequestConflict) {
		t.Fatalf("expected %v, got %v", ErrPullRequestConflict, err)
	}
	if len(store.pullRequestMarkedAt) != 0 {
		t.Fatal("conflicting pull request was recorded")
	}
}

func TestServiceRequiresAcceptedGoalAndRepositoryBinding(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	acceptedAt := now.Add(-time.Minute)
	tests := []struct {
		name       string
		stored     feature.Feature
		repository *project.ForgejoRepository
		want       error
	}{
		{name: "unaccepted goal", stored: feature.Feature{ID: "fea_test", ProjectID: "prj_test", State: feature.StateDraft}, repository: testRepository(now), want: ErrGoalNotAccepted},
		{name: "feature already advanced", stored: feature.Feature{ID: "fea_test", ProjectID: "prj_test", State: feature.StatePlanning, AcceptedGoal: "Ship it", GoalAcceptedAt: &acceptedAt}, repository: testRepository(now), want: ErrFeatureNotDraft},
		{name: "unbound repository", stored: feature.Feature{ID: "fea_test", ProjectID: "prj_test", State: feature.StateDraft, AcceptedGoal: "Ship it", GoalAcceptedAt: &acceptedAt}, want: ErrProjectRepositoryNotBound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{}
			branches := &recordingBranches{}
			service := NewService(
				store,
				fixedFeatureFinder{stored: test.stored},
				fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: test.repository}},
				branches,
			)
			_, _, err := service.Prepare(t.Context(), "prj_test", "fea_test")
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if len(store.reserve) != 0 || branches.getCalls != 0 || branches.ensureCalls != 0 {
				t.Fatal("invalid feature changed storage or contacted Forgejo")
			}
		})
	}
}

func TestServiceLeavesReservationPreparingWhenBranchCreationIsUncertain(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	branches := &recordingBranches{
		base:      Branch{Name: "main", CommitID: testCommitID},
		ensureErr: project.ErrForgejoUnavailable,
	}
	service := newTestService(store, branches, now)

	_, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if !errors.Is(err, project.ErrForgejoUnavailable) || created {
		t.Fatalf("expected uncertain Forgejo error, created=%t err=%v", created, err)
	}
	if store.stored.Status != StatusPreparing || len(store.markedAt) != 0 {
		t.Fatalf("uncertain creation was marked ready: %+v", store.stored)
	}
}

func TestServiceRejectsWorkspaceOwnedByDifferentProject(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	stored.ProjectID = "prj_other"
	service := newTestService(&memoryStore{stored: stored}, &recordingBranches{}, now)

	if _, _, err := service.Prepare(
		t.Context(), "prj_test", "fea_test",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected %v, got %v", ErrConflict, err)
	}
}

func newTestService(store Store, branches BranchManager, now time.Time) *Service {
	service := NewService(
		store,
		fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		branches,
	)
	service.now = func() time.Time { return now }
	return service
}

func acceptedTestFeature(now time.Time) feature.Feature {
	acceptedAt := now.Add(-time.Minute)
	return feature.Feature{
		ID: "fea_test", ProjectID: "prj_test", Title: "Test feature",
		State:        feature.StateDraft,
		AcceptedGoal: "Ship it", GoalAcceptedAt: &acceptedAt,
	}
}

func readyTestWorkspace(now time.Time) Workspace {
	stored := testWorkspace(now)
	branchReadyAt := now.Add(time.Minute)
	checkoutReadyAt := now.Add(2 * time.Minute)
	stored.Status = StatusBranchReady
	stored.BranchCreatedAt = &branchReadyAt
	stored.CheckoutRelativePath = stored.ID
	stored.CheckoutCreatedAt = &checkoutReadyAt
	stored.UpdatedAt = checkoutReadyAt
	return stored
}

func testRepository(now time.Time) *project.ForgejoRepository {
	return &project.ForgejoRepository{
		Owner: "owner", Name: "repository", DefaultBranch: "main", BoundAt: now,
	}
}

func testWorkspace(now time.Time) Workspace {
	return Workspace{
		ID: "wsp_fea_test", ProjectID: "prj_test", FeatureID: "fea_test",
		RepositoryOwner: "owner", RepositoryName: "repository",
		BaseBranch: "main", Branch: "commitarium/fea_test", BaseCommitID: testCommitID,
		Status: StatusPreparing, CreatedAt: now, UpdatedAt: now,
	}
}
