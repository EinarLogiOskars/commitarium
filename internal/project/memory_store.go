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
