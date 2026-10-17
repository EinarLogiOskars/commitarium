package database

import (
	"context"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
)

// RecordAssistantChatUsage stores one assistant conversation turn's usage
// under its scope. Recording the same turn again is a no-op.
func (s *ExecutionStore) RecordAssistantChatUsage(ctx context.Context, usage conversations.ChatUsage) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO assistant_chat_usage
		   (scope, attempt_id, agent_id, model, billing, cost_usd,
		    input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (scope, attempt_id) DO NOTHING`,
		usage.Scope, usage.AttemptID, usage.Agent, usage.Model, usage.Usage.Billing, usage.Usage.CostUSD,
		usage.Usage.InputTokens, usage.Usage.CachedInputTokens, usage.Usage.CacheWriteTokens, usage.Usage.OutputTokens,
		formatExecutionTime(usage.RecordedAt),
	); err != nil {
		return fmt.Errorf("record the assistant's usage for %q: %w", usage.Scope, err)
	}
	return nil
}

var _ conversations.ChatUsageRecorder = (*ExecutionStore)(nil)
