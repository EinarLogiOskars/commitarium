package project

import (
	"context"
	"errors"
)

type Store interface {
	Create(ctx context.Context, project Project) error
	GetByID(ctx context.Context, id string) (Project, error)
	List(ctx context.Context) ([]Project, error)
	BindForgejoRepository(
		ctx context.Context,
		projectID string,
		repository ForgejoRepository,
	) (Project, error)
}

var ErrAlreadyExists = errors.New("project already exists")
var ErrNotFound = errors.New("project not found")
var ErrForgejoRepositoryAlreadyBound = errors.New("project already has a different Forgejo repository")
