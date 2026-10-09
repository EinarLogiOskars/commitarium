package execution

import (
	"errors"
	"strings"
	"time"
)

// TokenUsage is provider-reported token consumption. InputTokens excludes
// cached reads and cache writes.
type TokenUsage struct {
	InputTokens       int64
	CachedInputTokens int64
	CacheWriteTokens  int64
	OutputTokens      int64
}

func (usage TokenUsage) Add(other TokenUsage) TokenUsage {
	return TokenUsage{
		InputTokens:       usage.InputTokens + other.InputTokens,
		CachedInputTokens: usage.CachedInputTokens + other.CachedInputTokens,
		CacheWriteTokens:  usage.CacheWriteTokens + other.CacheWriteTokens,
		OutputTokens:      usage.OutputTokens + other.OutputTokens,
	}
}

// AttemptUsage is the usage of one worker attempt.
type AttemptUsage struct {
	SessionID  string
	AttemptID  string
	Usage      TokenUsage
	RecordedAt time.Time
}

func (usage AttemptUsage) Validate() error {
	if strings.TrimSpace(usage.SessionID) == "" || strings.TrimSpace(usage.AttemptID) == "" {
		return errors.New("attempt usage requires session and attempt IDs")
	}
	if usage.Usage.InputTokens < 0 || usage.Usage.CachedInputTokens < 0 ||
		usage.Usage.CacheWriteTokens < 0 || usage.Usage.OutputTokens < 0 {
		return errors.New("attempt usage cannot be negative")
	}
	if usage.RecordedAt.IsZero() {
		return errors.New("attempt usage requires a recording time")
	}
	return nil
}

// RoleAttemptUsage is one recorded attempt's usage with its session's role.
type RoleAttemptUsage struct {
	Role      string
	SessionID string
	AttemptID string
	Usage     TokenUsage
}
