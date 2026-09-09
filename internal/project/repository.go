package project

import (
	"context"
	"errors"
	"strings"
)

var ErrForgejoOwnerRequired = errors.New("Forgejo repository owner is required")
var ErrForgejoRepositoryNameRequired = errors.New("Forgejo repository name is required")
var ErrInvalidForgejoRepositoryCoordinate = errors.New("Forgejo repository owner and name must be single path segments")
var ErrForgejoRepositoryNotFound = errors.New("Forgejo repository not found")
var ErrForgejoRepositoryNotReady = errors.New("Forgejo repository has no usable default branch")
var ErrForgejoUnavailable = errors.New("Forgejo repository verification is unavailable")

type RepositoryVerifier interface {
	VerifyRepository(ctx context.Context, owner, name string) (ForgejoRepository, error)
}

func NormalizeRepositoryCoordinate(owner, name string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	switch {
	case owner == "":
		return "", "", ErrForgejoOwnerRequired
	case name == "":
		return "", "", ErrForgejoRepositoryNameRequired
	case strings.ContainsAny(owner, "/\\") || strings.ContainsAny(name, "/\\"):
		return "", "", ErrInvalidForgejoRepositoryCoordinate
	default:
		return owner, name, nil
	}
}

func sameRepositoryCoordinate(repository ForgejoRepository, owner, name string) bool {
	return strings.EqualFold(repository.Owner, owner) && strings.EqualFold(repository.Name, name)
}

func (repository ForgejoRepository) Validate() error {
	owner, name, err := NormalizeRepositoryCoordinate(repository.Owner, repository.Name)
	if err != nil {
		return err
	}
	if owner != repository.Owner || name != repository.Name {
		return errors.New("Forgejo repository owner and name must be trimmed")
	}
	if strings.TrimSpace(repository.DefaultBranch) == "" ||
		repository.DefaultBranch != strings.TrimSpace(repository.DefaultBranch) {
		return ErrForgejoRepositoryNotReady
	}
	if repository.BoundAt.IsZero() {
		return errors.New("Forgejo repository binding time is required")
	}
	return nil
}
