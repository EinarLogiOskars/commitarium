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

// FeatureUsageByRole totals recorded usage across every run of a feature,
// grouped by session role and ordered by role.
func (s *ExecutionStore) FeatureUsageByRole(ctx context.Context, featureID string) ([]execution.RoleUsage, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT sessions.role,
		        SUM(usage.input_tokens), SUM(usage.cached_input_tokens),
		        SUM(usage.cache_write_tokens), SUM(usage.output_tokens)
		   FROM attempt_token_usage AS usage
		   JOIN sessions ON sessions.id = usage.session_id
		   JOIN runs ON runs.id = sessions.run_id
		  WHERE runs.feature_id = ?
		  GROUP BY sessions.role
		  ORDER BY sessions.role`,
		featureID,
	)
	if err != nil {
		return nil, fmt.Errorf("total feature token usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	totals := make([]execution.RoleUsage, 0, 2)
	for rows.Next() {
		var total execution.RoleUsage
		if err := rows.Scan(
			&total.Role,
			&total.Usage.InputTokens, &total.Usage.CachedInputTokens,
			&total.Usage.CacheWriteTokens, &total.Usage.OutputTokens,
		); err != nil {
			return nil, fmt.Errorf("scan feature token usage: %w", err)
		}
		totals = append(totals, total)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read feature token usage: %w", err)
	}
	return totals, nil
}
