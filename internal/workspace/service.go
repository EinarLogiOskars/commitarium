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

type Service struct {
	store    Store
	features FeatureFinder
	projects ProjectFinder
	branches BranchManager
	now      func() time.Time
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

func (service *Service) PrepareBranch(
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
	if reserved.Status == StatusBranchReady {
		return reserved, false, nil
	}
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
	ready, err := service.store.MarkBranchReady(ctx, featureID, service.now().UTC())
	if err != nil {
		return Workspace{}, false, fmt.Errorf("mark feature branch ready: %w", err)
	}
	return ready, created, nil
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
