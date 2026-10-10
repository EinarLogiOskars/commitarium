package database

import (
	"context"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// RecordAttemptUsage stores one attempt's token usage. Recording the same
// attempt again is a no-op, so recovery may observe a terminal result twice.
func (s *ExecutionStore) RecordAttemptUsage(ctx context.Context, usage execution.AttemptUsage) error {
	if err := usage.Validate(); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO attempt_token_usage
		 (session_id, attempt_id, input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (session_id, attempt_id) DO NOTHING`,
		usage.SessionID, usage.AttemptID,
		usage.Usage.InputTokens, usage.Usage.CachedInputTokens,
		usage.Usage.CacheWriteTokens, usage.Usage.OutputTokens,
		formatExecutionTime(usage.RecordedAt),
	); err != nil {
		return fmt.Errorf("record attempt token usage: %w", err)
	}
	return nil
}

// FeatureAttemptUsage lists recorded usage for every attempt across all of a
// feature's runs and its assistant clarification, ordered by session and
// attempt.
func (s *ExecutionStore) FeatureAttemptUsage(ctx context.Context, featureID string) ([]execution.RoleAttemptUsage, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT sessions.role, usage.session_id, usage.attempt_id,
		        usage.input_tokens, usage.cached_input_tokens,
		        usage.cache_write_tokens, usage.output_tokens
		   FROM attempt_token_usage AS usage
		   JOIN sessions ON sessions.id = usage.session_id
		   JOIN runs ON runs.id = sessions.run_id
		  WHERE runs.feature_id = ?
		 UNION ALL
		 SELECT 'assistant', usage.session_id, usage.attempt_id,
		        usage.input_tokens, usage.cached_input_tokens,
		        usage.cache_write_tokens, usage.output_tokens
		   FROM assistant_turn_usage AS usage
		   JOIN assistant_sessions ON assistant_sessions.id = usage.session_id
		  WHERE assistant_sessions.feature_id = ?
		  ORDER BY 2, 3`,
		featureID, featureID,
	)
	if err != nil {
		return nil, fmt.Errorf("list feature token usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	usage := make([]execution.RoleAttemptUsage, 0)
	for rows.Next() {
		var attempt execution.RoleAttemptUsage
		if err := rows.Scan(
			&attempt.Role, &attempt.SessionID, &attempt.AttemptID,
			&attempt.Usage.InputTokens, &attempt.Usage.CachedInputTokens,
			&attempt.Usage.CacheWriteTokens, &attempt.Usage.OutputTokens,
		); err != nil {
			return nil, fmt.Errorf("scan feature token usage: %w", err)
		}
		usage = append(usage, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read feature token usage: %w", err)
	}
	return usage, nil
}
