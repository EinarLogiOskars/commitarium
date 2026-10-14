package previewcheck

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore keeps checks in memory, for tests of the packages that use
// them.
type MemoryStore struct {
	mu     sync.Mutex
	checks map[string]Check
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{checks: map[string]Check{}}
}

func (store *MemoryStore) Create(_ context.Context, check Check) (Check, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if stored, ok := store.checks[check.ID]; ok {
		if stored.RunID != check.RunID || stored.CommitID != check.CommitID {
			return Check{}, false, ErrConflict
		}
		return stored, false, nil
	}
	store.checks[check.ID] = check
	return check, true, nil
}

func (store *MemoryStore) Get(_ context.Context, id string) (Check, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	check, ok := store.checks[id]
	if !ok {
		return Check{}, ErrNotFound
	}
	return check, nil
}

func (store *MemoryStore) ListByRun(_ context.Context, runID string) ([]Check, error) {
	return store.list(func(check Check) bool { return check.RunID == runID }), nil
}

func (store *MemoryStore) ListUnfinished(context.Context) ([]Check, error) {
	return store.list(func(check Check) bool {
		return check.Status == StatusPending || check.Status == StatusRunning
	}), nil
}

func (store *MemoryStore) list(keep func(Check) bool) []Check {
	store.mu.Lock()
	defer store.mu.Unlock()
	checks := make([]Check, 0)
	for _, check := range store.checks {
		if keep(check) {
			checks = append(checks, check)
		}
	}
	sort.Slice(checks, func(i, j int) bool {
		if !checks[i].CreatedAt.Equal(checks[j].CreatedAt) {
			return checks[i].CreatedAt.Before(checks[j].CreatedAt)
		}
		return checks[i].ID < checks[j].ID
	})
	return checks
}

func (store *MemoryStore) Transition(
	_ context.Context, id string, from, to Status, evidence *Evidence, at time.Time,
) (Check, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	check, ok := store.checks[id]
	if !ok {
		return Check{}, false, ErrNotFound
	}
	if check.Status == to {
		return check, false, nil
	}
	if check.Status != from {
		return Check{}, false, ErrConflict
	}
	check.Status, check.UpdatedAt = to, at
	if to == StatusRunning {
		check.ClaimedAt = &at
	}
	if to.terminal() {
		check.CompletedAt = &at
		check.Evidence = evidence
	}
	store.checks[id] = check
	return check, true, nil
}
