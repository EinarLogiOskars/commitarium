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
	MarkMergeReady(ctx context.Context, featureID, approvedCommitID string, readyAt time.Time) (Workspace, error)
	MarkMerged(ctx context.Context, featureID, approvedCommitID, mergeCommitID string, mergedAt, recordedAt time.Time) (Workspace, error)
}
