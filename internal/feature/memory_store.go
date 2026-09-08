package feature

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu       sync.RWMutex
	features map[string]Feature
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		features: make(map[string]Feature),
	}
}

func (s *MemoryStore) Create(
	_ context.Context,
	createdFeature Feature,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.features[createdFeature.ID]; exists {
		return ErrAlreadyExists
	}

	s.features[createdFeature.ID] = createdFeature
	return nil
}

func (s *MemoryStore) GetByID(
	_ context.Context,
	id string,
) (Feature, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	storedFeature, exists := s.features[id]
	if !exists {
		return Feature{}, ErrNotFound
	}

	return storedFeature, nil
}
