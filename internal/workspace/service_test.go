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

func TestImplementationPublicationKindOwnsAuditFormat(t *testing.T) {
	attemptID := "run_test:lead:correction:2"
	initialMarker := ImplementationPublicationInitial.Marker(attemptID)
	responseMarker := ImplementationPublicationReviewResponse.Marker(attemptID)
	readinessMarker := ImplementationPublicationMergeReadiness.Marker(attemptID)
	if ImplementationPublicationInitial.CommentHeading() != "Implementation summary" ||
		ImplementationPublicationReviewResponse.CommentHeading() != "Review response" ||
		ImplementationPublicationMergeReadiness.CommentHeading() != "Merge readiness" ||
		!strings.HasPrefix(initialMarker, "<!-- commitarium-implementation: ") ||
		!strings.HasPrefix(responseMarker, "<!-- commitarium-review-response: ") ||
		!strings.HasPrefix(readinessMarker, "<!-- commitarium-merge-readiness: ") ||
		initialMarker == responseMarker || responseMarker == readinessMarker {
		t.Fatalf("publication kinds did not derive distinct audit formats: initial=%q response=%q readiness=%q", initialMarker, responseMarker, readinessMarker)
	}
	invalid := ImplementationPublicationKind("unknown")
	if invalid.Validate() == nil || invalid.CommentHeading() != "" || invalid.Marker(attemptID) != "" {
		t.Fatal("unknown publication kind produced a usable audit format")
	}
}

type memoryStore struct {
	stored              Workspace
	reserve             []Workspace
	markedAt            []time.Time
	checkoutMarkedAt    []time.Time
	pullRequestMarkedAt []time.Time
	getErr              error
	reserveErr          error
	mergeReadyCalls     int
	mergedCalls         int
}

func (store *memoryStore) MarkMergeReady(
	_ context.Context,
	_ string,
	approvedCommitID string,
	readyAt time.Time,
) (Workspace, error) {
	store.mergeReadyCalls++
	store.stored.ApprovedCommitID = approvedCommitID
	store.stored.MergeReadyAt = &readyAt
	store.stored.UpdatedAt = readyAt
	return store.stored, nil
}

func (store *memoryStore) MarkMerged(
	_ context.Context,
	_ string,
	_ string,
	mergeCommitID string,
	mergedAt time.Time,
	recordedAt time.Time,
) (Workspace, error) {
	store.mergedCalls++
	store.stored.MergeCommitID = mergeCommitID
	store.stored.MergedAt = &mergedAt
	store.stored.UpdatedAt = recordedAt
	return store.stored, nil
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
	specs      []CheckoutSpec
	promotions []CheckoutPromotionSpec
	ensureErr  error
	promoteErr error
}

func (checkout *recordingCheckout) Promote(
	_ context.Context,
	spec CheckoutPromotionSpec,
) error {
	checkout.promotions = append(checkout.promotions, spec)
	return checkout.promoteErr
}

type recordingPullRequests struct {
	specs               []PullRequestSpec
	planSpecs           []PlanPublicationSpec
	verifiedPlanSpecs   []PlanPublicationSpec
	implementationSpecs []ImplementationPublicationSpec
	reviewSpecs         []ReviewPublicationSpec
	mergeSpecs          []PullRequestMergeSpec
	result              PullRequest
	planResult          PullRequest
	planPublished       bool
	err                 error
	planErr             error
}

func (pullRequests *recordingPullRequests) MergePullRequest(
	_ context.Context,
	_ string,
	_ string,
	spec PullRequestMergeSpec,
) (PullRequest, error) {
	pullRequests.mergeSpecs = append(pullRequests.mergeSpecs, spec)
	return pullRequests.planResult, pullRequests.planErr
}

func (pullRequests *recordingPullRequests) VerifyPullRequestReview(
	_ context.Context,
	_ string,
	_ string,
	spec ReviewPublicationSpec,
) (PullRequest, error) {
	pullRequests.reviewSpecs = append(pullRequests.reviewSpecs, spec)
	return pullRequests.planResult, pullRequests.planErr
}

func (pullRequests *recordingPullRequests) VerifyPullRequestImplementation(
	_ context.Context,
	_ string,
	_ string,
	spec ImplementationPublicationSpec,
) (PullRequest, error) {
	pullRequests.implementationSpecs = append(pullRequests.implementationSpecs, spec)
	return pullRequests.planResult, pullRequests.planErr
}

func (pullRequests *recordingPullRequests) VerifyPullRequestPlan(
	_ context.Context,
	_ string,
	_ string,
	spec PlanPublicationSpec,
) (PullRequest, error) {
	pullRequests.verifiedPlanSpecs = append(pullRequests.verifiedPlanSpecs, spec)
	return pullRequests.planResult, pullRequests.planErr
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

func (pullRequests *recordingPullRequests) EnsurePullRequestPlan(
	_ context.Context,
	_ string,
	_ string,
	spec PlanPublicationSpec,
) (PullRequest, bool, error) {
	pullRequests.planSpecs = append(pullRequests.planSpecs, spec)
	return pullRequests.planResult, pullRequests.planPublished, pullRequests.planErr
}

func (checkout *recordingCheckout) Ensure(_ context.Context, spec CheckoutSpec) error {
	checkout.specs = append(checkout.specs, spec)
	return checkout.ensureErr
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

func TestServicePrepareKeepsAcceptedGoalOnPinnedBaseCheckout(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	branches := &recordingBranches{base: Branch{Name: "main", CommitID: testCommitID}}
	checkout := &recordingCheckout{}
	service := NewServiceWithCheckout(
		store, fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, branches, checkout,
	)
	service.now = func() time.Time { return now }

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil {
		t.Fatalf("prepare accepted-goal checkout: %v", err)
	}
	if !created || prepared.Status != StatusPreparing || !prepared.CheckoutReady() {
		t.Fatalf("unexpected preparation result created=%t workspace=%+v", created, prepared)
	}
	if len(store.reserve) != 1 || store.reserve[0].BaseCommitID != testCommitID ||
		store.reserve[0].Branch != "commitarium/fea_test" {
		t.Fatalf("unexpected durable reservation %+v", store.reserve)
	}
	if branches.getCalls != 1 || branches.ensureCalls != 0 || len(store.markedAt) != 0 ||
		len(checkout.specs) != 1 || checkout.specs[0].Branch != "main" {
		t.Fatalf("accepted goal created remote artifacts: branches=%+v checkout=%+v", branches, checkout.specs)
	}
}

func TestServicePreparesSelectedProjectCheckoutForClarification(t *testing.T) {
	now := time.Date(2026, time.September, 11, 14, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	branches := &recordingBranches{base: Branch{Name: "main", CommitID: testCommitID}}
	checkout := &recordingCheckout{}
	service := NewServiceWithCheckout(
		store,
		fixedFeatureFinder{stored: feature.Feature{
			ID: "fea_test", ProjectID: "prj_test", State: feature.StateDraft,
		}},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		branches,
		checkout,
	)
	service.now = func() time.Time { return now }

	prepared, created, err := service.PrepareForClarification(
		t.Context(), "prj_test", "fea_test",
	)
	if err != nil {
		t.Fatalf("prepare clarification checkout: %v", err)
	}
	if !created || prepared.Status != StatusPreparing || !prepared.CheckoutReady() {
		t.Fatalf("unexpected clarification preparation created=%t workspace=%+v", created, prepared)
	}
	if branches.getCalls != 1 || branches.ensureCalls != 0 {
		t.Fatalf("clarification should pin the base without creating a feature branch: %+v", branches)
	}
	if len(checkout.specs) != 1 {
		t.Fatalf("expected one checkout request, got %+v", checkout.specs)
	}
	spec := checkout.specs[0]
	if spec.WorkspaceID != "wsp_fea_test" || spec.RepositoryOwner != "owner" ||
		spec.RepositoryName != "repository" || spec.Branch != "main" ||
		spec.BaseCommitID != testCommitID || spec.AlreadyReady {
		t.Fatalf("clarification was routed to the wrong checkout: %+v", spec)
	}
	if len(store.checkoutMarkedAt) != 1 || len(store.markedAt) != 0 {
		t.Fatalf("clarification persisted the wrong readiness state: %+v", store.stored)
	}
}

func TestServiceRetriesClarificationAgainstPinnedProjectCheckout(t *testing.T) {
	now := time.Date(2026, time.September, 11, 14, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	checkoutAt := now.Add(time.Minute)
	stored.CheckoutRelativePath = stored.ID
	stored.CheckoutCreatedAt = &checkoutAt
	stored.UpdatedAt = checkoutAt
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{base: Branch{
		Name: "main", CommitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	checkout := &recordingCheckout{}
	service := NewServiceWithCheckout(
		store,
		fixedFeatureFinder{stored: feature.Feature{
			ID: "fea_test", ProjectID: "prj_test", State: feature.StateDraft,
		}},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		branches,
		checkout,
	)

	prepared, created, err := service.PrepareForClarification(
		t.Context(), "prj_test", "fea_test",
	)
	if err != nil || created || prepared != stored {
		t.Fatalf("retry clarification: created=%t workspace=%+v err=%v", created, prepared, err)
	}
	if branches.getCalls != 0 || len(checkout.specs) != 1 ||
		!checkout.specs[0].AlreadyReady || checkout.specs[0].Branch != stored.BaseBranch ||
		checkout.specs[0].BaseCommitID != testCommitID || len(store.checkoutMarkedAt) != 0 {
		t.Fatalf("retry did not reconcile the pinned checkout: branches=%+v checkout=%+v", branches, checkout.specs)
	}
}

func TestServicePromotesClarificationCheckoutBeforeBranchReadiness(t *testing.T) {
	now := time.Date(2026, time.September, 11, 14, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	checkoutAt := now.Add(time.Minute)
	stored.CheckoutRelativePath = stored.ID
	stored.CheckoutCreatedAt = &checkoutAt
	stored.UpdatedAt = checkoutAt
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{base: Branch{Name: stored.Branch, CommitID: stored.BaseCommitID}}
	checkout := &recordingCheckout{}
	pullRequest := PullRequest{
		Number: 7, URL: "http://localhost:3001/owner/repository/pulls/7",
		Title: "WIP: Test feature", Body: "<!-- commitarium-feature: fea_test -->",
		State: "open", Draft: true, BaseBranch: "main",
		HeadBranch: stored.Branch, HeadCommitID: stored.BaseCommitID,
		CreatedAt: checkoutAt.Add(2 * time.Minute),
	}
	pullRequests := &recordingPullRequests{
		result: pullRequest, planResult: pullRequest, planPublished: true,
	}
	planningFeature := acceptedTestFeature(now)
	planningFeature.State = feature.StatePlanning
	service := NewServiceWithPreparation(
		store,
		fixedFeatureFinder{stored: planningFeature},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		branches,
		checkout,
		pullRequests,
	)
	branchReadyAt := checkoutAt.Add(time.Minute)
	service.now = func() time.Time { return branchReadyAt }

	prepared, published, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_plan", "Final plan",
	)
	if err != nil || !published || prepared.Status != StatusBranchReady ||
		!prepared.PullRequestReady() {
		t.Fatalf("promote clarification checkout: published=%t workspace=%+v err=%v", published, prepared, err)
	}
	if branches.ensureCalls != 1 || len(checkout.promotions) != 1 {
		t.Fatalf("expected remote and local branch promotion: branches=%+v promotions=%+v", branches, checkout.promotions)
	}
	promotion := checkout.promotions[0]
	if promotion.WorkspaceID != stored.ID || promotion.RepositoryOwner != stored.RepositoryOwner ||
		promotion.RepositoryName != stored.RepositoryName || promotion.BaseBranch != stored.BaseBranch ||
		promotion.FeatureBranch != stored.Branch || promotion.BaseCommitID != stored.BaseCommitID {
		t.Fatalf("unexpected checkout promotion %+v", promotion)
	}
	if len(checkout.specs) != 2 || !checkout.specs[0].AlreadyReady ||
		checkout.specs[0].Branch != stored.Branch ||
		!checkout.specs[0].RequireCleanBaseline ||
		!checkout.specs[1].RequireCleanBaseline || len(store.markedAt) != 1 {
		t.Fatalf("promoted checkout was not reconciled and recorded: specs=%+v workspace=%+v", checkout.specs, store.stored)
	}
}

func TestServiceDoesNotCreateForgejoArtifactsWhenPlanningCheckoutIsDirty(t *testing.T) {
	now := time.Date(2026, time.September, 11, 14, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	checkoutAt := now.Add(time.Minute)
	stored.CheckoutRelativePath = stored.ID
	stored.CheckoutCreatedAt = &checkoutAt
	stored.UpdatedAt = checkoutAt
	store := &memoryStore{stored: stored}
	planningFeature := acceptedTestFeature(now)
	planningFeature.State = feature.StatePlanning
	branches := &recordingBranches{}
	checkout := &recordingCheckout{promoteErr: ErrCheckoutConflict}
	pullRequests := &recordingPullRequests{}
	service := NewServiceWithPreparation(
		store, fixedFeatureFinder{stored: planningFeature},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, branches, checkout, pullRequests,
	)

	_, published, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_plan", "Final plan",
	)
	if !errors.Is(err, ErrCheckoutConflict) || published {
		t.Fatalf("expected dirty-checkout conflict, published=%t err=%v", published, err)
	}
	if len(checkout.promotions) != 1 || branches.ensureCalls != 0 ||
		len(pullRequests.specs) != 0 || store.stored.Status != StatusPreparing {
		t.Fatalf("dirty checkout allowed external mutation: workspace=%+v branches=%+v pull_requests=%+v", store.stored, branches, pullRequests.specs)
	}
}

func TestServiceRetriesPreparingPlanPromotionWithoutReadingMovingDefaultBranch(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	checkoutAt := now.Add(time.Minute)
	stored.CheckoutRelativePath = stored.ID
	stored.CheckoutCreatedAt = &checkoutAt
	stored.UpdatedAt = checkoutAt
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{base: Branch{
		Name: stored.Branch, CommitID: stored.BaseCommitID,
	}}
	checkout := &recordingCheckout{}
	pullRequest := PullRequest{
		Number: 7, URL: "http://localhost:3001/owner/repository/pulls/7",
		Title: "WIP: Test feature", Body: "<!-- commitarium-feature: fea_test -->",
		State: "open", Draft: true, BaseBranch: stored.BaseBranch,
		HeadBranch: stored.Branch, HeadCommitID: stored.BaseCommitID, CreatedAt: now,
	}
	planningFeature := acceptedTestFeature(now)
	planningFeature.State = feature.StatePlanning
	service := NewServiceWithPreparation(
		store, fixedFeatureFinder{stored: planningFeature},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		branches, checkout, &recordingPullRequests{
			result: pullRequest, planResult: pullRequest, planPublished: true,
		},
	)
	service.now = func() time.Time { return now.Add(2 * time.Minute) }

	prepared, published, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_plan", "Final plan",
	)
	if err != nil {
		t.Fatalf("retry plan promotion: %v", err)
	}
	if !published || prepared.Status != StatusBranchReady {
		t.Fatalf("unexpected retry result published=%t workspace=%+v", published, prepared)
	}
	if branches.getCalls != 1 || branches.ensureCalls != 1 {
		t.Fatalf("retry did not use reservation: %+v", branches)
	}
	if prepared.BaseCommitID != testCommitID {
		t.Fatalf("base commit changed to %q", prepared.BaseCommitID)
	}
}

func TestServicePrepareReconcilesLegacyBranchReadyCheckout(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	stored.Status = StatusBranchReady
	readyAt := now.Add(time.Minute)
	stored.BranchCreatedAt = &readyAt
	stored.CheckoutRelativePath = stored.ID
	stored.CheckoutCreatedAt = &readyAt
	stored.UpdatedAt = readyAt
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{}
	checkout := &recordingCheckout{}
	service := NewServiceWithCheckout(
		store, fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		branches, checkout,
	)

	prepared, created, err := service.Prepare(t.Context(), "prj_test", "fea_test")
	if err != nil || created || prepared != stored {
		t.Fatalf("unexpected ready retry result created=%t workspace=%+v err=%v", created, prepared, err)
	}
	if branches.getCalls != 0 || branches.ensureCalls != 0 || len(store.markedAt) != 0 ||
		len(checkout.specs) != 1 || checkout.specs[0].Branch != stored.Branch {
		t.Fatalf("ready retry did not only reconcile its checkout")
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

func TestServiceCreatesDraftPullRequestWhenPlanIsSubmitted(t *testing.T) {
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
	planningFeature := acceptedTestFeature(now)
	planningFeature.State = feature.StatePlanning
	branches := &recordingBranches{base: Branch{Name: stored.Branch, CommitID: stored.BaseCommitID}}
	pullRequests.planResult = pullRequest
	pullRequests.planPublished = true
	service := NewServiceWithPreparation(
		store,
		fixedFeatureFinder{stored: planningFeature},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		branches,
		&recordingCheckout{},
		pullRequests,
	)
	readyAt := now.Add(4 * time.Minute)
	service.now = func() time.Time { return readyAt }

	prepared, published, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_plan", "Final plan",
	)
	if err != nil || !published || !prepared.PullRequestReady() {
		t.Fatalf("publish plan: published=%t workspace=%+v err=%v", published, prepared, err)
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

func TestServicePublishesPlanThroughRecordedPullRequestWithoutRecreatingIt(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 7
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/7"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	store := &memoryStore{stored: stored}
	existing := PullRequest{
		Number: 7, URL: stored.PullRequestURL, Title: "Changed WIP title",
		Body: "<!-- commitarium-feature: fea_test -->", State: "open", Draft: true,
		BaseBranch: "main", HeadBranch: "commitarium/fea_test", HeadCommitID: testCommitID,
		CreatedAt: pullRequestReadyAt,
	}
	pullRequests := &recordingPullRequests{planResult: existing, planPublished: true}
	planningFeature := acceptedTestFeature(now)
	planningFeature.State = feature.StatePlanning
	service := NewServiceWithPreparation(
		store,
		fixedFeatureFinder{stored: planningFeature},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		&recordingBranches{base: Branch{Name: stored.Branch, CommitID: stored.BaseCommitID}},
		&recordingCheckout{}, pullRequests,
	)

	prepared, published, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_plan", "Final plan",
	)
	if err != nil || !published || prepared != stored {
		t.Fatalf("publish through recorded pull request: published=%t workspace=%+v err=%v", published, prepared, err)
	}
	if len(pullRequests.specs) != 0 || len(pullRequests.planSpecs) != 1 ||
		len(store.pullRequestMarkedAt) != 0 {
		t.Fatalf("recorded pull request was recreated: drafts=%+v plans=%+v", pullRequests.specs, pullRequests.planSpecs)
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
	planningFeature := acceptedTestFeature(now)
	planningFeature.State = feature.StatePlanning
	service := NewServiceWithPreparation(
		store,
		fixedFeatureFinder{stored: planningFeature},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		&recordingBranches{base: Branch{Name: stored.Branch, CommitID: stored.BaseCommitID}},
		&recordingCheckout{}, pullRequests,
	)

	_, _, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_plan", "Final plan",
	)
	if !errors.Is(err, ErrPullRequestConflict) {
		t.Fatalf("expected %v, got %v", ErrPullRequestConflict, err)
	}
	if len(store.pullRequestMarkedAt) != 0 {
		t.Fatal("conflicting pull request was recorded")
	}
}

func TestServicePublishesPlanAfterReconcilingManagedWorkspace(t *testing.T) {
	now := time.Date(2026, time.September, 10, 1, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 7
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/7"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	branches := &recordingBranches{base: Branch{
		Name: stored.Branch, CommitID: stored.BaseCommitID,
	}}
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{
		planPublished: true,
		planResult: PullRequest{
			Number: 7, URL: stored.PullRequestURL, Title: "WIP: Test feature",
			Body: "<!-- commitarium-feature: fea_test -->", State: "open", Draft: true,
			BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch,
			HeadCommitID: stored.BaseCommitID, CreatedAt: pullRequestReadyAt,
		},
	}
	accepted := acceptedTestFeature(now)
	accepted.State = feature.StatePlanning
	service := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: accepted},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, branches, checkout, pullRequests,
	)

	got, published, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
	)
	if err != nil || !published || got != stored {
		t.Fatalf("publish plan: workspace=%+v published=%t err=%v", got, published, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 ||
		!checkout.specs[0].AlreadyReady || !checkout.specs[0].RequireCleanBaseline ||
		len(pullRequests.planSpecs) != 1 {
		t.Fatalf("publication did not reconcile all state: branches=%+v checkout=%+v plans=%+v", branches, checkout.specs, pullRequests.planSpecs)
	}
	spec := pullRequests.planSpecs[0]
	if spec.Number != stored.PullRequestNumber || spec.Plan != "Final plan" ||
		spec.HeadCommitID != stored.BaseCommitID ||
		!strings.HasPrefix(spec.PublicationMarker, "<!-- commitarium-plan: ") {
		t.Fatalf("unexpected plan publication spec %+v", spec)
	}
}

func TestServiceRejectsPlanPublicationAfterFeatureBranchMoves(t *testing.T) {
	now := time.Date(2026, time.September, 10, 1, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 7
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/7"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	accepted := acceptedTestFeature(now)
	accepted.State = feature.StatePlanning
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{}
	service := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: accepted},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, &recordingBranches{base: Branch{
			Name: stored.Branch, CommitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}}, checkout, pullRequests,
	)

	_, _, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
	)
	if !errors.Is(err, ErrBranchConflict) {
		t.Fatalf("expected %v, got %v", ErrBranchConflict, err)
	}
	if len(checkout.specs) != 0 || len(pullRequests.planSpecs) != 0 {
		t.Fatal("conflicting branch allowed a checkout or pull-request mutation")
	}
}

func TestServiceVerifiesPublishedPlanWithoutRepublishing(t *testing.T) {
	now := time.Date(2026, time.September, 10, 2, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 7
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/7"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	branches := &recordingBranches{base: Branch{
		Name: stored.Branch, CommitID: stored.BaseCommitID,
	}}
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{planResult: PullRequest{
		Number: 7, URL: stored.PullRequestURL, Title: "WIP: Test feature",
		Body: "published plan", State: "open", Draft: true,
		BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch,
		HeadCommitID: stored.BaseCommitID, CreatedAt: pullRequestReadyAt,
	}}
	accepted := acceptedTestFeature(now)
	accepted.State = feature.StateImplementing
	service := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: accepted},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, branches, checkout, pullRequests,
	)

	got, err := service.VerifyPublishedPlan(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
	)
	if err != nil || got != stored {
		t.Fatalf("verify plan: workspace=%+v err=%v", got, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 ||
		!checkout.specs[0].RequireCleanBaseline || len(pullRequests.planSpecs) != 0 ||
		len(pullRequests.verifiedPlanSpecs) != 1 {
		t.Fatalf("verification mutated publication state: branches=%d checkout=%+v published=%+v verified=%+v", branches.getCalls, checkout.specs, pullRequests.planSpecs, pullRequests.verifiedPlanSpecs)
	}
}

func TestServiceVerifiesImplementationContinuationWithoutRequiringCleanCheckout(t *testing.T) {
	now := time.Date(2026, time.September, 10, 2, 30, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 8
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/8"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	branches := &recordingBranches{base: Branch{
		Name: stored.Branch, CommitID: stored.BaseCommitID,
	}}
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{planResult: PullRequest{
		Number: 8, URL: stored.PullRequestURL, Title: "WIP: Test feature",
		Body: "published plan", State: "open", Draft: true,
		BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch,
		HeadCommitID: stored.BaseCommitID, CreatedAt: pullRequestReadyAt,
	}}
	accepted := acceptedTestFeature(now)
	accepted.State = feature.StateImplementing
	service := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: accepted},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, branches, checkout, pullRequests,
	)

	got, err := service.VerifyImplementationContinuation(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
	)
	if err != nil || got != stored {
		t.Fatalf("verify implementation continuation: workspace=%+v err=%v", got, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 ||
		checkout.specs[0].RequireCleanBaseline || len(pullRequests.planSpecs) != 0 ||
		len(pullRequests.verifiedPlanSpecs) != 1 {
		t.Fatalf("continuation verification did not preserve local work: branches=%d checkout=%+v published=%+v verified=%+v", branches.getCalls, checkout.specs, pullRequests.planSpecs, pullRequests.verifiedPlanSpecs)
	}
}

func TestServiceVerifiesAgentImplementationPublicationWithoutWriting(t *testing.T) {
	now := time.Date(2026, time.September, 10, 4, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 8
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/8"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	implementationCommit := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	branches := &recordingBranches{base: Branch{
		Name: stored.Branch, CommitID: implementationCommit,
	}}
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{planResult: PullRequest{
		Number: 8, URL: stored.PullRequestURL, Title: "WIP: Test feature",
		Body: "published plan", State: "open", Draft: true,
		BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch,
		HeadCommitID: implementationCommit, CreatedAt: pullRequestReadyAt,
	}}
	accepted := acceptedTestFeature(now)
	accepted.State = feature.StateImplementing
	service := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: accepted},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}}, branches, checkout, pullRequests,
	)

	got, err := service.VerifyImplementationPublication(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
		"att_implementation_1", "Changed the exporter and passed tests.",
		implementationCommit, 8, "codex-lead",
	)
	if err != nil || got != stored {
		t.Fatalf("verify implementation publication: workspace=%+v err=%v", got, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 ||
		checkout.specs[0].ExpectedHeadCommitID != implementationCommit ||
		!checkout.specs[0].RequireClean || len(pullRequests.implementationSpecs) != 1 {
		t.Fatalf("implementation verification did not reconcile every fact: branches=%d checkout=%+v pull_requests=%+v", branches.getCalls, checkout.specs, pullRequests.implementationSpecs)
	}
	spec := pullRequests.implementationSpecs[0]
	if spec.Number != 8 || spec.HeadCommitID != implementationCommit ||
		spec.ExpectedAuthor != "codex-lead" ||
		spec.PublicationKind != ImplementationPublicationInitial ||
		spec.AttemptID != "att_implementation_1" ||
		!strings.HasPrefix(spec.PublicationKind.Marker(spec.AttemptID), "<!-- commitarium-implementation: ") ||
		!strings.HasPrefix(spec.PlanPublicationMarker, "<!-- commitarium-plan: ") {
		t.Fatalf("unexpected implementation publication spec %+v", spec)
	}
	mismatchedRepository := *testRepository(now)
	mismatchedRepository.Owner = "different-owner"
	mismatched := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: accepted},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: &mismatchedRepository,
		}}, branches, checkout, pullRequests,
	)
	if _, err := mismatched.VerifyImplementationPublication(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
		"att_implementation_1", "Changed the exporter and passed tests.",
		implementationCommit, 8, "codex-lead",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched project repository verification error = %v, want %v", err, ErrConflict)
	}
}

func TestServiceVerifiesLeadReviewResponseAsNewDescendantCommit(t *testing.T) {
	now := time.Date(2026, time.September, 10, 5, 30, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 8
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/8"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	reviewedCommit := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	correctedCommit := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	branches := &recordingBranches{base: Branch{Name: stored.Branch, CommitID: correctedCommit}}
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{planResult: PullRequest{
		Number: 8, URL: stored.PullRequestURL, State: "open", Draft: true,
		BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch, HeadCommitID: correctedCommit,
	}}
	reviewing := acceptedTestFeature(now)
	reviewing.State = feature.StateReviewing
	service := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: reviewing},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		branches, checkout, pullRequests,
	)
	got, err := service.VerifyImplementationReviewResponse(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
		"att_correction_1", "Added the missing failure-path test.", reviewedCommit,
		correctedCommit, 8, "codex-lead",
	)
	if err != nil || got != stored {
		t.Fatalf("verify review response: workspace=%+v err=%v", got, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 ||
		checkout.specs[0].BaseCommitID != reviewedCommit ||
		checkout.specs[0].ExpectedHeadCommitID != correctedCommit ||
		!checkout.specs[0].RequireClean || len(pullRequests.implementationSpecs) != 1 {
		t.Fatalf("response verification did not reconcile every fact: branches=%d checkout=%+v pull_requests=%+v", branches.getCalls, checkout.specs, pullRequests.implementationSpecs)
	}
	spec := pullRequests.implementationSpecs[0]
	if spec.PublicationKind != ImplementationPublicationReviewResponse ||
		spec.AttemptID != "att_correction_1" || spec.HeadCommitID != correctedCommit ||
		spec.ExpectedAuthor != "codex-lead" ||
		!strings.HasPrefix(spec.PublicationKind.Marker(spec.AttemptID), "<!-- commitarium-review-response: ") {
		t.Fatalf("unexpected review response publication spec %+v", spec)
	}
	if _, err := service.VerifyImplementationReviewResponse(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
		"att_correction_1", "No new revision.", reviewedCommit, reviewedCommit, 8, "codex-lead",
	); err == nil {
		t.Fatal("review response accepted the already-reviewed commit")
	}
}

func TestServiceVerifiesLeadMergeReadinessOnApprovedCommit(t *testing.T) {
	now := time.Date(2026, time.September, 10, 5, 45, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 8
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/8"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	approvedCommit := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	branches := &recordingBranches{base: Branch{Name: stored.Branch, CommitID: approvedCommit}}
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{planResult: PullRequest{
		Number: 8, URL: stored.PullRequestURL, State: "open", Draft: true,
		BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch, HeadCommitID: approvedCommit,
	}}
	reviewing := acceptedTestFeature(now)
	reviewing.State = feature.StateReviewing
	service := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: reviewing},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		branches, checkout, pullRequests,
	)
	got, err := service.VerifyImplementationMergeReadiness(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
		"att_readiness_2", "I agree this exact commit is ready to merge.",
		approvedCommit, 8, "codex-lead",
	)
	if err != nil || got != stored {
		t.Fatalf("verify merge readiness: workspace=%+v err=%v", got, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 ||
		checkout.specs[0].ExpectedHeadCommitID != approvedCommit ||
		!checkout.specs[0].RequireClean || len(pullRequests.implementationSpecs) != 1 {
		t.Fatalf("merge-readiness verification did not reconcile every fact: branches=%d checkout=%+v pull_requests=%+v", branches.getCalls, checkout.specs, pullRequests.implementationSpecs)
	}
	spec := pullRequests.implementationSpecs[0]
	if spec.PublicationKind != ImplementationPublicationMergeReadiness ||
		spec.AttemptID != "att_readiness_2" || spec.HeadCommitID != approvedCommit ||
		spec.ExpectedAuthor != "codex-lead" {
		t.Fatalf("unexpected merge-readiness publication spec %+v", spec)
	}
}

func TestServicePinsExactMergeTargetBeforeLifecycleAdvance(t *testing.T) {
	now := time.Date(2026, time.September, 11, 15, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 8
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/8"
	stored.PullRequestRecordedAt = &pullRequestAt
	stored.UpdatedAt = pullRequestAt
	approvedCommit := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{base: Branch{Name: stored.Branch, CommitID: approvedCommit}}
	checkout := &recordingCheckout{}
	reviewing := acceptedTestFeature(now)
	reviewing.State = feature.StateReviewing
	service := NewServiceWithPreparation(
		store, fixedFeatureFinder{stored: reviewing},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		branches, checkout, &recordingPullRequests{},
	)
	mergeReadyAt := now.Add(4 * time.Minute)
	service.now = func() time.Time { return mergeReadyAt }

	ready, err := service.RecordMergeReady(
		t.Context(), "prj_test", "fea_test", approvedCommit, 8,
	)
	if err != nil || ready.ApprovedCommitID != approvedCommit ||
		ready.MergeReadyAt == nil || !ready.MergeReadyAt.Equal(mergeReadyAt) {
		t.Fatalf("record merge readiness: workspace=%+v err=%v", ready, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 ||
		checkout.specs[0].ExpectedHeadCommitID != approvedCommit ||
		!checkout.specs[0].RequireClean || store.mergeReadyCalls != 1 {
		t.Fatalf("merge target was not fully reconciled: branches=%d checkout=%+v store_calls=%d", branches.getCalls, checkout.specs, store.mergeReadyCalls)
	}
}

func TestServiceMergesOnlyPinnedRevisionAndAdoptsRecordedResult(t *testing.T) {
	now := time.Date(2026, time.September, 11, 16, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestAt := now.Add(3 * time.Minute)
	mergeReadyAt := now.Add(4 * time.Minute)
	// Forgejo may report a coarser or slightly earlier timestamp than the
	// coordinator's merge-ready record even though the merge happened later.
	mergedAt := pullRequestAt
	approvedCommit := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mergeCommit := "cccccccccccccccccccccccccccccccccccccccc"
	stored.PullRequestNumber = 8
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/8"
	stored.PullRequestRecordedAt = &pullRequestAt
	stored.ApprovedCommitID = approvedCommit
	stored.MergeReadyAt = &mergeReadyAt
	stored.UpdatedAt = mergeReadyAt
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{base: Branch{Name: stored.Branch, CommitID: approvedCommit}}
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{planResult: PullRequest{
		Number: 8, URL: stored.PullRequestURL, Title: "Test feature",
		Body: "<!-- commitarium-feature: fea_test -->", State: "closed", Draft: false,
		BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch,
		HeadCommitID: approvedCommit, CreatedAt: pullRequestAt,
		Merged: true, MergeCommitID: mergeCommit, MergedAt: &mergedAt,
	}}
	ready := acceptedTestFeature(now)
	ready.State = feature.StateReadyToMerge
	service := NewServiceWithPreparation(
		store, fixedFeatureFinder{stored: ready},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		branches, checkout, pullRequests,
	)
	service.now = func() time.Time { return now.Add(5 * time.Minute) }

	merged, changed, err := service.MergeApproved(t.Context(), "prj_test", "fea_test")
	if err != nil || !changed || merged.MergeCommitID != mergeCommit {
		t.Fatalf("merge approved revision: changed=%t workspace=%+v err=%v", changed, merged, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 ||
		checkout.specs[0].ExpectedHeadCommitID != approvedCommit ||
		len(pullRequests.mergeSpecs) != 1 ||
		pullRequests.mergeSpecs[0].HeadCommitID != approvedCommit || store.mergedCalls != 1 {
		t.Fatalf("merge did not use the pinned revision: branches=%d checkout=%+v merge=%+v store_calls=%d", branches.getCalls, checkout.specs, pullRequests.mergeSpecs, store.mergedCalls)
	}
	retried, changed, err := service.MergeApproved(t.Context(), "prj_test", "fea_test")
	if err != nil || changed || retried.MergeCommitID != mergeCommit || len(pullRequests.mergeSpecs) != 1 {
		t.Fatalf("retry should adopt durable merge result: changed=%t workspace=%+v err=%v calls=%d", changed, retried, err, len(pullRequests.mergeSpecs))
	}
}

func TestServiceVerifiesReviewerPublicationWithoutWriting(t *testing.T) {
	now := time.Date(2026, time.September, 10, 5, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestReadyAt := now.Add(3 * time.Minute)
	stored.PullRequestNumber = 8
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/8"
	stored.PullRequestRecordedAt = &pullRequestReadyAt
	stored.UpdatedAt = pullRequestReadyAt
	implementationCommit := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	branches := &recordingBranches{base: Branch{Name: stored.Branch, CommitID: implementationCommit}}
	checkout := &recordingCheckout{}
	pullRequests := &recordingPullRequests{planResult: PullRequest{
		Number: 8, URL: stored.PullRequestURL, State: "open", Draft: true,
		BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch, HeadCommitID: implementationCommit,
	}}
	reviewing := acceptedTestFeature(now)
	reviewing.State = feature.StateReviewing
	service := NewServiceWithPreparation(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: reviewing},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		branches, checkout, pullRequests,
	)
	got, err := service.VerifyImplementationReview(
		t.Context(), "prj_test", "fea_test", "sev_final_plan", "Final plan",
		"att_review_1", "One edge case still fails.", implementationCommit, 8, 11,
		"codex-reviewer", "REQUEST_CHANGES",
	)
	if err != nil || got != stored {
		t.Fatalf("verify implementation review: workspace=%+v err=%v", got, err)
	}
	if branches.getCalls != 1 || len(checkout.specs) != 1 || !checkout.specs[0].RequireClean ||
		checkout.specs[0].ExpectedHeadCommitID != implementationCommit || len(pullRequests.reviewSpecs) != 1 {
		t.Fatalf("review verification did not reconcile every fact: branches=%d checkout=%+v pull_requests=%+v", branches.getCalls, checkout.specs, pullRequests.reviewSpecs)
	}
	spec := pullRequests.reviewSpecs[0]
	if spec.ReviewID != 11 || spec.ExpectedAuthor != "codex-reviewer" ||
		spec.ExpectedState != "REQUEST_CHANGES" ||
		!strings.HasPrefix(spec.PublicationMarker, "<!-- commitarium-review: ") {
		t.Fatalf("unexpected review publication spec %+v", spec)
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

func TestGetCompletedHandoffReturnsRecordedReviewedAndMergedCommits(t *testing.T) {
	now := time.Date(2026, time.September, 11, 14, 0, 0, 0, time.UTC)
	stored := readyTestWorkspace(now)
	pullRequestAt := stored.UpdatedAt.Add(time.Minute)
	mergeReadyAt := pullRequestAt.Add(time.Minute)
	mergedAt := mergeReadyAt.Add(time.Minute)
	stored.PullRequestNumber = 14
	stored.PullRequestURL = "http://localhost:3001/owner/repository/pulls/14"
	stored.PullRequestRecordedAt = &pullRequestAt
	stored.ApprovedCommitID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stored.MergeReadyAt = &mergeReadyAt
	stored.MergeCommitID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stored.MergedAt = &mergedAt
	stored.UpdatedAt = mergedAt
	completed := acceptedTestFeature(now)
	completed.State = feature.StateCompleted
	service := NewService(
		&memoryStore{stored: stored},
		fixedFeatureFinder{stored: completed},
		fixedProjectFinder{stored: project.Project{
			ID: "prj_test", ForgejoRepository: testRepository(now),
		}},
		&recordingBranches{},
	)

	got, err := service.GetCompletedHandoff(t.Context(), "prj_test", "fea_test")
	if err != nil {
		t.Fatalf("get completed handoff: %v", err)
	}
	if got != stored {
		t.Fatalf("unexpected completed handoff %+v", got)
	}
}

func TestGetCompletedHandoffRequiresCompletedFeatureAndRecordedMerge(t *testing.T) {
	now := time.Date(2026, time.September, 11, 14, 0, 0, 0, time.UTC)
	completed := acceptedTestFeature(now)
	completed.State = feature.StateCompleted
	tests := []struct {
		name    string
		feature feature.Feature
		stored  Workspace
		project project.Project
		want    error
	}{
		{
			name: "feature still ready to merge",
			feature: func() feature.Feature {
				value := completed
				value.State = feature.StateReadyToMerge
				return value
			}(),
			stored:  readyTestWorkspace(now),
			project: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)},
			want:    ErrHandoffNotReady,
		},
		{
			name:    "merge identity missing",
			feature: completed,
			stored:  readyTestWorkspace(now),
			project: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)},
			want:    ErrHandoffNotReady,
		},
		{
			name:    "repository identity changed",
			feature: completed,
			stored:  readyTestWorkspace(now),
			project: project.Project{ID: "prj_test", ForgejoRepository: &project.ForgejoRepository{
				Owner: "other", Name: "repository", DefaultBranch: "main", BoundAt: now,
			}},
			want: ErrConflict,
		},
		{
			name:    "invalid durable commit identity",
			feature: completed,
			stored: func() Workspace {
				value := readyTestWorkspace(now)
				value.BaseCommitID = "not-a-commit"
				return value
			}(),
			project: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)},
			want:    ErrConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(
				&memoryStore{stored: test.stored},
				fixedFeatureFinder{stored: test.feature},
				fixedProjectFinder{stored: test.project},
				&recordingBranches{},
			)
			_, err := service.GetCompletedHandoff(t.Context(), "prj_test", "fea_test")
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
		})
	}
}

func TestServiceLeavesReservationPreparingWhenBranchCreationIsUncertain(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	checkoutAt := now.Add(time.Minute)
	stored.CheckoutRelativePath = stored.ID
	stored.CheckoutCreatedAt = &checkoutAt
	stored.UpdatedAt = checkoutAt
	store := &memoryStore{stored: stored}
	branches := &recordingBranches{
		ensureErr: project.ErrForgejoUnavailable,
	}
	planningFeature := acceptedTestFeature(now)
	planningFeature.State = feature.StatePlanning
	service := NewServiceWithPreparation(
		store, fixedFeatureFinder{stored: planningFeature},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		branches, &recordingCheckout{}, &recordingPullRequests{},
	)

	_, published, err := service.PublishPlan(
		t.Context(), "prj_test", "fea_test", "sev_plan", "Final plan",
	)
	if !errors.Is(err, project.ErrForgejoUnavailable) || published {
		t.Fatalf("expected uncertain Forgejo error, published=%t err=%v", published, err)
	}
	if store.stored.Status != StatusPreparing || len(store.markedAt) != 0 {
		t.Fatalf("uncertain creation was marked ready: %+v", store.stored)
	}
}

func TestServiceRejectsWorkspaceOwnedByDifferentProject(t *testing.T) {
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	stored := testWorkspace(now)
	stored.ProjectID = "prj_other"
	service := NewServiceWithCheckout(
		&memoryStore{stored: stored}, fixedFeatureFinder{stored: acceptedTestFeature(now)},
		fixedProjectFinder{stored: project.Project{ID: "prj_test", ForgejoRepository: testRepository(now)}},
		&recordingBranches{}, &recordingCheckout{},
	)

	if _, _, err := service.Prepare(
		t.Context(), "prj_test", "fea_test",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected %v, got %v", ErrConflict, err)
	}
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
