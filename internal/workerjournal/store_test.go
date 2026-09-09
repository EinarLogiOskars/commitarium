package workerjournal

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestStorePersistsAttemptMutationResultAndEventsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.db")
	db, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate journal: %v", err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate journal again: %v", err)
	}
	store := NewStore(db)
	creation := validAttemptCreation("ses_one", "att_one", "idem_launch")
	attempt, created, err := store.CreateAttempt(t.Context(), creation)
	if err != nil || !created {
		t.Fatalf("create attempt: created=%t error=%v", created, err)
	}
	runningAt := attempt.StartedAt.Add(time.Second)
	attempt, err = store.TransitionAttempt(t.Context(), AttemptTransition{
		Reference: attempt.AttemptReference, Expected: workerhttp.AttemptStateStarting,
		State: workerhttp.AttemptStateRunning, ProviderSessionID: "provider_session_one",
		OccurredAt: runningAt,
	})
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	event := validJournalEvent(attempt.AttemptReference, 1, runningAt.Add(time.Second))
	if _, created, err := store.AppendEvent(t.Context(), EventAppend{
		Event: event, AcceptedAt: event.OccurredAt.Add(time.Second),
	}); err != nil || !created {
		t.Fatalf("append event: created=%t error=%v", created, err)
	}
	recoveryEvent := validJournalEvent(attempt.AttemptReference, 2, event.OccurredAt.Add(2*time.Second))
	recoveryEvent.Type = workerhttp.EventRecoveryAssessment
	recoveryEvent.Text = "Durable state is consistent"
	recoveryEvent.Redaction = workerhttp.RedactionMetadata{}
	recoveryEvent.Truncation = nil
	recoveryEvent.RecoveryAssessment = &workerhttp.RecoveryAssessment{Consistent: true}
	if _, created, err := store.AppendEvent(t.Context(), EventAppend{
		Event: recoveryEvent, AcceptedAt: recoveryEvent.OccurredAt.Add(time.Second),
	}); err != nil || !created {
		t.Fatalf("append recovery event: created=%t error=%v", created, err)
	}
	mutation := validMutation(attempt.AttemptReference, "idem_pause", MutationPause, recoveryEvent.OccurredAt.Add(2*time.Second))
	if _, created, err := store.ClaimMutation(t.Context(), mutation); err != nil || !created {
		t.Fatalf("claim mutation: created=%t error=%v", created, err)
	}
	resolved, err := store.ResolveMutation(t.Context(), MutationResolution{
		MutationIdentity: mutation.MutationIdentity,
		Status:           MutationApplied, OccurredAt: mutation.RequestedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("resolve mutation: %v", err)
	}
	completedAt := resolved.UpdatedAt.Add(time.Second)
	attempt, err = store.TransitionAttempt(t.Context(), AttemptTransition{
		Reference: attempt.AttemptReference, Expected: workerhttp.AttemptStateRunning,
		State: workerhttp.AttemptStateTerminal, OccurredAt: completedAt,
		Result: &workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
			Summary: "Implementation completed",
		},
	})
	if err != nil {
		t.Fatalf("complete attempt: %v", err)
	}
	if replayed, created, err := store.AppendEvent(t.Context(), EventAppend{
		Event: recoveryEvent, AcceptedAt: completedAt.Add(time.Second),
	}); err != nil || created || !reflect.DeepEqual(replayed, recoveryEvent) {
		t.Fatalf("replay terminal attempt event: result=%+v created=%t error=%v", replayed, created, err)
	}
	if _, _, err := store.AppendEvent(t.Context(), EventAppend{
		Event:      validJournalEvent(attempt.AttemptReference, 3, completedAt.Add(time.Second)),
		AcceptedAt: completedAt.Add(2 * time.Second),
	}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected terminal attempt to reject a new event, got %v", err)
	}
	newMutation := validMutation(
		attempt.AttemptReference,
		"idem_after_terminal",
		MutationPause,
		completedAt.Add(time.Second),
	)
	if _, _, err := store.ClaimMutation(t.Context(), newMutation); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("expected terminal attempt to reject a new mutation, got %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}

	reopened, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := Migrate(t.Context(), reopened); err != nil {
		t.Fatalf("migrate reopened journal: %v", err)
	}
	reopenedStore := NewStore(reopened)
	storedAttempt, err := reopenedStore.GetAttempt(t.Context(), attempt.AttemptReference)
	if err != nil {
		t.Fatalf("get reopened attempt: %v", err)
	}
	if !reflect.DeepEqual(storedAttempt, attempt) {
		t.Fatalf("stored attempt\n%+v\nwant\n%+v", storedAttempt, attempt)
	}
	storedMutation, err := reopenedStore.GetMutation(t.Context(), mutation.MutationIdentity)
	if err != nil {
		t.Fatalf("get reopened mutation: %v", err)
	}
	if storedMutation != resolved {
		t.Fatalf("stored mutation %+v, want %+v", storedMutation, resolved)
	}
	events, err := reopenedStore.ListEventsAfter(t.Context(), attempt.AttemptReference, 0)
	if err != nil {
		t.Fatalf("list reopened events: %v", err)
	}
	if len(events) != 2 || !reflect.DeepEqual(events[0], event) || !reflect.DeepEqual(events[1], recoveryEvent) {
		t.Fatalf("stored events %+v", events)
	}
	replayed, err := reopenedStore.ListEventsAfter(t.Context(), attempt.AttemptReference, 1)
	if err != nil {
		t.Fatalf("replay events after cursor: %v", err)
	}
	if len(replayed) != 1 || !reflect.DeepEqual(replayed[0], recoveryEvent) {
		t.Fatalf("events after cursor %+v, want %+v", replayed, recoveryEvent)
	}
}

func TestStoreFencesAttemptsAndMutationRetries(t *testing.T) {
	_, store := newTestJournal(t)
	creation := validAttemptCreation("ses_one", "att_one", "idem_launch")
	attempt, _, err := store.CreateAttempt(t.Context(), creation)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	retried, created, err := store.CreateAttempt(t.Context(), creation)
	if err != nil || created || !reflect.DeepEqual(retried, attempt) {
		t.Fatalf("retry attempt: result=%+v created=%t error=%v", retried, created, err)
	}
	conflict := creation
	conflict.RequestDigest = mustDigest(t, workerhttp.PutAttemptRequest{Instructions: "different"})
	if _, _, err := store.CreateAttempt(t.Context(), conflict); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("expected attempt conflict, got %v", err)
	}
	second := validAttemptCreation("ses_one", "att_two", "idem_second")
	if _, _, err := store.CreateAttempt(t.Context(), second); !errors.Is(err, ErrAttemptActive) {
		t.Fatalf("expected active-attempt fence, got %v", err)
	}

	now := attempt.StartedAt.Add(time.Second)
	mutation := validMutation(attempt.AttemptReference, "idem_command", MutationPause, now)
	claimed, created, err := store.ClaimMutation(t.Context(), mutation)
	if err != nil || !created {
		t.Fatalf("claim mutation: created=%t error=%v", created, err)
	}
	retry := mutation
	retry.RequestedAt = retry.RequestedAt.Add(time.Hour)
	retry.UpdatedAt = retry.RequestedAt
	retriedMutation, created, err := store.ClaimMutation(t.Context(), retry)
	if err != nil || created || retriedMutation != claimed {
		t.Fatalf("retry mutation: result=%+v created=%t error=%v", retriedMutation, created, err)
	}
	changed := mutation
	changed.Kind = MutationContinue
	if _, _, err := store.ClaimMutation(t.Context(), changed); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("expected mutation conflict, got %v", err)
	}
	resolved, err := store.ResolveMutation(t.Context(), MutationResolution{
		MutationIdentity: mutation.MutationIdentity,
		Status:           MutationApplied, OccurredAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("resolve mutation: %v", err)
	}
	retriedResolution, err := store.ResolveMutation(t.Context(), MutationResolution{
		MutationIdentity: mutation.MutationIdentity,
		Status:           MutationApplied, OccurredAt: now.Add(time.Hour),
	})
	if err != nil || retriedResolution != resolved {
		t.Fatalf("retry mutation resolution: result=%+v error=%v", retriedResolution, err)
	}
	if _, err := store.ResolveMutation(t.Context(), MutationResolution{
		MutationIdentity: mutation.MutationIdentity,
		Status:           MutationRejected, OccurredAt: now.Add(time.Hour),
	}); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("expected changed resolution conflict, got %v", err)
	}
}

func TestStoreCapturesProviderSessionIdentityExactlyAndOnlyOnce(t *testing.T) {
	_, store := newTestJournal(t)
	creation := validAttemptCreation("ses_one", "att_one", "idem_launch")
	attempt, _, err := store.CreateAttempt(t.Context(), creation)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}

	providerSessionID := "  opaque provider session identity  "
	runningAt := attempt.StartedAt.Add(time.Second)
	attempt, err = store.TransitionAttempt(t.Context(), AttemptTransition{
		Reference: attempt.AttemptReference, Expected: workerhttp.AttemptStateStarting,
		State: workerhttp.AttemptStateRunning, ProviderSessionID: providerSessionID,
		OccurredAt: runningAt,
	})
	if err != nil {
		t.Fatalf("capture provider session identity: %v", err)
	}
	if attempt.ProviderSessionID != providerSessionID {
		t.Fatalf("provider session identity = %q, want exact value %q", attempt.ProviderSessionID, providerSessionID)
	}
	stored, err := store.GetAttempt(t.Context(), attempt.AttemptReference)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if stored.ProviderSessionID != providerSessionID {
		t.Fatalf("stored provider session identity = %q, want exact value %q", stored.ProviderSessionID, providerSessionID)
	}
	if _, err := store.TransitionAttempt(t.Context(), AttemptTransition{
		Reference: attempt.AttemptReference, Expected: workerhttp.AttemptStateRunning,
		State: workerhttp.AttemptStatePauseRequested, ProviderSessionID: "different_session",
		OccurredAt: runningAt.Add(time.Second),
	}); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("expected changed provider session identity conflict, got %v", err)
	}
}

func TestStoreSequencesFullRedactedEventsAndRollsBackCursor(t *testing.T) {
	db, store := newTestJournal(t)
	creation := validAttemptCreation("ses_one", "att_one", "idem_launch")
	attempt, _, err := store.CreateAttempt(t.Context(), creation)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	first := validJournalEvent(attempt.AttemptReference, 1, attempt.StartedAt.Add(time.Second))
	recorded, created, err := store.AppendEvent(t.Context(), EventAppend{
		Event: first, AcceptedAt: first.OccurredAt.Add(time.Second),
	})
	if err != nil || !created || !reflect.DeepEqual(recorded, first) {
		t.Fatalf("append first event: result=%+v created=%t error=%v", recorded, created, err)
	}
	replayed, created, err := store.AppendEvent(t.Context(), EventAppend{
		Event: first, AcceptedAt: first.OccurredAt.Add(time.Hour),
	})
	if err != nil || created || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("replay first event: result=%+v created=%t error=%v", replayed, created, err)
	}
	changed := first
	changed.Text = "changed content"
	if _, _, err := store.AppendEvent(t.Context(), EventAppend{
		Event: changed, AcceptedAt: first.OccurredAt.Add(time.Hour),
	}); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("expected event conflict, got %v", err)
	}
	gap := validJournalEvent(attempt.AttemptReference, 3, first.OccurredAt.Add(time.Second))
	if _, _, err := store.AppendEvent(t.Context(), EventAppend{
		Event: gap, AcceptedAt: gap.OccurredAt.Add(time.Second),
	}); !errors.Is(err, ErrEventSequence) {
		t.Fatalf("expected event gap error, got %v", err)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`CREATE TRIGGER reject_worker_cursor
		 BEFORE UPDATE OF latest_event_sequence ON worker_attempts
		 BEGIN SELECT RAISE(ABORT, 'cursor rejected'); END`,
	); err != nil {
		t.Fatalf("create cursor rejection trigger: %v", err)
	}
	second := validJournalEvent(attempt.AttemptReference, 2, first.OccurredAt.Add(2*time.Second))
	if _, _, err := store.AppendEvent(t.Context(), EventAppend{
		Event: second, AcceptedAt: second.OccurredAt.Add(time.Second),
	}); err == nil {
		t.Fatal("expected cursor update failure")
	}
	events, err := store.ListEventsAfter(t.Context(), attempt.AttemptReference, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	storedAttempt, err := store.GetAttempt(t.Context(), attempt.AttemptReference)
	if err != nil {
		t.Fatalf("get attempt: %v", err)
	}
	if len(events) != 1 || storedAttempt.LatestEventSequence != 1 {
		t.Fatalf("partial event append: attempt=%+v events=%+v", storedAttempt, events)
	}
}

func TestStoreSerializesConcurrentMutationClaims(t *testing.T) {
	_, store := newTestJournal(t)
	creation := validAttemptCreation("ses_one", "att_one", "idem_launch")
	attempt, _, err := store.CreateAttempt(t.Context(), creation)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	mutation := validMutation(attempt.AttemptReference, "idem_pause", MutationPause, attempt.StartedAt.Add(time.Second))
	type result struct {
		created bool
		err     error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			_, created, err := store.ClaimMutation(t.Context(), mutation)
			results <- result{created: created, err: err}
		}()
	}
	ready.Wait()
	close(start)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil || first.created == second.created {
		t.Fatalf("expected one claim and one replay, got %+v and %+v", first, second)
	}
}

func TestStoreMarksInterruptedAttemptsIndeterminateAndKeepsFence(t *testing.T) {
	_, store := newTestJournal(t)
	activeCreation := validAttemptCreation("ses_active", "att_active", "idem_active")
	active, _, err := store.CreateAttempt(t.Context(), activeCreation)
	if err != nil {
		t.Fatalf("create active attempt: %v", err)
	}
	pending := validMutation(
		active.AttemptReference,
		"idem_pending",
		MutationMessage,
		active.StartedAt.Add(time.Second),
	)
	if _, created, err := store.ClaimMutation(t.Context(), pending); err != nil || !created {
		t.Fatalf("claim pending mutation: created=%t error=%v", created, err)
	}
	terminalCreation := validAttemptCreation("ses_terminal", "att_terminal", "idem_terminal")
	terminal, _, err := store.CreateAttempt(t.Context(), terminalCreation)
	if err != nil {
		t.Fatalf("create terminal attempt: %v", err)
	}
	terminal, err = store.TransitionAttempt(t.Context(), AttemptTransition{
		Reference: terminal.AttemptReference, Expected: workerhttp.AttemptStateStarting,
		State: workerhttp.AttemptStateTerminal, OccurredAt: terminal.StartedAt.Add(time.Second),
		Result: &workerhttp.TerminalResult{Outcome: workerhttp.OutcomeStopped, Summary: "Stopped before launch"},
	})
	if err != nil {
		t.Fatalf("complete terminal attempt: %v", err)
	}

	recovery, err := store.RecoverInterrupted(t.Context(), active.StartedAt.Add(2*time.Second))
	if err != nil || recovery.AttemptsMarked != 1 || recovery.MutationsMarked != 1 {
		t.Fatalf("recover interrupted records: result=%+v error=%v", recovery, err)
	}
	recovery, err = store.RecoverInterrupted(t.Context(), active.StartedAt.Add(3*time.Second))
	if err != nil || recovery != (RecoveryResult{}) {
		t.Fatalf("repeat interrupted-record recovery: result=%+v error=%v", recovery, err)
	}
	storedActive, err := store.GetAttempt(t.Context(), active.AttemptReference)
	if err != nil || storedActive.State != workerhttp.AttemptStateIndeterminate {
		t.Fatalf("unexpected interrupted attempt %+v error=%v", storedActive, err)
	}
	storedMutation, err := store.GetMutation(t.Context(), pending.MutationIdentity)
	if err != nil || storedMutation.Status != MutationIndeterminate {
		t.Fatalf("unexpected interrupted mutation %+v error=%v", storedMutation, err)
	}
	retriedMutation, created, err := store.ClaimMutation(t.Context(), pending)
	if err != nil || created || retriedMutation.Status != MutationIndeterminate {
		t.Fatalf("retry interrupted mutation: result=%+v created=%t error=%v", retriedMutation, created, err)
	}
	storedTerminal, err := store.GetAttempt(t.Context(), terminal.AttemptReference)
	if err != nil || storedTerminal.State != workerhttp.AttemptStateTerminal {
		t.Fatalf("unexpected terminal attempt %+v error=%v", storedTerminal, err)
	}
	if _, _, err := store.CreateAttempt(
		t.Context(),
		validAttemptCreation("ses_active", "att_replacement", "idem_replacement"),
	); !errors.Is(err, ErrAttemptActive) {
		t.Fatalf("expected indeterminate attempt to fence replacement, got %v", err)
	}
	if _, _, err := store.CreateAttempt(
		t.Context(),
		validAttemptCreation("ses_terminal", "att_next", "idem_next"),
	); err != nil {
		t.Fatalf("create replacement after terminal attempt: %v", err)
	}
}

func newTestJournal(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate journal: %v", err)
	}
	return db, NewStore(db)
}

func validAttemptCreation(sessionID string, attemptID string, key string) AttemptCreation {
	now := time.Date(2026, time.September, 9, 3, 0, 0, 0, time.UTC)
	request := workerhttp.PutAttemptRequest{
		Mode: workerhttp.AttemptModeStart,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "profile_test", ProjectID: "prj_test", FeatureID: "fea_test",
			Role: workerhttp.RoleCoder, WorkspaceID: "workspace_test",
		},
		Instructions: "Implement the accepted plan.",
	}
	digest, _ := DigestRequest(request)
	return AttemptCreation{
		Attempt: workerhttp.Attempt{
			AttemptReference: workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID},
			Mode:             request.Mode, Assignment: request.Assignment,
			State: workerhttp.AttemptStateStarting, StartedAt: now, UpdatedAt: now,
		},
		IdempotencyKey: key, RequestDigest: digest,
	}
}

func validMutation(
	reference workerhttp.AttemptReference,
	key string,
	kind MutationKind,
	now time.Time,
) Mutation {
	digest, _ := DigestRequest(workerhttp.CommandRequest{Type: workerhttp.CommandPause})
	return Mutation{
		MutationIdentity: workerhttp.MutationIdentity{AttemptReference: reference, IdempotencyKey: key},
		Kind:             kind, RequestDigest: digest, Status: MutationPending,
		RequestedAt: now, UpdatedAt: now,
	}
}

func validJournalEvent(
	reference workerhttp.AttemptReference,
	sequence int64,
	now time.Time,
) workerhttp.Event {
	return workerhttp.Event{
		AttemptReference: reference, Sequence: sequence,
		Type: workerhttp.EventActivity, Text: "Using [REDACTED]",
		OccurredAt: now,
		Redaction: workerhttp.RedactionMetadata{
			Count: 1, Categories: []workerhttp.RedactionCategory{workerhttp.RedactionCredential},
		},
		Truncation: &workerhttp.TruncationMetadata{OriginalBytes: 128, RetainedBytes: 64},
	}
}

func mustDigest(t *testing.T, request any) string {
	t.Helper()
	digest, err := DigestRequest(request)
	if err != nil {
		t.Fatalf("digest request: %v", err)
	}
	return digest
}
