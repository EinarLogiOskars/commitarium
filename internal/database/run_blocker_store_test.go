package database

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func TestRunBlockersAndRecoveriesAreStored(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, _ := createExecutionRecords(t, db, store)
	now := time.Date(2026, time.October, 17, 9, 0, 0, 0, time.UTC)
	if _, found, err := store.RunBlocker(t.Context(), run.ID); err != nil || found {
		t.Fatalf("no blocker yet: found=%t err=%v", found, err)
	}
	blocker := execution.Blocker{RunID: run.ID, Kind: execution.BlockerCheck, Check: "verify", Detail: "missing",
		Reason: "paused", Fingerprint: "abc", RecordedAt: now}
	if err := store.RecordRunBlocker(t.Context(), blocker); err != nil {
		t.Fatal(err)
	}
	blocker.Kind, blocker.Fingerprint = execution.BlockerAgent, "def"
	if err := store.RecordRunBlocker(t.Context(), blocker); err != nil {
		t.Fatal(err)
	}
	if got, found, err := store.RunBlocker(t.Context(), run.ID); err != nil || !found || got.Kind != execution.BlockerAgent ||
		got.Fingerprint != "def" || !got.RecordedAt.Equal(now) {
		t.Fatalf("blocker = %+v found=%t err=%v", got, found, err)
	}
	recovery := execution.Recovery{ID: "act_1", RunID: run.ID, Fingerprint: "def", How: execution.RecoveryRecheck,
		FromAssistant: true, CreatedAt: now}
	for range 2 {
		if err := store.RecordRunRecovery(t.Context(), recovery); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordRunRecovery(t.Context(), execution.Recovery{ID: "act_2", RunID: run.ID, Fingerprint: "def",
		How: execution.RecoveryContinue, Guidance: "post it", CreatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	recoveries, err := store.RunRecoveries(t.Context(), run.ID)
	if err != nil || len(recoveries) != 2 || recoveries[0].How != execution.RecoveryRecheck || !recoveries[0].FromAssistant ||
		recoveries[1].Guidance != "post it" {
		t.Fatalf("recoveries = %+v err=%v", recoveries, err)
	}
}

func TestLastTurnSaysWhatTheAgentDid(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	if _, found, err := store.LastTurn(t.Context(), run.ID); err != nil || found {
		t.Fatalf("no turn yet: found=%t err=%v", found, err)
	}
	start := time.Date(2026, time.November, 7, 15, 19, 6, 0, time.UTC)
	events := []struct {
		kind, text string
		after      time.Duration
	}{
		{"activity", "Codex started working.", 0},
		{"activity", "Codex ran command with exit code 0: git status", time.Second},
		{"attempt_terminal", "Done.", 30 * time.Second},
		{"activity", "Codex started working.", time.Minute},
		{"input_required", "Unable to verify Git HEAD.", time.Minute + 4*time.Second},
		{"attempt_terminal", "Unable to verify Git HEAD.", time.Minute + 4*time.Second},
	}
	for index, event := range events {
		if _, err := db.ExecContext(t.Context(),
			`INSERT INTO session_events (id, session_id, sequence, event_type, text, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`,
			"sev_turn_"+string(rune('a'+index)), session.ID, 1000+index, event.kind, event.text,
			start.Add(event.after).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO attempt_token_usage (session_id, attempt_id, input_tokens, cached_input_tokens, cache_write_tokens,
		   output_tokens, recorded_at) VALUES (?, 'attempt_2', 16320, 0, 0, 57, ?)`,
		session.ID, start.Add(time.Minute+4*time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	turn, found, err := store.LastTurn(t.Context(), run.ID)
	if err != nil || !found || turn.Duration != 4*time.Second || turn.Steps != 0 || !turn.HasTokens ||
		turn.OutputTokens != 57 || turn.Role != string(session.Role) {
		t.Fatalf("turn = %+v found=%t err=%v", turn, found, err)
	}
}
