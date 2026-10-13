package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// RunStateSince returns when a run entered its current status, wait and
// reason: for a blocked run, when it blocked.
func (s *ExecutionStore) RunStateSince(ctx context.Context, runID string) (time.Time, error) {
	var since string
	err := s.db.QueryRowContext(ctx, `SELECT state_since FROM runs WHERE id = ?`, runID).Scan(&since)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, execution.ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("select run %q state since: %w", runID, err)
	}
	if since == "" {
		return time.Time{}, nil
	}
	return parseExecutionTime(since)
}
