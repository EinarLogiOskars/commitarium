package project

import (
	"context"
	"errors"
	"strings"
)

// ErrRepositoryFileNotFound means the default branch has no file at the path.
var ErrRepositoryFileNotFound = errors.New("repository file not found")

// ReadRepositoryFile reads one file from the project's default branch by its
// path, such as ".commitarium/stack.json".
func (s *Service) ReadRepositoryFile(
	ctx context.Context,
	projectID string,
	filePath string,
	maxBytes int64,
) ([]byte, error) {
	storedProject, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if storedProject.ForgejoRepository == nil {
		return nil, ErrForgejoRepositoryNotReady
	}
	treeReader, treeOK := s.repositoryOverviewReader.(RepositoryTreeReader)
	blobReader, blobOK := s.repositoryOverviewReader.(RepositoryBlobReader)
	if !treeOK || !blobOK {
		return nil, ErrForgejoUnavailable
	}
	repository := *storedProject.ForgejoRepository
	snapshot, err := treeReader.ReadRepositoryTree(ctx, repository.Owner, repository.Name, repository.DefaultBranch)
	if err != nil {
		return nil, err
	}
	filePath = strings.TrimPrefix(filePath, "/")
	for _, entry := range snapshot.Tree {
		if entry.Type != "file" || entry.Path != filePath {
			continue
		}
		if entry.Size > maxBytes {
			return nil, ErrRepositoryContentTooLarge
		}
		return blobReader.ReadRepositoryBlob(ctx, repository.Owner, repository.Name, entry.BlobID, maxBytes)
	}
	return nil, ErrRepositoryFileNotFound
}
