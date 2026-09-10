package workspace

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("feature workspace not found")
var ErrConflict = errors.New("feature workspace conflicts with its existing reservation")

type Store interface {
	GetByFeatureID(ctx context.Context, featureID string) (Workspace, error)
	Reserve(ctx context.Context, workspace Workspace) (Workspace, bool, error)
	MarkBranchReady(ctx context.Context, featureID string, readyAt time.Time) (Workspace, error)
	MarkCheckoutReady(
		ctx context.Context,
		featureID string,
		relativePath string,
		readyAt time.Time,
	) (Workspace, error)
	MarkPullRequestReady(
		ctx context.Context,
		featureID string,
		number int64,
		url string,
		createdAt time.Time,
	) (Workspace, error)
}

var ErrPublicationNotFound = errors.New("workspace publication not found")
var ErrPublicationConflict = errors.New("workspace publication conflicts with durable state")
var ErrNoChanges = errors.New("managed workspace has no changes to publish")

type PublicationStore interface {
	GetPublication(ctx context.Context, runID, idempotencyKey string) (Publication, error)
	ReservePublication(ctx context.Context, publication Publication) (Publication, bool, error)
	CompletePublication(ctx context.Context, publicationID string, completedAt time.Time) (Publication, error)
	ActivePublication(ctx context.Context, workspaceID string) (Publication, error)
}
