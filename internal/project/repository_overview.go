package project

import (
	"context"
	"errors"
	"strings"
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

// HasCode reports whether the repository holds more than the files a new
// repository starts with, such as a README, a license, or dotfiles.
func (overview RepositoryOverview) HasCode() bool {
	for _, entry := range overview.Tree {
		name := strings.ToLower(entry.Path)
		if !strings.HasPrefix(name, "readme") && !strings.HasPrefix(name, "license") &&
			!strings.HasPrefix(name, "licence") && !strings.HasPrefix(name, ".") {
			return true
		}
	}
	return false
}

// composeFileNames are the compose files a preview runs from (ADR-014).
var composeFileNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}

// HasCompose reports whether the repository has a compose file at its root,
// which the app's preview runs.
func (overview RepositoryOverview) HasCompose() bool {
	for _, entry := range overview.Tree {
		for _, name := range composeFileNames {
			if entry.Path == name {
				return true
			}
		}
	}
	return false
}
