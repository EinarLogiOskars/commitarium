package feature

import (
	"context"
	"errors"
)

var ErrAlreadyExists = errors.New("feature already exists")
var ErrNotFound = errors.New("feature not found")

type Store interface {
	Create(ctx context.Context, feature Feature) error
	GetByID(ctx context.Context, id string) (Feature, error)
}
