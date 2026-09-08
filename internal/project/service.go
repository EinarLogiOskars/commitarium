package project

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNameRequired = errors.New("project name is required")

type Service struct {
	store      Store
	generateID func() string
	now        func() time.Time
}

func NewService(store Store) *Service {
	return &Service{
		store: store,
		generateID: func() string {
			return "prj_" + rand.Text()
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) Create(
	ctx context.Context,
	name string,
	recoveryPolicy RecoveryPolicy,
) (Project, error) {
	sanitizedName := strings.TrimSpace(name)

	if sanitizedName == "" {
		return Project{}, ErrNameRequired
	}

	policy, err := NormalizeRecoveryPolicy(recoveryPolicy)
	if err != nil {
		return Project{}, err
	}

	project := Project{
		ID:             s.generateID(),
		Name:           sanitizedName,
		RecoveryPolicy: policy,
		CreatedAt:      s.now(),
	}

	if err := s.store.Create(ctx, project); err != nil {
		return Project{}, fmt.Errorf("store project: %w", err)
	}

	return project, nil
}

func (s *Service) GetByID(
	ctx context.Context,
	id string,
) (Project, error) {
	project, err := s.store.GetByID(ctx, id)
	if err != nil {
		return Project{}, fmt.Errorf(
			"get project %q: %w",
			id,
			err,
		)
	}

	return project, nil
}
