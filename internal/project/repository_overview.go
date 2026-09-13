package project

import (
	"context"
	"errors"
	"time"
)

const RepositoryReadmeMaxBytes int64 = 128 * 1024

var ErrRepositoryContentTooLarge = errors.New("repository content is too large")

type RepositoryOverviewReader interface {
	ReadRepositoryOverview(
		ctx context.Context,
		owner string,
		name string,
		defaultBranch string,
	) (RepositoryOverview, error)
}

type RepositoryOverview struct {
	DefaultBranch  string
	Head           RepositoryHead
	ReadmeMarkdown *string
	Tree           []RepositoryTreeEntry
}

type RepositoryHead struct {
	CommitID    string
	Message     string
	Author      string
	CommittedAt time.Time
}

type RepositoryTreeEntry struct {
	Path string
	Type string
}

type unavailableRepositoryOverviewReader struct{}

func (unavailableRepositoryOverviewReader) ReadRepositoryOverview(
	context.Context,
	string,
	string,
	string,
) (RepositoryOverview, error) {
	return RepositoryOverview{}, ErrForgejoUnavailable
}
