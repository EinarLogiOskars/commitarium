package database

import (
	"slices"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestExecutionStoreTotalsAttemptUsageByRole(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, coder := createExecutionRecords(t, db, store)
	reviewer := execution.Session{
		ID: "ses_execution_reviewer", RunID: run.ID, AgentID: "agt_claude",
		Role: worker.RoleReviewer, Status: execution.SessionStatusRunning,
		ProviderSessionID: "provider_session_reviewer", StartedAt: coder.StartedAt, UpdatedAt: coder.UpdatedAt,
	}
	if err := store.CreateSession(t.Context(), reviewer); err != nil {
		t.Fatalf("create reviewer session: %v", err)
	}
	now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	records := []execution.AttemptUsage{
		{SessionID: coder.ID, AttemptID: "att_1", Usage: execution.TokenUsage{InputTokens: 10, CachedInputTokens: 100, OutputTokens: 5}, RecordedAt: now},
		{SessionID: coder.ID, AttemptID: "att_2", Usage: execution.TokenUsage{InputTokens: 20, CacheWriteTokens: 30, OutputTokens: 7}, RecordedAt: now},
		{SessionID: reviewer.ID, AttemptID: "att_3", Usage: execution.TokenUsage{InputTokens: 1, OutputTokens: 2}, RecordedAt: now},
		// Observing the same attempt again must not double count.
		{SessionID: coder.ID, AttemptID: "att_1", Usage: execution.TokenUsage{InputTokens: 999}, RecordedAt: now},
	}
	for _, record := range records {
		if err := store.RecordAttemptUsage(t.Context(), record); err != nil {
			t.Fatalf("record usage %+v: %v", record, err)
		}
	}
	totals, err := store.FeatureUsageByRole(t.Context(), run.FeatureID)
	if err != nil {
		t.Fatalf("total usage: %v", err)
	}
	want := []execution.RoleUsage{
		{Role: string(worker.RoleCoder), Usage: execution.TokenUsage{InputTokens: 30, CachedInputTokens: 100, CacheWriteTokens: 30, OutputTokens: 12}},
		{Role: string(worker.RoleReviewer), Usage: execution.TokenUsage{InputTokens: 1, OutputTokens: 2}},
	}
	if !slices.Equal(totals, want) {
		t.Fatalf("usage totals = %+v, want %+v", totals, want)
	}
	if err := store.RecordAttemptUsage(t.Context(), execution.AttemptUsage{
		SessionID: coder.ID, AttemptID: "att_bad", Usage: execution.TokenUsage{OutputTokens: -1}, RecordedAt: now,
	}); err == nil {
		t.Fatal("negative usage was accepted")
	}
}
