package execution

import (
	"context"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// LimitWait names the agent whose provider usage limit a paused run waits on,
// and when the limit resets, when the provider said.
type LimitWait struct {
	RunID     string
	AgentID   string
	Role      worker.Role
	ResetsAt  *time.Time
	CreatedAt time.Time
}

// LimitWaitStore is implemented by stores that record runs waiting for an
// agent's usage limit to reset.
type LimitWaitStore interface {
	RecordLimitWait(ctx context.Context, wait LimitWait) error
	ClearLimitWait(ctx context.Context, runID string) error
	LimitWaitForRun(ctx context.Context, runID string) (LimitWait, error)
	LimitWaits(ctx context.Context) ([]LimitWait, error)
}

// RecordLimitWait notes that a run waits for an agent's usage limit to reset.
func (s *Service) RecordLimitWait(ctx context.Context, runID, agentID string, role worker.Role, resetsAt *time.Time) error {
	if waits, ok := s.store.(LimitWaitStore); ok {
		return waits.RecordLimitWait(ctx, LimitWait{
			RunID: runID, AgentID: agentID, Role: role, ResetsAt: resetsAt, CreatedAt: s.now().UTC(),
		})
	}
	return nil
}

// ClearLimitWait forgets a run's limit wait, if any.
func (s *Service) ClearLimitWait(ctx context.Context, runID string) error {
	if waits, ok := s.store.(LimitWaitStore); ok {
		return waits.ClearLimitWait(ctx, runID)
	}
	return nil
}

// LimitWaitForRun returns the limit a run waits on, if any.
func (s *Service) LimitWaitForRun(ctx context.Context, runID string) (LimitWait, error) {
	if waits, ok := s.store.(LimitWaitStore); ok {
		return waits.LimitWaitForRun(ctx, runID)
	}
	return LimitWait{}, nil
}

// LimitWaits lists every run waiting on a usage limit.
func (s *Service) LimitWaits(ctx context.Context) ([]LimitWait, error) {
	if waits, ok := s.store.(LimitWaitStore); ok {
		return waits.LimitWaits(ctx)
	}
	return nil, nil
}
