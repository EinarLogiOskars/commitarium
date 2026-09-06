package project

import (
	"context"
	"errors"
)

type Store interface {
	Create(ctx context.Context, project Project) error
}

var ErrAlreadyExists = errors.New("project already exists")
