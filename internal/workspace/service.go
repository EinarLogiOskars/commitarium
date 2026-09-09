package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

var ErrGoalNotAccepted = errors.New("feature goal has not been accepted")
var ErrFeatureNotDraft = errors.New("feature is no longer a draft")
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
