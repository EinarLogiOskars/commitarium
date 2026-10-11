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

// Rename changes a work order's title.
func (s *MemoryStore) Rename(_ context.Context, id string, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.features[id]
	if !exists {
		return ErrNotFound
	}
	stored.Title = title
	s.features[id] = stored
	return nil
}

// UpdateReadySettings saves the settings a Ready work order starts with.
func (s *MemoryStore) UpdateReadySettings(_ context.Context, chosen Feature) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.features[chosen.ID]
	if !exists {
		return ErrNotFound
	}
	if stored.State != StateReady {
		return ErrNotReady
	}
	stored.AgentProviders, stored.AgentModels = chosen.AgentProviders, chosen.AgentModels
	stored.MergePolicy, stored.AutonomyPolicy = chosen.MergePolicy, chosen.AutonomyPolicy
	stored.IndependentAcceptanceTests = chosen.IndependentAcceptanceTests
	stored.Steps = chosen.Steps
	s.features[chosen.ID] = stored
	return nil
}
