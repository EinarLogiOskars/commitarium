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
