package project

import (
	"context"
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
	createdProject.RecoveryPolicy = policy
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.projects[createdProject.ID]; exists {
		return ErrAlreadyExists
	}

	s.projects[createdProject.ID] = createdProject
	return nil
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

	return project, nil
}
