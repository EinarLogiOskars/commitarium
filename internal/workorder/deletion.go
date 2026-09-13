package workorder

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

var ErrActive = errors.New("work order has active agent work")
var ErrUnsafeArtifacts = errors.New("work order artifacts are unsafe to delete")

type Deletion struct {
	Feature   feature.Feature
	Workspace *workspace.Workspace
}

type Result struct {
	ProjectID           string `json:"project_id"`
	FeatureID           string `json:"feature_id"`
	Deleted             bool   `json:"deleted"`
	MergedChangesRemain bool   `json:"merged_changes_remain"`
}

type Store interface {
	BeginDeletion(ctx context.Context, projectID, featureID string) (Deletion, error)
	FinishDeletion(ctx context.Context, projectID, featureID string) error
}

type ForgejoCleaner interface {
	ClosePullRequest(ctx context.Context, owner, repository string, number int64, baseBranch, headBranch, featureMarker string) error
	DeleteBranch(ctx context.Context, owner, repository, branch string) error
}

type CheckoutCleaner interface {
	Remove(ctx context.Context, workspaceID string) error
}

type Service struct {
	store     Store
	forgejo   ForgejoCleaner
	checkouts CheckoutCleaner
}

func NewService(store Store, forgejo ForgejoCleaner, checkouts CheckoutCleaner) *Service {
	return &Service{store: store, forgejo: forgejo, checkouts: checkouts}
}

// Delete removes only isolated work-order artifacts. In particular, it never
// names the default branch in a mutating operation. Completed work is not
// reverted; its already-merged default-branch changes remain.
func (service *Service) Delete(
	ctx context.Context,
	projectID string,
	featureID string,
) (Result, error) {
	deletion, err := service.store.BeginDeletion(ctx, projectID, featureID)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		ProjectID: projectID, FeatureID: featureID, Deleted: true,
		MergedChangesRemain: deletion.Feature.State == feature.StateCompleted,
	}
	if deletion.Workspace != nil {
		stored := *deletion.Workspace
		if err := validateArtifacts(deletion.Feature, stored); err != nil {
			return Result{}, err
		}
		if stored.PullRequestNumber > 0 && deletion.Feature.State != feature.StateCompleted {
			if service.forgejo == nil {
				return Result{}, ErrUnsafeArtifacts
			}
			if err := service.forgejo.ClosePullRequest(
				ctx, stored.RepositoryOwner, stored.RepositoryName,
				stored.PullRequestNumber, stored.BaseBranch, stored.Branch,
				"<!-- commitarium-feature: "+deletion.Feature.ID+" -->",
			); err != nil {
				return Result{}, fmt.Errorf("close managed pull request: %w", err)
			}
		}
		if stored.BranchCreatedAt != nil {
			if service.forgejo == nil {
				return Result{}, ErrUnsafeArtifacts
			}
			if err := service.forgejo.DeleteBranch(
				ctx, stored.RepositoryOwner, stored.RepositoryName, stored.Branch,
			); err != nil {
				return Result{}, fmt.Errorf("delete managed feature branch: %w", err)
			}
		}
		if stored.CheckoutCreatedAt != nil {
			if service.checkouts == nil {
				return Result{}, ErrUnsafeArtifacts
			}
			if err := service.checkouts.Remove(ctx, stored.ID); err != nil {
				return Result{}, fmt.Errorf("remove managed checkout: %w", err)
			}
		}
	}
	if err := service.store.FinishDeletion(ctx, projectID, featureID); err != nil {
		return Result{}, err
	}
	return result, nil
}

func validateArtifacts(storedFeature feature.Feature, stored workspace.Workspace) error {
	if err := stored.Validate(); err != nil {
		return fmt.Errorf("%w: invalid workspace record: %v", ErrUnsafeArtifacts, err)
	}
	if stored.ProjectID != storedFeature.ProjectID || stored.FeatureID != storedFeature.ID ||
		strings.EqualFold(stored.Branch, stored.BaseBranch) {
		return ErrUnsafeArtifacts
	}
	if storedFeature.State == feature.StateCompleted &&
		(stored.MergeCommitID == "" || stored.MergedAt == nil) {
		return fmt.Errorf("%w: completed work order has no durable merge", ErrUnsafeArtifacts)
	}
	return nil
}
