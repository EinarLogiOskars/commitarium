package feature

import (
	"context"
	"sort"
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

func (s *MemoryStore) ListByProjectID(
	_ context.Context,
	projectID string,
) ([]Feature, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	features := make([]Feature, 0)
	for _, storedFeature := range s.features {
		if storedFeature.ProjectID == projectID {
			features = append(features, storedFeature)
		}
	}
	sort.Slice(features, func(left, right int) bool {
		if features[left].UpdatedAt.Equal(features[right].UpdatedAt) {
			if features[left].CreatedAt.Equal(features[right].CreatedAt) {
				return features[left].ID < features[right].ID
			}
			return features[left].CreatedAt.After(features[right].CreatedAt)
		}
		return features[left].UpdatedAt.After(features[right].UpdatedAt)
	})
	return features, nil
}
