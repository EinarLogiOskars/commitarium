package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

var ErrGoalNotAccepted = errors.New("feature goal has not been accepted")
var ErrFeatureNotDraft = errors.New("feature is no longer a draft")
var ErrFeatureNotPlanning = errors.New("feature is not in planning")
var ErrFeatureNotReviewing = errors.New("feature is not in review")
var ErrProjectRepositoryNotBound = errors.New("project has no Forgejo repository binding")
var ErrBranchNotFound = errors.New("Forgejo branch not found")
var ErrBranchConflict = errors.New("Forgejo feature branch exists at a different commit")
var ErrCheckoutConflict = errors.New("managed checkout conflicts with its durable workspace")
var ErrCheckoutUnavailable = errors.New("managed checkout is unavailable")
var ErrPullRequestConflict = errors.New("managed Forgejo pull request conflicts with its durable workspace")

type FeatureFinder interface {
	GetByID(ctx context.Context, projectID, featureID string) (feature.Feature, error)
}

type ProjectFinder interface {
	GetByID(ctx context.Context, projectID string) (project.Project, error)
}

type BranchManager interface {
	GetBranch(ctx context.Context, owner, repository, branch string) (Branch, error)
	EnsureBranch(
		ctx context.Context,
		owner string,
		repository string,
		branch string,
		baseCommitID string,
	) (Branch, error)
}

type CheckoutManager interface {
	Ensure(ctx context.Context, spec CheckoutSpec) error
}

type PullRequestManager interface {
	EnsureDraftPullRequest(
		ctx context.Context,
		owner string,
		repository string,
		spec PullRequestSpec,
	) (PullRequest, error)
	EnsurePullRequestPlan(
		ctx context.Context,
		owner string,
		repository string,
		spec PlanPublicationSpec,
	) (PullRequest, bool, error)
	VerifyPullRequestPlan(
		ctx context.Context,
		owner string,
		repository string,
		spec PlanPublicationSpec,
	) (PullRequest, error)
	VerifyPullRequestImplementation(
		ctx context.Context,
		owner string,
		repository string,
		spec ImplementationPublicationSpec,
	) (PullRequest, error)
	VerifyPullRequestReview(
		ctx context.Context,
		owner string,
		repository string,
		spec ReviewPublicationSpec,
	) (PullRequest, error)
}

type Service struct {
	store        Store
	features     FeatureFinder
	projects     ProjectFinder
	branches     BranchManager
	checkouts    CheckoutManager
	pullRequests PullRequestManager
	now          func() time.Time
}

func NewServiceWithPreparation(
	store Store,
	features FeatureFinder,
	projects ProjectFinder,
	branches BranchManager,
	checkouts CheckoutManager,
	pullRequests PullRequestManager,
) *Service {
	service := NewServiceWithCheckout(store, features, projects, branches, checkouts)
	service.pullRequests = pullRequests
	return service
}

func NewServiceWithCheckout(
	store Store,
	features FeatureFinder,
	projects ProjectFinder,
	branches BranchManager,
	checkouts CheckoutManager,
) *Service {
	service := NewService(store, features, projects, branches)
	service.checkouts = checkouts
	return service
}

func NewService(
	store Store,
	features FeatureFinder,
	projects ProjectFinder,
	branches BranchManager,
) *Service {
	return &Service{
		store: store, features: features, projects: projects, branches: branches,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (service *Service) Get(
	ctx context.Context,
	projectID string,
	featureID string,
) (Workspace, error) {
	if _, err := service.features.GetByID(ctx, projectID, featureID); err != nil {
		return Workspace{}, err
	}
	stored, err := service.store.GetByFeatureID(ctx, featureID)
	if err != nil {
		return Workspace{}, fmt.Errorf("get workspace for feature %q: %w", featureID, err)
	}
	if stored.ProjectID != projectID || stored.FeatureID != featureID {
		return Workspace{}, ErrConflict
	}
	return stored, nil
}

func (service *Service) Prepare(
	ctx context.Context,
	projectID string,
	featureID string,
) (Workspace, bool, error) {
	storedFeature, err := service.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return Workspace{}, false, err
	}
	if strings.TrimSpace(storedFeature.AcceptedGoal) == "" || storedFeature.GoalAcceptedAt == nil {
		return Workspace{}, false, ErrGoalNotAccepted
	}
	if storedFeature.State != feature.StateDraft {
		return Workspace{}, false, ErrFeatureNotDraft
	}
	storedProject, err := service.projects.GetByID(ctx, projectID)
	if err != nil {
		return Workspace{}, false, err
	}
	if storedProject.ForgejoRepository == nil {
		return Workspace{}, false, ErrProjectRepositoryNotBound
	}

	reserved, err := service.store.GetByFeatureID(ctx, featureID)
	created := false
	if errors.Is(err, ErrNotFound) {
		reserved, created, err = service.reserve(
			ctx, storedProject, storedFeature,
		)
	}
	if err != nil {
		return Workspace{}, false, err
	}
	if reserved.ProjectID != projectID || reserved.FeatureID != featureID {
		return Workspace{}, false, ErrConflict
	}
	if reserved.Status != StatusBranchReady {
		branch, err := service.branches.EnsureBranch(
			ctx,
			reserved.RepositoryOwner,
			reserved.RepositoryName,
			reserved.Branch,
			reserved.BaseCommitID,
		)
		if err != nil {
			return Workspace{}, false, fmt.Errorf("ensure Forgejo feature branch: %w", err)
		}
		if branch.Name != reserved.Branch || branch.CommitID != reserved.BaseCommitID {
			return Workspace{}, false, ErrBranchConflict
		}
		reserved, err = service.store.MarkBranchReady(ctx, featureID, service.now().UTC())
		if err != nil {
			return Workspace{}, false, fmt.Errorf("mark feature branch ready: %w", err)
		}
	}
	if service.checkouts == nil {
		return reserved, created, nil
	}
	if reserved.CheckoutReady() && reserved.CheckoutRelativePath != reserved.ID {
		return Workspace{}, false, ErrConflict
	}
	relativePath := reserved.ID
	err = service.checkouts.Ensure(ctx, CheckoutSpec{
		WorkspaceID:     reserved.ID,
		RepositoryOwner: reserved.RepositoryOwner,
		RepositoryName:  reserved.RepositoryName,
		Branch:          reserved.Branch,
		BaseCommitID:    reserved.BaseCommitID,
		AlreadyReady:    reserved.CheckoutReady(),
	})
	if err != nil {
		return Workspace{}, false, fmt.Errorf("ensure managed checkout: %w", err)
	}
	if !reserved.CheckoutReady() {
		reserved, err = service.store.MarkCheckoutReady(
			ctx, featureID, relativePath, service.now().UTC(),
		)
		if err != nil {
			return Workspace{}, false, fmt.Errorf("mark managed checkout ready: %w", err)
		}
	}
	if service.pullRequests == nil {
		return reserved, created, nil
	}
	spec := pullRequestSpec(storedFeature, reserved)
	pullRequest, err := service.pullRequests.EnsureDraftPullRequest(
		ctx,
		reserved.RepositoryOwner,
		reserved.RepositoryName,
		spec,
	)
	if err != nil {
		return Workspace{}, false, fmt.Errorf("ensure draft Forgejo pull request: %w", err)
	}
	if err := validatePreparedPullRequest(pullRequest, spec, !reserved.PullRequestReady()); err != nil {
		return Workspace{}, false, err
	}
	if reserved.PullRequestReady() {
		if reserved.PullRequestNumber != pullRequest.Number ||
			reserved.PullRequestURL != pullRequest.URL {
			return Workspace{}, false, ErrPullRequestConflict
		}
		return reserved, false, nil
	}
	ready, err := service.store.MarkPullRequestReady(
		ctx, featureID, pullRequest.Number, pullRequest.URL, service.now().UTC(),
	)
	if err != nil {
		return Workspace{}, false, fmt.Errorf("mark draft pull request ready: %w", err)
	}
	return ready, created, nil
}

// PublishPlan reconciles every durable identity used during planning before it
// asks Forgejo to append the submitted plan. The publication marker is derived
// from the immutable session-event ID, making the external update safe to
// repeat after an uncertain response or coordinator restart.
func (service *Service) PublishPlan(
	ctx context.Context,
	projectID string,
	featureID string,
	eventID string,
	plan string,
) (Workspace, bool, error) {
	return service.reconcileSubmittedPlan(ctx, projectID, featureID, eventID, plan, true, true)
}

// VerifyPublishedPlan applies the same identity and clean-baseline checks as
// publication, but never repairs or changes Forgejo. It is the read-only gate
// used before implementation begins.
func (service *Service) VerifyPublishedPlan(
	ctx context.Context,
	projectID string,
	featureID string,
	eventID string,
	plan string,
) (Workspace, error) {
	stored, _, err := service.reconcileSubmittedPlan(
		ctx, projectID, featureID, eventID, plan, false, true,
	)
	return stored, err
}

// VerifyImplementationContinuation confirms that the original branch, pull
// request, and accepted plan are still intact without requiring the managed
// checkout to be clean. Local edits and descendant commits are expected here:
// they may be work from the previous agent turn or from the user.
func (service *Service) VerifyImplementationContinuation(
	ctx context.Context,
	projectID string,
	featureID string,
	eventID string,
	plan string,
) (Workspace, error) {
	stored, _, err := service.reconcileSubmittedPlan(
		ctx, projectID, featureID, eventID, plan, false, false,
	)
	return stored, err
}

// VerifyImplementationPublication checks only objective external facts after
// the lead has decided its implementation is ready for review. It does not
// judge code quality: it confirms that the exact clean local commit is on the
// assigned Forgejo branch, that the draft PR points to it, and that the lead's
// own Forgejo account wrote the expected audit comment.
func (service *Service) VerifyImplementationPublication(
	ctx context.Context,
	projectID string,
	featureID string,
	planEventID string,
	plan string,
	attemptID string,
	summary string,
	commitID string,
	pullRequestNumber int64,
	expectedAuthor string,
) (Workspace, error) {
	for _, value := range []string{
		planEventID, plan, attemptID, summary, commitID, expectedAuthor,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return Workspace{}, errors.New("implementation publication identities are required and must be trimmed")
		}
	}
	storedFeature, err := service.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return Workspace{}, err
	}
	if storedFeature.State != feature.StateImplementing && storedFeature.State != feature.StateReviewing {
		return Workspace{}, ErrFeatureNotPlanning
	}
	storedProject, err := service.projects.GetByID(ctx, projectID)
	if err != nil {
		return Workspace{}, err
	}
	if storedProject.ForgejoRepository == nil {
		return Workspace{}, ErrProjectRepositoryNotBound
	}
	stored, err := service.Get(ctx, projectID, featureID)
	if err != nil {
		return Workspace{}, err
	}
	repository := storedProject.ForgejoRepository
	if stored.RepositoryOwner != repository.Owner ||
		stored.RepositoryName != repository.Name ||
		stored.BaseBranch != repository.DefaultBranch ||
		!stored.CheckoutReady() || !stored.PullRequestReady() ||
		service.checkouts == nil || service.pullRequests == nil ||
		stored.PullRequestNumber != pullRequestNumber {
		return Workspace{}, ErrConflict
	}
	branch, err := service.branches.GetBranch(
		ctx, stored.RepositoryOwner, stored.RepositoryName, stored.Branch,
	)
	if err != nil {
		return Workspace{}, fmt.Errorf("verify Forgejo feature branch: %w", err)
	}
	if branch.Name != stored.Branch || branch.CommitID != commitID {
		return Workspace{}, ErrBranchConflict
	}
	if err := service.checkouts.Ensure(ctx, CheckoutSpec{
		WorkspaceID: stored.ID, RepositoryOwner: stored.RepositoryOwner,
		RepositoryName: stored.RepositoryName, Branch: stored.Branch,
		BaseCommitID: stored.BaseCommitID, ExpectedHeadCommitID: commitID,
		AlreadyReady: true, RequireClean: true,
	}); err != nil {
		return Workspace{}, fmt.Errorf("verify implemented checkout: %w", err)
	}
	planDigest := sha256.Sum256([]byte(planEventID))
	implementationDigest := sha256.Sum256([]byte(attemptID))
	pullRequest, err := service.pullRequests.VerifyPullRequestImplementation(
		ctx, stored.RepositoryOwner, stored.RepositoryName,
		ImplementationPublicationSpec{
			Number:                stored.PullRequestNumber,
			FeatureMarker:         "<!-- commitarium-feature: " + storedFeature.ID + " -->",
			PlanPublicationMarker: "<!-- commitarium-plan: " + hex.EncodeToString(planDigest[:]) + " -->",
			Plan:                  plan,
			PublicationMarker:     "<!-- commitarium-implementation: " + hex.EncodeToString(implementationDigest[:]) + " -->",
			Summary:               summary, ExpectedAuthor: expectedAuthor,
			BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch, HeadCommitID: commitID,
		},
	)
	if err != nil {
		return Workspace{}, fmt.Errorf("verify implementation pull request: %w", err)
	}
	if pullRequest.Number != stored.PullRequestNumber || pullRequest.URL != stored.PullRequestURL {
		return Workspace{}, ErrPullRequestConflict
	}
	return stored, nil
}

// VerifyImplementationReview confirms the reviewer examined the same clean
// commit that the lead published and that the claimed formal Forgejo review
// exists with the expected author, decision, and exact audit body.
func (service *Service) VerifyImplementationReview(
	ctx context.Context,
	projectID string,
	featureID string,
	planEventID string,
	plan string,
	attemptID string,
	summary string,
	commitID string,
	pullRequestNumber int64,
	reviewID int64,
	expectedAuthor string,
	expectedState string,
) (Workspace, error) {
	for _, value := range []string{
		planEventID, plan, attemptID, summary, commitID, expectedAuthor, expectedState,
	} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
			return Workspace{}, errors.New("review publication identities are required and must be trimmed")
		}
	}
	storedFeature, err := service.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return Workspace{}, err
	}
	if storedFeature.State != feature.StateReviewing {
		return Workspace{}, ErrFeatureNotReviewing
	}
	storedProject, err := service.projects.GetByID(ctx, projectID)
	if err != nil {
		return Workspace{}, err
	}
	if storedProject.ForgejoRepository == nil {
		return Workspace{}, ErrProjectRepositoryNotBound
	}
	stored, err := service.Get(ctx, projectID, featureID)
	if err != nil {
		return Workspace{}, err
	}
	repository := storedProject.ForgejoRepository
	if stored.RepositoryOwner != repository.Owner || stored.RepositoryName != repository.Name ||
		stored.BaseBranch != repository.DefaultBranch || !stored.CheckoutReady() ||
		!stored.PullRequestReady() || service.checkouts == nil || service.pullRequests == nil ||
		stored.PullRequestNumber != pullRequestNumber {
		return Workspace{}, ErrConflict
	}
	branch, err := service.branches.GetBranch(ctx, stored.RepositoryOwner, stored.RepositoryName, stored.Branch)
	if err != nil {
		return Workspace{}, fmt.Errorf("verify reviewed Forgejo branch: %w", err)
	}
	if branch.Name != stored.Branch || branch.CommitID != commitID {
		return Workspace{}, ErrBranchConflict
	}
	if err := service.checkouts.Ensure(ctx, CheckoutSpec{
		WorkspaceID: stored.ID, RepositoryOwner: stored.RepositoryOwner,
		RepositoryName: stored.RepositoryName, Branch: stored.Branch,
		BaseCommitID: stored.BaseCommitID, ExpectedHeadCommitID: commitID,
		AlreadyReady: true, RequireClean: true,
	}); err != nil {
		return Workspace{}, fmt.Errorf("verify reviewed checkout: %w", err)
	}
	planDigest := sha256.Sum256([]byte(planEventID))
	reviewDigest := sha256.Sum256([]byte(attemptID))
	pullRequest, err := service.pullRequests.VerifyPullRequestReview(
		ctx, stored.RepositoryOwner, stored.RepositoryName,
		ReviewPublicationSpec{
			Number: stored.PullRequestNumber, ReviewID: reviewID,
			FeatureMarker:         "<!-- commitarium-feature: " + storedFeature.ID + " -->",
			PlanPublicationMarker: "<!-- commitarium-plan: " + hex.EncodeToString(planDigest[:]) + " -->",
			Plan:                  plan,
			PublicationMarker:     "<!-- commitarium-review: " + hex.EncodeToString(reviewDigest[:]) + " -->",
			Summary:               summary, ExpectedAuthor: expectedAuthor, ExpectedState: expectedState,
			BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch, HeadCommitID: commitID,
		},
	)
	if err != nil {
		return Workspace{}, fmt.Errorf("verify implementation review: %w", err)
	}
	if pullRequest.Number != stored.PullRequestNumber || pullRequest.URL != stored.PullRequestURL {
		return Workspace{}, ErrPullRequestConflict
	}
	return stored, nil
}

func (service *Service) reconcileSubmittedPlan(
	ctx context.Context,
	projectID string,
	featureID string,
	eventID string,
	plan string,
	publishMissing bool,
	requireCleanBaseline bool,
) (Workspace, bool, error) {
	eventID = strings.TrimSpace(eventID)
	plan = strings.TrimSpace(plan)
	if eventID == "" || plan == "" {
		return Workspace{}, false, errors.New("submitted plan identity and content are required")
	}
	storedFeature, err := service.features.GetByID(ctx, projectID, featureID)
	if err != nil {
		return Workspace{}, false, err
	}
	if storedFeature.State != feature.StatePlanning &&
		(publishMissing || storedFeature.State != feature.StateImplementing) {
		return Workspace{}, false, ErrFeatureNotPlanning
	}
	storedProject, err := service.projects.GetByID(ctx, projectID)
	if err != nil {
		return Workspace{}, false, err
	}
	if storedProject.ForgejoRepository == nil {
		return Workspace{}, false, ErrProjectRepositoryNotBound
	}
	stored, err := service.Get(ctx, projectID, featureID)
	if err != nil {
		return Workspace{}, false, err
	}
	repository := storedProject.ForgejoRepository
	if stored.RepositoryOwner != repository.Owner ||
		stored.RepositoryName != repository.Name ||
		stored.BaseBranch != repository.DefaultBranch ||
		!stored.CheckoutReady() || !stored.PullRequestReady() ||
		service.checkouts == nil || service.pullRequests == nil {
		return Workspace{}, false, ErrConflict
	}
	branch, err := service.branches.GetBranch(
		ctx, stored.RepositoryOwner, stored.RepositoryName, stored.Branch,
	)
	if err != nil {
		return Workspace{}, false, fmt.Errorf("reconcile Forgejo feature branch: %w", err)
	}
	if branch.Name != stored.Branch || branch.CommitID != stored.BaseCommitID {
		return Workspace{}, false, ErrBranchConflict
	}
	if err := service.checkouts.Ensure(ctx, CheckoutSpec{
		WorkspaceID: stored.ID, RepositoryOwner: stored.RepositoryOwner,
		RepositoryName: stored.RepositoryName, Branch: stored.Branch,
		BaseCommitID: stored.BaseCommitID, AlreadyReady: true,
		RequireCleanBaseline: requireCleanBaseline,
	}); err != nil {
		return Workspace{}, false, fmt.Errorf("reconcile managed checkout: %w", err)
	}
	digest := sha256.Sum256([]byte(eventID))
	spec := PlanPublicationSpec{
		Number:            stored.PullRequestNumber,
		FeatureMarker:     "<!-- commitarium-feature: " + storedFeature.ID + " -->",
		PublicationMarker: "<!-- commitarium-plan: " + hex.EncodeToString(digest[:]) + " -->",
		Plan:              plan, BaseBranch: stored.BaseBranch, HeadBranch: stored.Branch,
		HeadCommitID: stored.BaseCommitID,
	}
	var pullRequest PullRequest
	published := false
	if publishMissing {
		pullRequest, published, err = service.pullRequests.EnsurePullRequestPlan(
			ctx, stored.RepositoryOwner, stored.RepositoryName, spec,
		)
	} else {
		pullRequest, err = service.pullRequests.VerifyPullRequestPlan(
			ctx, stored.RepositoryOwner, stored.RepositoryName, spec,
		)
	}
	if err != nil {
		action := "publish agreed plan to Forgejo"
		if !publishMissing {
			action = "verify agreed plan in Forgejo"
		}
		return Workspace{}, false, fmt.Errorf("%s: %w", action, err)
	}
	if pullRequest.Number != stored.PullRequestNumber || pullRequest.URL != stored.PullRequestURL {
		return Workspace{}, false, ErrPullRequestConflict
	}
	return stored, published, nil
}

func validatePreparedPullRequest(
	pullRequest PullRequest,
	spec PullRequestSpec,
	requireInitial bool,
) error {
	if err := pullRequest.Validate(); err != nil || pullRequest.State != "open" ||
		!pullRequest.Draft || pullRequest.BaseBranch != spec.BaseBranch ||
		pullRequest.HeadBranch != spec.HeadBranch ||
		!strings.Contains(pullRequest.Body, spec.FeatureMarker) {
		return ErrPullRequestConflict
	}
	if requireInitial && (pullRequest.Title != spec.Title ||
		pullRequest.HeadCommitID != spec.InitialHeadCommitID) {
		return ErrPullRequestConflict
	}
	return nil
}

func pullRequestSpec(storedFeature feature.Feature, reserved Workspace) PullRequestSpec {
	marker := "<!-- commitarium-feature: " + storedFeature.ID + " -->"
	return PullRequestSpec{
		Title:               "WIP: " + storedFeature.Title,
		Body:                marker + "\n\n## Accepted goal\n\n" + storedFeature.AcceptedGoal,
		FeatureMarker:       marker,
		BaseBranch:          reserved.BaseBranch,
		HeadBranch:          reserved.Branch,
		InitialHeadCommitID: reserved.BaseCommitID,
		ExistingNumber:      reserved.PullRequestNumber,
	}
}

func (service *Service) reserve(
	ctx context.Context,
	storedProject project.Project,
	storedFeature feature.Feature,
) (Workspace, bool, error) {
	repository := storedProject.ForgejoRepository
	base, err := service.branches.GetBranch(
		ctx, repository.Owner, repository.Name, repository.DefaultBranch,
	)
	if err != nil {
		return Workspace{}, false, fmt.Errorf("get Forgejo default branch: %w", err)
	}
	if err := base.Validate(); err != nil || base.Name != repository.DefaultBranch {
		return Workspace{}, false, fmt.Errorf("get Forgejo default branch: invalid branch response")
	}
	now := service.now().UTC()
	reservation := Workspace{
		ID:        "wsp_" + storedFeature.ID,
		ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: repository.Owner, RepositoryName: repository.Name,
		BaseBranch: repository.DefaultBranch,
		Branch:     "commitarium/" + storedFeature.ID, BaseCommitID: base.CommitID,
		Status: StatusPreparing, CreatedAt: now, UpdatedAt: now,
	}
	reserved, created, err := service.store.Reserve(ctx, reservation)
	if err != nil {
		return Workspace{}, false, fmt.Errorf("reserve feature workspace: %w", err)
	}
	return reserved, created, nil
}
