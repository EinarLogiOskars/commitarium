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
	store              Store
	repositoryVerifier RepositoryVerifier
	generateID         func() string
	now                func() time.Time
}

func NewService(store Store) *Service {
	return NewServiceWithRepositoryVerifier(store, unavailableRepositoryVerifier{})
}

func NewServiceWithRepositoryVerifier(
	store Store,
	verifier RepositoryVerifier,
) *Service {
	if verifier == nil {
		verifier = unavailableRepositoryVerifier{}
	}
	return &Service{
		store:              store,
		repositoryVerifier: verifier,
		generateID: func() string {
			return "prj_" + rand.Text()
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

type unavailableRepositoryVerifier struct{}

func (unavailableRepositoryVerifier) VerifyRepository(
	context.Context,
	string,
	string,
) (ForgejoRepository, error) {
	return ForgejoRepository{}, ErrForgejoUnavailable
}

func (s *Service) Create(
	ctx context.Context,
	name string,
	recoveryPolicy RecoveryPolicy,
	dialogueLimits DialogueLimits,
) (Project, error) {
	sanitizedName := strings.TrimSpace(name)

	if sanitizedName == "" {
		return Project{}, ErrNameRequired
	}

	policy, err := NormalizeRecoveryPolicy(recoveryPolicy)
	if err != nil {
		return Project{}, err
	}
	if err := dialogueLimits.Validate(); err != nil {
		return Project{}, err
	}

	project := Project{
		ID:             s.generateID(),
		Name:           sanitizedName,
		RecoveryPolicy: policy,
		DialogueLimits: dialogueLimits,
		CreatedAt:      s.now(),
	}

	if err := s.store.Create(ctx, project); err != nil {
		return Project{}, fmt.Errorf("store project: %w", err)
	}

	return project, nil
}

func (s *Service) UpdateDialogueLimits(
	ctx context.Context,
	projectID string,
	limits DialogueLimits,
) (Project, error) {
	if err := limits.Validate(); err != nil {
		return Project{}, err
	}
	updated, err := s.store.UpdateDialogueLimits(ctx, projectID, limits)
	if err != nil {
		return Project{}, fmt.Errorf("update dialogue limits for project %q: %w", projectID, err)
	}
	return updated, nil
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

func (s *Service) List(ctx context.Context) ([]Project, error) {
	projects, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

func (s *Service) BindForgejoRepository(
	ctx context.Context,
	projectID string,
	owner string,
	name string,
) (Project, error) {
	owner, name, err := NormalizeRepositoryCoordinate(owner, name)
	if err != nil {
		return Project{}, err
	}
	storedProject, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return Project{}, fmt.Errorf("get project %q before Forgejo binding: %w", projectID, err)
	}
	if storedProject.ForgejoRepository != nil {
		if sameRepositoryCoordinate(*storedProject.ForgejoRepository, owner, name) {
			return storedProject, nil
		}
		return Project{}, ErrForgejoRepositoryAlreadyBound
	}

	repository, err := s.repositoryVerifier.VerifyRepository(ctx, owner, name)
	if err != nil {
		return Project{}, fmt.Errorf("verify Forgejo repository %q/%q: %w", owner, name, err)
	}
	repository.BoundAt = s.now().UTC()
	if err := repository.Validate(); err != nil {
		return Project{}, fmt.Errorf("verify Forgejo repository %q/%q: %w", owner, name, err)
	}
	boundProject, err := s.store.BindForgejoRepository(ctx, projectID, repository)
	if err != nil {
		return Project{}, fmt.Errorf("bind Forgejo repository to project %q: %w", projectID, err)
	}
	return boundProject, nil
}
