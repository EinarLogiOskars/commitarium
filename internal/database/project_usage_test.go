package database

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A project's usage gathers its work orders' turns, with billing and cost,
// and the assistant's own turns in the project (ADR-031).
func TestProjectUsageGathersWorkOrdersAndTheAssistant(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, coder := createExecutionRecords(t, db, store)
	var projectID string
	if err := db.QueryRowContext(t.Context(), `SELECT project_id FROM features WHERE id = ?`, run.FeatureID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 17, 9, 0, 0, 0, time.UTC)
	cost := 0.42
	if err := store.RecordAttemptUsage(t.Context(), execution.AttemptUsage{
		SessionID: coder.ID, AttemptID: "att_1", Model: "claude-sonnet-5-5", Billing: "api_key", CostUSD: &cost,
		Usage: execution.TokenUsage{InputTokens: 10, OutputTokens: 2}, RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	chat := conversations.ChatUsage{
		Scope: projectID, AttemptID: "turn_1", Agent: "codex", Model: "gpt-6.1-sol",
		Usage: workerhttp.TokenUsage{InputTokens: 5, OutputTokens: 1, Billing: "subscription"}, RecordedAt: now,
	}
	for range 2 { // recording a turn twice keeps one row
		if err := store.RecordAssistantChatUsage(t.Context(), chat); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordAssistantChatUsage(t.Context(), conversations.ChatUsage{
		Scope: "prj_other", AttemptID: "turn_1", Usage: workerhttp.TokenUsage{InputTokens: 99}, RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	usage, err := store.ProjectAttemptUsage(t.Context(), projectID)
	if err != nil || len(usage) != 2 {
		t.Fatalf("usage = %+v err=%v", usage, err)
	}
	var work, assistant execution.ProjectAttemptUsage
	for _, attempt := range usage {
		if attempt.FeatureID == "" {
			assistant = attempt
		} else {
			work = attempt
		}
	}
	if work.FeatureID != run.FeatureID || work.Billing != "api_key" || work.CostUSD == nil || *work.CostUSD != 0.42 {
		t.Fatalf("work order usage = %+v", work)
	}
	if assistant.Role != "assistant" || assistant.Model != "gpt-6.1-sol" || assistant.Billing != "subscription" ||
		assistant.CostUSD != nil || assistant.Usage.InputTokens != 5 {
		t.Fatalf("assistant usage = %+v", assistant)
	}
}
