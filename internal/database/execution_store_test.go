package database

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestExecutionStorePersistsAcrossDatabaseReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "coordinator.db")
	db, err := OpenSQLite(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	store := NewExecutionStore(db)
	run, session := createExecutionRecords(t, db, store)
	pending := execution.PendingEvent{
		ID: "sev_one", SessionID: session.ID, Type: worker.EventActivity,
		Text: "inspecting accepted plan", OccurredAt: run.StartedAt.Add(time.Second),
	}
	createdEvent, _, err := store.AppendEvent(t.Context(), pending)
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	command := execution.Command{
		ID: "cmd_pause", SessionID: session.ID, Type: worker.CommandPause,
		Status: execution.CommandStatusPending, RequestedAt: run.StartedAt.Add(2 * time.Second),
	}
	if _, _, err := store.CreateCommand(t.Context(), command); err != nil {
		t.Fatalf("create command: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopened, err := OpenSQLite(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close reopened database: %v", err)
		}
	})
	reopenedStore := NewExecutionStore(reopened)
	storedRun, err := reopenedStore.GetRun(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("get persisted run: %v", err)
	}
	if storedRun != run {
		t.Errorf("expected run %+v, got %+v", run, storedRun)
	}
	storedSession, err := reopenedStore.GetSession(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("get persisted session: %v", err)
	}
	if storedSession != session {
		t.Errorf("expected session %+v, got %+v", session, storedSession)
	}
	sessions, err := reopenedStore.ListSessions(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("list persisted sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0] != session {
		t.Errorf("expected session list [%+v], got %+v", session, sessions)
	}
	events, err := reopenedStore.ListEvents(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list persisted events: %v", err)
	}
	if len(events) != 1 || events[0] != createdEvent {
		t.Errorf("expected event %+v, got %+v", createdEvent, events)
	}
	storedCommand, err := reopenedStore.GetCommand(t.Context(), command.ID)
	if err != nil {
		t.Fatalf("get persisted command: %v", err)
	}
	if storedCommand != command {
		t.Errorf("expected command %+v, got %+v", command, storedCommand)
	}
}

func TestExecutionStoreListsFeatureRunsNewestFirst(t *testing.T) {
	db, store := newTestExecutionStore(t)
	first, _ := createExecutionRecords(t, db, store)
	second := first
	second.ID = "run_newer"
	second.StartedAt = first.StartedAt.Add(time.Hour)
	second.UpdatedAt = second.StartedAt
	if err := store.CreateRun(t.Context(), second); err != nil {
		t.Fatalf("create newer run: %v", err)
	}
	if err := NewFeatureStore(db).Create(t.Context(), feature.Feature{
		ID: "fea_other", ProjectID: "prj_execution_test", Title: "Other",
		State: feature.StateDraft, CreatedAt: first.StartedAt, UpdatedAt: first.StartedAt,
	}); err != nil {
		t.Fatalf("create other feature: %v", err)
	}
	other := first
	other.ID = "run_other"
	other.FeatureID = "fea_other"
	other.StartedAt = first.StartedAt.Add(2 * time.Hour)
	other.UpdatedAt = other.StartedAt
	if err := store.CreateRun(t.Context(), other); err != nil {
		t.Fatalf("create other run: %v", err)
	}

	runs, err := store.ListRunsByFeatureID(t.Context(), first.FeatureID)
	if err != nil {
		t.Fatalf("list feature runs: %v", err)
	}
	if len(runs) != 2 || runs[0] != second || runs[1] != first {
		t.Errorf("expected newer then older run, got %+v", runs)
	}
	empty, err := store.ListRunsByFeatureID(t.Context(), "fea_empty")
	if err != nil {
		t.Fatalf("list empty feature: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("expected non-nil empty list, got %#v", empty)
	}
}

func TestExecutionStoreSequencesEventsAndRetriesByID(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	now := time.Date(2026, time.September, 8, 23, 0, 0, 0, time.UTC)
	firstRequest := execution.PendingEvent{
		ID: "sev_one", SessionID: session.ID, Type: worker.EventMessage,
		Text: "first", OccurredAt: now,
	}
	first, created, err := store.AppendEvent(t.Context(), firstRequest)
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	if !created {
		t.Error("expected first event to be newly created")
	}
	retry := firstRequest
	retry.OccurredAt = retry.OccurredAt.Add(time.Hour)
	retried, created, err := store.AppendEvent(t.Context(), retry)
	if err != nil {
		t.Fatalf("retry first event: %v", err)
	}
	if created {
		t.Error("expected retry to return the existing event")
	}
	if retried != first || first.Sequence != 1 {
		t.Errorf("expected original first event %+v, got %+v", first, retried)
	}
	second, _, err := store.AppendEvent(t.Context(), execution.PendingEvent{
		ID: "sev_two", SessionID: session.ID, Type: worker.EventMessage,
		Text: "second", OccurredAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	if second.Sequence != 2 {
		t.Errorf("expected sequence 2, got %d", second.Sequence)
	}

	conflict := firstRequest
	conflict.Text = "different"
	if _, _, err := store.AppendEvent(t.Context(), conflict); !errors.Is(err, execution.ErrEventConflict) {
		t.Fatalf("expected error %v, got %v", execution.ErrEventConflict, err)
	}
}

func TestExecutionStoreCreatesCommandsIdempotently(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	command := execution.Command{
		ID: "cmd_message", SessionID: session.ID, Type: worker.CommandMessage,
		Message: "Please use the simpler approach.",
		Status:  execution.CommandStatusPending, RequestedAt: run.StartedAt.Add(time.Second),
	}
	created, wasCreated, err := store.CreateCommand(t.Context(), command)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}
	if !wasCreated {
		t.Error("expected command to be newly created")
	}
	retry := command
	retry.RequestedAt = retry.RequestedAt.Add(time.Hour)
	retried, wasCreated, err := store.CreateCommand(t.Context(), retry)
	if err != nil {
		t.Fatalf("retry command: %v", err)
	}
	if wasCreated {
		t.Error("expected retry to return the existing command")
	}
	if retried != created {
		t.Errorf("expected original command %+v, got %+v", created, retried)
	}

	conflict := command
	conflict.Type = worker.CommandPause
	conflict.Message = ""
	if _, _, err := store.CreateCommand(t.Context(), conflict); !errors.Is(err, execution.ErrCommandConflict) {
		t.Fatalf("expected error %v, got %v", execution.ErrCommandConflict, err)
	}
}

func TestExecutionStoreRejectsDuplicateRunAndSessionIDs(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	if err := store.CreateRun(t.Context(), run); !errors.Is(err, execution.ErrAlreadyExists) {
		t.Fatalf("expected error %v, got %v", execution.ErrAlreadyExists, err)
	}
	if err := store.CreateSession(t.Context(), session); !errors.Is(err, execution.ErrAlreadyExists) {
		t.Fatalf("expected error %v, got %v", execution.ErrAlreadyExists, err)
	}
}

func TestExecutionStoreTransitionsRunAndSessionWithExpectedState(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := run.StartedAt.Add(time.Minute)

	waiting, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser,
		Reason: "planning agents need a user decision", OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("pause run for user: %v", err)
	}
	if waiting.Status != execution.RunStatusWaitingForUser || waiting.EndedAt != nil {
		t.Errorf("unexpected waiting run %+v", waiting)
	}
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusFailed, OccurredAt: now,
	}); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("expected error %v, got %v", execution.ErrStateConflict, err)
	}
	running, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusWaitingForUser,
		Status: execution.RunStatusRunning, OccurredAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	completed, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: running.Status,
		Status: execution.RunStatusSucceeded, OccurredAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if completed.EndedAt == nil || !completed.EndedAt.Equal(now.Add(2*time.Second)) {
		t.Errorf("unexpected completed run %+v", completed)
	}

	pauseRequested, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusPauseRequested, OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("request session pause: %v", err)
	}
	paused, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: pauseRequested.Status,
		Status: execution.SessionStatusPaused, OccurredAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("acknowledge session pause: %v", err)
	}
	resumed, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: paused.Status,
		Status: execution.SessionStatusRunning, OccurredAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("resume session: %v", err)
	}
	finished, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: resumed.Status,
		Status:            execution.SessionStatusCompleted,
		ProviderSessionID: session.ProviderSessionID,
		OccurredAt:        now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatalf("complete session: %v", err)
	}
	if finished.EndedAt == nil || finished.Status != execution.SessionStatusCompleted {
		t.Errorf("unexpected completed session %+v", finished)
	}
}

func TestExecutionStoreResolvesCommandsIdempotently(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	command, _, err := store.CreateCommand(t.Context(), execution.Command{
		ID: "cmd_pause", SessionID: session.ID, Type: worker.CommandPause,
		Status: execution.CommandStatusPending, RequestedAt: run.StartedAt,
	})
	if err != nil {
		t.Fatalf("create command: %v", err)
	}
	resolution := execution.CommandResolution{
		CommandID: command.ID, Status: execution.CommandStatusApplied,
		AppliedAt: run.StartedAt.Add(time.Second),
	}
	resolved, err := store.ResolveCommand(t.Context(), resolution)
	if err != nil {
		t.Fatalf("resolve command: %v", err)
	}
	retry := resolution
	retry.AppliedAt = retry.AppliedAt.Add(time.Hour)
	retried, err := store.ResolveCommand(t.Context(), retry)
	if err != nil {
		t.Fatalf("retry command resolution: %v", err)
	}
	if retried.ID != resolved.ID ||
		retried.Status != resolved.Status ||
		retried.AppliedAt == nil ||
		resolved.AppliedAt == nil ||
		!retried.AppliedAt.Equal(*resolved.AppliedAt) {
		t.Errorf("expected original resolution %+v, got %+v", resolved, retried)
	}
	if _, err := store.ResolveCommand(t.Context(), execution.CommandResolution{
		CommandID: command.ID, Status: execution.CommandStatusRejected,
		AppliedAt: run.StartedAt.Add(2 * time.Second), Error: "session already stopped",
	}); !errors.Is(err, execution.ErrCommandConflict) {
		t.Fatalf("expected error %v, got %v", execution.ErrCommandConflict, err)
	}
}

func newTestExecutionStore(t *testing.T) (*sql.DB, *ExecutionStore) {
	t.Helper()
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	return db, NewExecutionStore(db)
}

func createExecutionRecords(
	t *testing.T,
	db *sql.DB,
	store *ExecutionStore,
) (execution.Run, execution.Session) {
	t.Helper()
	now := time.Date(2026, time.September, 8, 23, 0, 0, 123456789, time.UTC)
	if err := NewProjectStore(db).Create(t.Context(), project.Project{
		ID: "prj_execution_test", Name: "Execution test", CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := NewFeatureStore(db).Create(t.Context(), feature.Feature{
		ID: "fea_execution_test", ProjectID: "prj_execution_test", Title: "Execution test",
		State: feature.StateDraft, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	run := execution.Run{
		ID: "run_execution_test", FeatureID: "fea_execution_test",
		Status:             execution.RunStatusRunning,
		PlanningRoundLimit: 0, ImplementationReviewRoundLimit: 4,
		StartedAt: now, UpdatedAt: now,
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	session := execution.Session{
		ID: "ses_execution_test", RunID: run.ID, AgentID: "agt_codex",
		Role: worker.RoleCoder, Status: execution.SessionStatusRunning,
		ProviderSessionID: "provider_session_test", StartedAt: now, UpdatedAt: now,
	}
	if err := store.CreateSession(t.Context(), session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return run, session
}

func TestExecutionStoreDiscoversAndClaimsInterruptedSession(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := session.UpdatedAt.Add(time.Minute)
	command := execution.Command{
		ID: "cmd_pending", SessionID: session.ID,
		Type: worker.CommandMessage, Message: "Check the interrupted test.",
		Status: execution.CommandStatusPending, RequestedAt: now,
	}
	if _, created, err := store.CreateCommand(t.Context(), command); err != nil || !created {
		t.Fatalf("create pending command: created=%t err=%v", created, err)
	}
	betweenSessions := run
	betweenSessions.ID = "run_between_sessions"
	if err := store.CreateRun(t.Context(), betweenSessions); err != nil {
		t.Fatalf("create run interrupted between sessions: %v", err)
	}

	runs, err := store.ListRecoverableRuns(t.Context())
	if err != nil {
		t.Fatalf("list recoverable runs: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != "run_between_sessions" || runs[1].ID != run.ID {
		t.Fatalf("unexpected recoverable runs %+v", runs)
	}
	active, err := store.ListActiveSessions(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("list active sessions: %v", err)
	}
	if len(active) != 1 || active[0].ID != session.ID {
		t.Fatalf("unexpected active sessions %+v", active)
	}
	pending, err := store.ListPendingCommands(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list pending commands: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != command.ID {
		t.Fatalf("unexpected pending commands %+v", pending)
	}

	recovering, err := store.BeginSessionRecovery(t.Context(), execution.SessionRecovery{
		SessionID: session.ID, Expected: execution.SessionStatusRunning, OccurredAt: now,
	})
	if err != nil {
		t.Fatalf("begin session recovery: %v", err)
	}
	if recovering.Status != execution.SessionStatusPauseRequested ||
		recovering.RecoveryAttempt != 1 ||
		recovering.ProviderSessionID != session.ProviderSessionID {
		t.Fatalf("unexpected recovering session %+v", recovering)
	}
	retried, err := store.BeginSessionRecovery(t.Context(), execution.SessionRecovery{
		SessionID: session.ID, Expected: execution.SessionStatusPauseRequested,
		OccurredAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("retry session recovery after restart: %v", err)
	}
	if retried.RecoveryAttempt != 2 {
		t.Fatalf("expected second durable recovery attempt, got %+v", retried)
	}
}

func TestExecutionStoreDoesNotRecoverStableUserWait(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, store)
	now := session.UpdatedAt.Add(time.Minute)
	if _, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusWaitingForUser, OccurredAt: now,
	}); err != nil {
		t.Fatalf("wait for user in session: %v", err)
	}
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser, OccurredAt: now,
	}); err != nil {
		t.Fatalf("wait for user in run: %v", err)
	}
	runs, err := store.ListRecoverableRuns(t.Context())
	if err != nil {
		t.Fatalf("list recoverable runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("stable user wait was treated as an interruption: %+v", runs)
	}
}

func TestExecutionStorePersistsReplayableSessionResult(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	result := worker.Result{
		Outcome:           worker.OutcomeCompleted,
		Disposition:       worker.DispositionChangesRequested,
		ProviderSessionID: session.ProviderSessionID,
		Summary:           "one blocking issue remains",
	}
	finished, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: session.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusCompleted, Result: &result,
		OccurredAt: session.UpdatedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("complete session with result: %v", err)
	}
	if finished.Outcome != result.Outcome ||
		finished.Disposition != result.Disposition ||
		finished.Summary != result.Summary {
		t.Fatalf("unexpected persisted result %+v", finished)
	}
	stored, err := store.GetSession(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("reload completed session: %v", err)
	}
	if stored.Outcome != result.Outcome || stored.Summary != result.Summary {
		t.Fatalf("unexpected reloaded session %+v", stored)
	}
}
