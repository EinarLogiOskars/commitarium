package project

import (
	"context"
	"sort"
	"sync"
)

type MemoryStore struct {
	mu       sync.RWMutex
	projects map[string]Project
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		projects: make(map[string]Project),
	}
}

func (s *MemoryStore) Create(
	_ context.Context,
	createdProject Project,
) error {
	policy, err := NormalizeRecoveryPolicy(createdProject.RecoveryPolicy)
	if err != nil {
		return err
	}
	if err := createdProject.DialogueLimits.Validate(); err != nil {
		return err
	}
	providers, err := createdProject.AgentProviders.Normalize()
	if err != nil {
		return err
	}
	createdProject.AgentProviders = providers
	mergePolicy, err := NormalizeMergePolicy(createdProject.MergePolicy)
	if err != nil {
		return err
	}
	createdProject.MergePolicy = mergePolicy
	createdProject.RecoveryPolicy = policy
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.projects[createdProject.ID]; exists {
		return ErrAlreadyExists
	}

	s.projects[createdProject.ID] = cloneProject(createdProject)
	return nil
}

func (s *MemoryStore) UpdateMergePolicy(
	_ context.Context,
	projectID string,
	policy MergePolicy,
) (Project, error) {
	normalized, err := NormalizeMergePolicy(policy)
	if err != nil {
		return Project{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	storedProject, exists := s.projects[projectID]
	if !exists {
		return Project{}, ErrNotFound
	}
	storedProject.MergePolicy = normalized
	s.projects[projectID] = cloneProject(storedProject)
	return cloneProject(storedProject), nil
}

func (s *MemoryStore) UpdateAgentProviders(
	_ context.Context,
	projectID string,
	providers AgentProviders,
) (Project, error) {
	normalized, err := providers.Normalize()
	if err != nil {
		return Project{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	storedProject, exists := s.projects[projectID]
	if !exists {
		return Project{}, ErrNotFound
	}
	storedProject.AgentProviders = normalized
	s.projects[projectID] = cloneProject(storedProject)
	return cloneProject(storedProject), nil
}

func (s *MemoryStore) UpdateDialogueLimits(
	_ context.Context,
	projectID string,
	limits DialogueLimits,
) (Project, error) {
	if err := limits.Validate(); err != nil {
		return Project{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	storedProject, exists := s.projects[projectID]
	if !exists {
		return Project{}, ErrNotFound
	}
	storedProject.DialogueLimits = limits
	s.projects[projectID] = cloneProject(storedProject)
	return cloneProject(storedProject), nil
}

func (s *MemoryStore) GetByID(
	_ context.Context,
	id string,
) (Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	project, exists := s.projects[id]
	if !exists {
		return Project{}, ErrNotFound
	}

	return cloneProject(project), nil
}

func (s *MemoryStore) List(_ context.Context) ([]Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	projects := make([]Project, 0, len(s.projects))
	for _, storedProject := range s.projects {
		projects = append(projects, cloneProject(storedProject))
	}
	sort.Slice(projects, func(left, right int) bool {
		if projects[left].CreatedAt.Equal(projects[right].CreatedAt) {
			return projects[left].ID < projects[right].ID
		}
		return projects[left].CreatedAt.Before(projects[right].CreatedAt)
	})
	return projects, nil
}

func (s *MemoryStore) BindForgejoRepository(
	_ context.Context,
	projectID string,
	repository ForgejoRepository,
) (Project, error) {
	if err := repository.Validate(); err != nil {
		return Project{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	storedProject, exists := s.projects[projectID]
	if !exists {
		return Project{}, ErrNotFound
	}
	if storedProject.ForgejoRepository != nil {
		if sameRepositoryCoordinate(
			*storedProject.ForgejoRepository,
			repository.Owner,
			repository.Name,
		) {
			return cloneProject(storedProject), nil
		}
		return Project{}, ErrForgejoRepositoryAlreadyBound
	}
	storedProject.ForgejoRepository = &repository
	s.projects[projectID] = cloneProject(storedProject)
	return cloneProject(storedProject), nil
}

func cloneProject(project Project) Project {
	if project.ForgejoRepository != nil {
		repository := *project.ForgejoRepository
		project.ForgejoRepository = &repository
	}
	return project
}
