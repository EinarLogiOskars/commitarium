package project

import (
	"context"
	"errors"
	"time"
)

const RepositoryReadmeMaxBytes int64 = 128 * 1024
const RepositoryDetectionFileMaxBytes int64 = 64 * 1024
const RepositoryToolchainEvidenceMaxBytes int64 = 128 * 1024

var ErrRepositoryContentTooLarge = errors.New("repository content is too large")

type RepositoryOverviewReader interface {
	ReadRepositoryOverview(
		ctx context.Context,
		owner string,
		name string,
		defaultBranch string,
	) (RepositoryOverview, error)
}

type RepositoryBlobReader interface {
	ReadRepositoryBlob(context.Context, string, string, string, int64) ([]byte, error)
}

type RepositoryTreeReader interface {
	ReadRepositoryTree(
		ctx context.Context,
		owner string,
		name string,
		defaultBranch string,
	) (RepositoryTreeSnapshot, error)
}

type RepositoryOverview struct {
	URL            string
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
	Path   string
	Type   string
	BlobID string
	Size   int64
}

type RepositoryTreeSnapshot struct {
	DefaultBranch string
	Head          RepositoryHead
	Tree          []RepositoryTreeEntry
}

type RepositoryToolchainEvidence struct {
	DefaultBranch  string                   `json:"default_branch"`
	CommitID       string                   `json:"commit_id"`
	Files          []RepositoryEvidenceFile `json:"files"`
	LanguageCounts map[string]int           `json:"language_file_counts"`
}

type RepositoryEvidenceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
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
