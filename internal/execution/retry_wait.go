package execution

import (
	"context"
	"time"
)

// RetryWait is a run's turn that infrastructure cut off (a worker restart, a
// lost connection) and that the coordinator retries by itself. It outlives
// each retry, so the count carries on until the turn completes or blocks.
type RetryWait struct {
	RunID     string
	SessionID string
	// AttemptID is the interrupted attempt.
	AttemptID string
	// Retries counts the automatic retries already started for the turn.
	Retries int
	Cause   string
	// DueAt is when the next retry is due; nil once it started.
	DueAt *time.Time
	// Turn is the interrupted attempt's worker request (JSON), sent again
	// should the worker never have admitted it.
	Turn      string
	CreatedAt time.Time
}

// RetryWaitStore is implemented by stores that record runs retrying an
// interrupted turn.
type RetryWaitStore interface {
	RecordRetryWait(ctx context.Context, wait RetryWait) error
	ClearRetryWait(ctx context.Context, runID string) error
	RetryWaitForRun(ctx context.Context, runID string) (RetryWait, error)
	RetryWaits(ctx context.Context) ([]RetryWait, error)
}

// RecordRetryWait records, or replaces, a run's retry wait.
func (s *Service) RecordRetryWait(ctx context.Context, wait RetryWait) error {
	if waits, ok := s.store.(RetryWaitStore); ok {
		wait.CreatedAt = s.now().UTC()
		return waits.RecordRetryWait(ctx, wait)
	}
	return nil
}

// ClearRetryWait forgets a run's retry wait, if any.
func (s *Service) ClearRetryWait(ctx context.Context, runID string) error {
	if waits, ok := s.store.(RetryWaitStore); ok {
		return waits.ClearRetryWait(ctx, runID)
	}
	return nil
}

// RetryWaitForRun returns a run's retry wait, or an empty one.
func (s *Service) RetryWaitForRun(ctx context.Context, runID string) (RetryWait, error) {
	if waits, ok := s.store.(RetryWaitStore); ok {
		return waits.RetryWaitForRun(ctx, runID)
	}
	return RetryWait{}, nil
}

// RetryWaits lists every run's retry wait.
func (s *Service) RetryWaits(ctx context.Context) ([]RetryWait, error) {
	if waits, ok := s.store.(RetryWaitStore); ok {
		return waits.RetryWaits(ctx)
	}
	return nil, nil
}
