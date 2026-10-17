package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// RecordRunBlocker stores what a run's latest blocker is, replacing the
// previous one (ADR-032).
func (s *ExecutionStore) RecordRunBlocker(ctx context.Context, blocker execution.Blocker) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO run_blockers (run_id, kind, check_name, detail, reason, fingerprint, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET kind = excluded.kind, check_name = excluded.check_name,
		   detail = excluded.detail, reason = excluded.reason, fingerprint = excluded.fingerprint,
		   recorded_at = excluded.recorded_at`,
		blocker.RunID, string(blocker.Kind), blocker.Check, blocker.Detail, blocker.Reason, blocker.Fingerprint,
		formatExecutionTime(blocker.RecordedAt),
	); err != nil {
		return fmt.Errorf("record blocker of run %q: %w", blocker.RunID, err)
	}
	return nil
}

// RunBlocker returns a run's latest blocker; false when none was recorded.
func (s *ExecutionStore) RunBlocker(ctx context.Context, runID string) (execution.Blocker, bool, error) {
	var blocker execution.Blocker
	var kind, recordedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT run_id, kind, check_name, detail, reason, fingerprint, recorded_at FROM run_blockers WHERE run_id = ?`,
		runID,
	).Scan(&blocker.RunID, &kind, &blocker.Check, &blocker.Detail, &blocker.Reason, &blocker.Fingerprint, &recordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Blocker{}, false, nil
	}
	if err != nil {
		return execution.Blocker{}, false, fmt.Errorf("read blocker of run %q: %w", runID, err)
	}
	blocker.Kind = execution.BlockerKind(kind)
	if blocker.RecordedAt, err = time.Parse(time.RFC3339Nano, recordedAt); err != nil {
		return execution.Blocker{}, false, err
	}
	return blocker, true, nil
}

// RecordRunRecovery stores a recovery tried on a blocked run. Recording the
// same recovery again is a no-op.
func (s *ExecutionStore) RecordRunRecovery(ctx context.Context, recovery execution.Recovery) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO run_recoveries (id, run_id, fingerprint, how, guidance, from_assistant, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		recovery.ID, recovery.RunID, recovery.Fingerprint, string(recovery.How), recovery.Guidance,
		recovery.FromAssistant, formatExecutionTime(recovery.CreatedAt),
	); err != nil {
		return fmt.Errorf("record recovery of run %q: %w", recovery.RunID, err)
	}
	return nil
}

// RunRecoveries lists the recoveries tried on a run, oldest first.
func (s *ExecutionStore) RunRecoveries(ctx context.Context, runID string) ([]execution.Recovery, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, run_id, fingerprint, how, guidance, from_assistant, created_at
		   FROM run_recoveries WHERE run_id = ? ORDER BY created_at, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list recoveries of run %q: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()
	recoveries := make([]execution.Recovery, 0)
	for rows.Next() {
		var recovery execution.Recovery
		var how, createdAt string
		if err := rows.Scan(&recovery.ID, &recovery.RunID, &recovery.Fingerprint, &how, &recovery.Guidance,
			&recovery.FromAssistant, &createdAt); err != nil {
			return nil, err
		}
		recovery.How = execution.RecoveryHow(how)
		if recovery.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return nil, err
		}
		recoveries = append(recoveries, recovery)
	}
	return recoveries, rows.Err()
}

// LatestAgentMessages returns a run's last agent messages, newest last, each
// with the role that wrote it.
func (s *ExecutionStore) LatestAgentMessages(ctx context.Context, runID string, limit int) ([]execution.AgentMessage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sessions.role, events.text, events.occurred_at
		   FROM session_events AS events
		   JOIN sessions ON sessions.id = events.session_id
		  WHERE sessions.run_id = ? AND events.event_type = 'message'
		  ORDER BY events.occurred_at DESC, events.sequence DESC
		  LIMIT ?`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("list latest messages of run %q: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()
	messages := make([]execution.AgentMessage, 0, limit)
	for rows.Next() {
		var message execution.AgentMessage
		var occurredAt string
		if err := rows.Scan(&message.Role, &message.Text, &occurredAt); err != nil {
			return nil, err
		}
		if message.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages, rows.Err()
}
