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
	project Project,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.projects[project.ID]; exists {
		return ErrAlreadyExists
	}

	s.projects[project.ID] = project
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
