package database

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestExecutionStoreCreatesWorkerAttemptIdempotently(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	checkpoint := execution.WorkerAttemptCheckpoint{
		SessionID: session.ID, AttemptID: "att_one", CreatedAt: now, UpdatedAt: now,
	}

	created, wasCreated, err := store.CreateWorkerAttempt(t.Context(), checkpoint)
	if err != nil {
		t.Fatalf("create worker attempt: %v", err)
	}
	if !wasCreated || created != checkpoint {
		t.Fatalf("unexpected created checkpoint %+v, created %t", created, wasCreated)
	}
	retried, wasCreated, err := store.CreateWorkerAttempt(t.Context(), checkpoint)
	if err != nil {
		t.Fatalf("retry worker attempt: %v", err)
	}
	if wasCreated || retried != checkpoint {
		t.Fatalf("unexpected retried checkpoint %+v, created %t", retried, wasCreated)
	}

	conflict := checkpoint
	conflict.AttemptID = "att_other"
	if _, _, err := store.CreateWorkerAttempt(t.Context(), conflict); !errors.Is(err, execution.ErrWorkerAttemptConflict) {
		t.Fatalf("expected error %v, got %v", execution.ErrWorkerAttemptConflict, err)
	}
	if _, _, err := store.CreateWorkerAttempt(t.Context(), execution.WorkerAttemptCheckpoint{
		SessionID: "ses_missing", AttemptID: "att_missing", CreatedAt: now, UpdatedAt: now,
	}); err == nil {
		t.Fatal("expected unknown session to violate the foreign key")
	}
}

func TestWorkerEventMigrationRequiresCompleteSourceIdentity(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	now := formatExecutionTime(session.StartedAt.Add(time.Second))
	base := `INSERT INTO session_events (
		id, session_id, sequence, event_type, text, occurred_at,
		worker_attempt_id, worker_event_sequence
	) VALUES (?, ?, 1, 'activity', 'invalid source', ?, ?, ?)`
	for _, test := range []struct {
		name      string
		attemptID any
		sequence  any
	}{
		{name: "attempt without sequence", attemptID: "att_one", sequence: nil},
		{name: "sequence without attempt", attemptID: nil, sequence: 1},
		{name: "blank attempt", attemptID: " ", sequence: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := db.ExecContext(
				t.Context(), base, "sev_"+test.name, session.ID, now, test.attemptID, test.sequence,
			); err == nil {
				t.Fatal("expected incomplete worker source identity to violate the schema")
			}
		})
	}
}

func TestExecutionStorePersistsWorkerEventAndCheckpointAtomically(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	checkpoint := createWorkerAttempt(t, store, session.ID, "att_one", now)

	ordinary, _, err := store.AppendEvent(t.Context(), execution.PendingEvent{
		ID: "sev_local", SessionID: session.ID, Type: worker.EventMessage,
		Text: "coordinator event", OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("append coordinator event: %v", err)
	}
	pending := execution.PendingWorkerEvent{
		ID: "sev_worker_one", SessionID: session.ID, AttemptID: checkpoint.AttemptID,
		SourceSequence: 1, Type: worker.EventActivity, Text: "running tests",
		OccurredAt: now.Add(time.Second), AcceptedAt: now.Add(2 * time.Second),
	}
	recorded, created, err := store.AppendWorkerEvent(t.Context(), pending)
	if err != nil {
		t.Fatalf("append worker event: %v", err)
	}
	if !created || recorded.Sequence != ordinary.Sequence+1 ||
		recorded.WorkerAttemptID != pending.AttemptID ||
		recorded.WorkerEventSequence != pending.SourceSequence {
		t.Fatalf("unexpected recorded worker event %+v", recorded)
	}
	storedCheckpoint, err := store.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("get worker attempt: %v", err)
	}
	if storedCheckpoint.LastEventSequence != 1 || !storedCheckpoint.UpdatedAt.Equal(pending.AcceptedAt) {
		t.Fatalf("unexpected advanced checkpoint %+v", storedCheckpoint)
	}
	events, err := store.ListEvents(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 2 || events[0] != ordinary || events[1] != recorded {
		t.Fatalf("unexpected event history %+v", events)
	}
}

func TestExecutionStoreWorkerEventReplayAndFencing(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	createWorkerAttempt(t, store, session.ID, "att_one", now)
	pending := execution.PendingWorkerEvent{
		ID: "sev_worker_one", SessionID: session.ID, AttemptID: "att_one",
		SourceSequence: 1, Type: worker.EventActivity, Text: "running tests",
		OccurredAt: now.Add(time.Second), AcceptedAt: now.Add(2 * time.Second),
	}
	original, _, err := store.AppendWorkerEvent(t.Context(), pending)
	if err != nil {
		t.Fatalf("append worker event: %v", err)
	}

	retry := pending
	retry.ID = "sev_retry_has_a_new_coordinator_id"
	retry.AcceptedAt = retry.AcceptedAt.Add(time.Hour)
	replayed, created, err := store.AppendWorkerEvent(t.Context(), retry)
	if err != nil {
		t.Fatalf("replay worker event: %v", err)
	}
	if created || replayed != original {
		t.Fatalf("expected existing event %+v, got %+v, created %t", original, replayed, created)
	}

	conflict := retry
	conflict.Text = "different content"
	if _, _, err := store.AppendWorkerEvent(t.Context(), conflict); !errors.Is(err, execution.ErrWorkerEventConflict) {
		t.Fatalf("expected error %v, got %v", execution.ErrWorkerEventConflict, err)
	}
	gap := pending
	gap.ID = "sev_gap"
	gap.SourceSequence = 3
	if _, _, err := store.AppendWorkerEvent(t.Context(), gap); !errors.Is(err, execution.ErrWorkerEventSequence) {
		t.Fatalf("expected error %v, got %v", execution.ErrWorkerEventSequence, err)
	}
	wrongAttempt := pending
	wrongAttempt.ID = "sev_wrong_attempt"
	wrongAttempt.AttemptID = "att_other"
	wrongAttempt.SourceSequence = 2
	if _, _, err := store.AppendWorkerEvent(t.Context(), wrongAttempt); !errors.Is(err, execution.ErrWorkerAttemptConflict) {
		t.Fatalf("expected error %v, got %v", execution.ErrWorkerAttemptConflict, err)
	}

	checkpoint, err := store.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("get worker attempt: %v", err)
	}
	events, err := store.ListEvents(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if checkpoint.LastEventSequence != 1 || len(events) != 1 {
		t.Fatalf("rejected events changed state: checkpoint %+v, events %+v", checkpoint, events)
	}
}

func TestExecutionStoreRollsBackWorkerEventWhenCheckpointCannotAdvance(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	createWorkerAttempt(t, store, session.ID, "att_one", now)
	if _, err := db.ExecContext(
		t.Context(),
		`CREATE TRIGGER reject_worker_checkpoint_update
		 BEFORE UPDATE ON worker_attempt_checkpoints
		 BEGIN SELECT RAISE(ABORT, 'checkpoint update rejected'); END`,
	); err != nil {
		t.Fatalf("create rejection trigger: %v", err)
	}

	if _, _, err := store.AppendWorkerEvent(t.Context(), execution.PendingWorkerEvent{
		ID: "sev_rolled_back", SessionID: session.ID, AttemptID: "att_one",
		SourceSequence: 1, Type: worker.EventActivity, Text: "must roll back",
		OccurredAt: now.Add(time.Second), AcceptedAt: now.Add(2 * time.Second),
	}); err == nil {
		t.Fatal("expected checkpoint update failure")
	}
	checkpoint, err := store.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("get worker attempt: %v", err)
	}
	events, err := store.ListEvents(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if checkpoint.LastEventSequence != 0 || len(events) != 0 {
		t.Fatalf("transaction was only partially applied: checkpoint %+v, events %+v", checkpoint, events)
	}
}

func TestExecutionStoreSerializesConcurrentWorkerEventReplay(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	createWorkerAttempt(t, store, session.ID, "att_one", now)

	type result struct {
		event   execution.Event
		created bool
		err     error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for _, id := range []string{"sev_concurrent_one", "sev_concurrent_two"} {
		go func(eventID string) {
			ready.Done()
			<-start
			event, created, err := store.AppendWorkerEvent(t.Context(), execution.PendingWorkerEvent{
				ID: eventID, SessionID: session.ID, AttemptID: "att_one",
				SourceSequence: 1, Type: worker.EventActivity, Text: "same event",
				OccurredAt: now.Add(time.Second), AcceptedAt: now.Add(2 * time.Second),
			})
			results <- result{event: event, created: created, err: err}
		}(id)
	}
	ready.Wait()
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent ingestion errors: %v, %v", first.err, second.err)
	}
	if first.created == second.created || first.event != second.event {
		t.Fatalf("expected one create and one replay, got %+v and %+v", first, second)
	}
	events, err := store.ListEvents(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one durable event, got %+v", events)
	}
}

func TestExecutionStoreWorkerCheckpointSurvivesDatabaseReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "coordinator.db")
	db, err := OpenSQLite(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	store := NewExecutionStore(db)
	_, session := createExecutionRecords(t, db, store)
	now := session.StartedAt.Add(time.Second)
	createWorkerAttempt(t, store, session.ID, "att_one", now)
	expected, _, err := store.AppendWorkerEvent(t.Context(), execution.PendingWorkerEvent{
		ID: "sev_worker_one", SessionID: session.ID, AttemptID: "att_one",
		SourceSequence: 1, Type: worker.EventActivity, Text: "persist me",
		OccurredAt: now.Add(time.Second), AcceptedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("append worker event: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopened, err := OpenSQLite(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedStore := NewExecutionStore(reopened)
	checkpoint, err := reopenedStore.GetWorkerAttempt(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("get reopened checkpoint: %v", err)
	}
	events, err := reopenedStore.ListEvents(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list reopened events: %v", err)
	}
	if checkpoint.LastEventSequence != 1 || len(events) != 1 || events[0] != expected {
		t.Fatalf("unexpected reopened state: checkpoint %+v, events %+v", checkpoint, events)
	}
	replayed, created, err := reopenedStore.AppendWorkerEvent(t.Context(), execution.PendingWorkerEvent{
		ID: "sev_after_restart_retry", SessionID: session.ID, AttemptID: "att_one",
		SourceSequence: 1, Type: worker.EventActivity, Text: "persist me",
		OccurredAt: now.Add(time.Second), AcceptedAt: now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("replay worker event after reopen: %v", err)
	}
	if created || replayed != expected {
		t.Fatalf("expected reopened replay to return %+v, got %+v, created %t", expected, replayed, created)
	}
}

func createWorkerAttempt(
	t *testing.T,
	store *ExecutionStore,
	sessionID string,
	attemptID string,
	now time.Time,
) execution.WorkerAttemptCheckpoint {
	t.Helper()
	checkpoint := execution.WorkerAttemptCheckpoint{
		SessionID: sessionID, AttemptID: attemptID, CreatedAt: now, UpdatedAt: now,
	}
	created, wasCreated, err := store.CreateWorkerAttempt(t.Context(), checkpoint)
	if err != nil || !wasCreated {
		t.Fatalf("create worker attempt: created=%t err=%v", wasCreated, err)
	}
	return created
}
