package project

import (
	"context"
	"errors"
)

type Store interface {
	Create(ctx context.Context, project Project) error
	GetByID(ctx context.Context, id string) (Project, error)
}

var ErrAlreadyExists = errors.New("project already exists")
var ErrNotFound = errors.New("project not found")
