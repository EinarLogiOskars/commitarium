package projectdeletion

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
)

var (
	ErrActive          = errors.New("project has active agent work")
	ErrConflict        = errors.New("project deletion idempotency conflict")
	ErrUnsafeArtifacts = errors.New("project artifacts are unsafe to delete")
	ErrUnavailable     = errors.New("project deletion is temporarily unavailable")
)

type Deletion struct {
	ProjectID      string
	IdempotencyKey string
	Force          bool
	Repository     *project.ForgejoRepository
	Completed      bool
}

type Result struct {
	ProjectID string `json:"project_id"`
	Deleted   bool   `json:"deleted"`
}

type Store interface {
	BeginDeletion(context.Context, string, string, bool) (Deletion, error)
	ReleaseDeletion(context.Context, string, string) error
	ListFeatureIDs(context.Context, string) ([]string, error)
	FinishDeletion(context.Context, string, string) error
}

type FeatureDeleter interface {
	Delete(context.Context, string, string) (workorder.Result, error)
}

type RunTerminator interface {
	StopFeatureRuns(context.Context, string, string) error
}

type ToolchainCleaner interface {
	DeleteProject(context.Context, string) error
}

type AssistantCleaner interface {
	DeleteProject(context.Context, string, string, bool) error
}

type RepositoryCleaner interface {
	DeleteRepository(context.Context, string, string) error
}

type Service struct {
	store        Store
	features     FeatureDeleter
	runs         RunTerminator
	toolchains   ToolchainCleaner
	assistants   AssistantCleaner
	repositories RepositoryCleaner
	mu           sync.Mutex
}

func NewService(
	store Store,
	features FeatureDeleter,
	runs RunTerminator,
	toolchains ToolchainCleaner,
	assistants AssistantCleaner,
	repositories RepositoryCleaner,
) *Service {
	return &Service{store: store, features: features, runs: runs, toolchains: toolchains,
		assistants: assistants, repositories: repositories}
}

// Delete resumes the same durable teardown after any interrupted external
// step. The project row is intentionally the last coordinator-owned record
// removed, after Forgejo confirms the exact private repository is gone.
func (service *Service) Delete(
	ctx context.Context,
	projectID string,
	idempotencyKey string,
	force bool,
) (Result, error) {
	service.mu.Lock()
	defer service.mu.Unlock()

	deletion, err := service.store.BeginDeletion(ctx, projectID, idempotencyKey, force)
	if err != nil {
		return Result{}, err
	}
	result := Result{ProjectID: projectID, Deleted: true}
	if deletion.Completed {
		return result, nil
	}

	featureIDs, err := service.store.ListFeatureIDs(ctx, projectID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: list project work orders: %v", ErrUnavailable, err)
	}
	if force {
		if service.runs == nil {
			return Result{}, fmt.Errorf("%w: active-run termination is unavailable", ErrUnavailable)
		}
		for _, featureID := range featureIDs {
			if err := service.runs.StopFeatureRuns(ctx, featureID, idempotencyKey); err != nil {
				return Result{}, fmt.Errorf("%w: stop work order %q: %v", ErrUnavailable, featureID, err)
			}
		}
	}
	if service.assistants != nil {
		if err := service.assistants.DeleteProject(ctx, projectID, idempotencyKey, force); err != nil {
			if errors.Is(err, ErrActive) {
				if releaseErr := service.store.ReleaseDeletion(context.WithoutCancel(ctx), projectID, idempotencyKey); releaseErr != nil {
					return Result{}, fmt.Errorf("%w: release active deletion claim: %v", ErrUnavailable, releaseErr)
				}
				return Result{}, err
			}
			return Result{}, fmt.Errorf("%w: remove setup assistants: %v", ErrUnavailable, err)
		}
	}

	for _, featureID := range featureIDs {
		if service.features == nil {
			return Result{}, fmt.Errorf("%w: work-order cleanup is unavailable", ErrUnavailable)
		}
		if _, err := service.features.Delete(ctx, projectID, featureID); err != nil {
			return Result{}, err
		}
	}
	if service.toolchains != nil {
		if err := service.toolchains.DeleteProject(ctx, projectID); err != nil {
			return Result{}, fmt.Errorf("%w: remove project toolchain: %v", ErrUnavailable, err)
		}
	}
	if deletion.Repository != nil {
		if service.repositories == nil {
			return Result{}, fmt.Errorf("%w: Forgejo cleanup is unavailable", ErrUnavailable)
		}
		if err := service.repositories.DeleteRepository(
			ctx, deletion.Repository.Owner, deletion.Repository.Name,
		); err != nil {
			return Result{}, err
		}
	}
	if err := service.store.FinishDeletion(ctx, projectID, idempotencyKey); err != nil {
		return Result{}, fmt.Errorf("%w: finish project cleanup: %v", ErrUnavailable, err)
	}
	return result, nil
}
