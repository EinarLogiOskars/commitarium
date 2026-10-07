package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func (s *ExecutionStore) RotateProviderSession(
	ctx context.Context,
	rotation execution.ProviderSessionRotation,
) (execution.Session, error) {
	if strings.TrimSpace(rotation.SessionID) == "" ||
		strings.TrimSpace(rotation.ExpectedProviderSessionID) == "" ||
		strings.TrimSpace(rotation.ProviderSessionID) == "" ||
		rotation.ExpectedProviderSessionID == rotation.ProviderSessionID || rotation.OccurredAt.IsZero() {
		return execution.Session{}, execution.ErrInvalidSession
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return execution.Session{}, fmt.Errorf("begin provider session rotation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	session, err := scanExecutionSession(tx.QueryRowContext(ctx,
		`SELECT id, run_id, agent_id, role, status, provider_session_id,
		        outcome, disposition, summary, recovery_attempt,
		        started_at, updated_at, ended_at
		 FROM sessions WHERE id = ?`, rotation.SessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return execution.Session{}, execution.ErrNotFound
	}
	if err != nil {
		return execution.Session{}, err
	}
	if session.Status != execution.SessionStatusRunning ||
		session.ProviderSessionID != rotation.ExpectedProviderSessionID ||
		rotation.OccurredAt.Before(session.UpdatedAt) {
		return execution.Session{}, execution.ErrStateConflict
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE sessions SET provider_session_id = ?, updated_at = ?
		 WHERE id = ? AND status = ? AND provider_session_id = ?`,
		rotation.ProviderSessionID, formatExecutionTime(rotation.OccurredAt), session.ID,
		execution.SessionStatusRunning, rotation.ExpectedProviderSessionID)
	if err != nil {
		return execution.Session{}, fmt.Errorf("update provider session rotation: %w", err)
	}
	if err := requireExecutionUpdate(result, "session", session.ID); err != nil {
		return execution.Session{}, err
	}
	session.ProviderSessionID = rotation.ProviderSessionID
	session.UpdatedAt = rotation.OccurredAt.UTC()
	if err := session.Validate(); err != nil {
		return execution.Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return execution.Session{}, fmt.Errorf("commit provider session rotation: %w", err)
	}
	return session, nil
}
