package database

import (
	"context"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// MarkAgentNeedsSignIn records that an agent's provider login failed past
// the automatic retries. A repeat keeps when it began.
func (s *ExecutionStore) MarkAgentNeedsSignIn(ctx context.Context, agentID, lastError string, at time.Time) error {
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO agent_sign_ins (agent_id, since, last_error, last_tried_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (agent_id) DO UPDATE SET last_error = excluded.last_error,
		                                      last_tried_at = excluded.last_tried_at`,
		agentID, formatExecutionTime(at), lastError, formatExecutionTime(at),
	); err != nil {
		return fmt.Errorf("record that agent %q needs a sign-in: %w", agentID, err)
	}
	return nil
}

// ClearAgentSignIn forgets that an agent needs a sign-in. It reports
// whether it did.
func (s *ExecutionStore) ClearAgentSignIn(ctx context.Context, agentID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_sign_ins WHERE agent_id = ?`, agentID)
	if err != nil {
		return false, fmt.Errorf("clear agent %q sign-in: %w", agentID, err)
	}
	removed, err := result.RowsAffected()
	return removed > 0, err
}

// TouchAgentSignIn records that the waiting work was tried again.
func (s *ExecutionStore) TouchAgentSignIn(ctx context.Context, agentID string, at time.Time) error {
	if _, err := s.db.ExecContext(
		ctx, `UPDATE agent_sign_ins SET last_tried_at = ? WHERE agent_id = ?`, formatExecutionTime(at), agentID,
	); err != nil {
		return fmt.Errorf("record a try of agent %q: %w", agentID, err)
	}
	return nil
}

// AgentSignIns lists the agents that need a sign-in, oldest first.
func (s *ExecutionStore) AgentSignIns(ctx context.Context) ([]execution.AgentSignIn, error) {
	rows, err := s.db.QueryContext(
		ctx, `SELECT agent_id, since, last_error, last_tried_at FROM agent_sign_ins ORDER BY since, agent_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents that need a sign-in: %w", err)
	}
	defer rows.Close()
	signIns := make([]execution.AgentSignIn, 0)
	for rows.Next() {
		var signIn execution.AgentSignIn
		var since, tried string
		if err := rows.Scan(&signIn.AgentID, &since, &signIn.LastError, &tried); err != nil {
			return nil, fmt.Errorf("scan agent sign-in: %w", err)
		}
		if signIn.Since, err = parseExecutionTime(since); err != nil {
			return nil, err
		}
		if signIn.LastTriedAt, err = parseExecutionTime(tried); err != nil {
			return nil, err
		}
		signIns = append(signIns, signIn)
	}
	return signIns, rows.Err()
}
