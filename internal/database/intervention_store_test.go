package database

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestExecutionStoreInterventionSurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinator.db")
	db, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	store := NewExecutionStore(db)
	run, original := createExecutionRecords(t, db, store)
	lead := original
	lead.ID = "run_execution_test:lead"
	lead.Role = worker.RoleLead
	lead.AgentID = "codex-lead"
	if err := store.CreateSession(t.Context(), lead); err != nil {
		t.Fatalf("create lead session: %v", err)
	}
	request := execution.InterventionRequest{
		ID: "int_reopen", RunID: run.ID, Target: worker.RoleLead,
		Message: "Remember this after restart.", OccurredAt: run.UpdatedAt.Add(time.Second),
	}
	expected, created, err := store.QueueIntervention(t.Context(), request)
	if err != nil || !created {
		t.Fatalf("queue intervention: created=%t err=%v", created, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	reopenedDB, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = reopenedDB.Close() })
	reopened := NewExecutionStore(reopenedDB)
	actual, err := reopened.GetLatestIntervention(t.Context(), run.ID)
	if err != nil || actual != expected.Intervention {
		t.Fatalf("reopened intervention=%+v want=%+v err=%v", actual, expected.Intervention, err)
	}
	paused, err := reopened.GetRun(t.Context(), run.ID)
	if err != nil || !paused.Paused || paused.WaitKind != execution.RunWaitKindPaused {
		t.Fatalf("reopened pause state=%+v err=%v", paused, err)
	}
	events, err := reopened.ListEvents(t.Context(), lead.ID)
	if err != nil || len(events) != 1 || events[0] != expected.UserEvent {
		t.Fatalf("reopened intervention events=%+v err=%v", events, err)
	}
}

func TestExecutionStoreQueuesInterventionAndArmsPauseAtomically(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, original := createExecutionRecords(t, db, store)
	lead := original
	lead.ID = "run_execution_test:lead"
	lead.Role = worker.RoleLead
	lead.AgentID = "codex-lead"
	if err := store.CreateSession(t.Context(), lead); err != nil {
		t.Fatalf("create lead session: %v", err)
	}
	requestedAt := run.UpdatedAt.Add(time.Second)
	request := execution.InterventionRequest{
		ID: "int_one", RunID: run.ID, Target: worker.RoleLead,
		Message: "Please explain the database choice.", OccurredAt: requestedAt,
	}

	result, created, err := store.QueueIntervention(t.Context(), request)
	if err != nil || !created {
		t.Fatalf("queue intervention: result=%+v created=%t err=%v", result, created, err)
	}
	if result.Intervention.Status != execution.InterventionStatusWaitingForBoundary ||
		result.Intervention.SessionID != lead.ID || result.Intervention.RequestedAt != requestedAt {
		t.Fatalf("unexpected intervention %+v", result.Intervention)
	}
	if result.UserEvent.SessionID != lead.ID || result.UserEvent.Type != worker.EventUserMessage ||
		result.UserEvent.Text != request.Message {
		t.Fatalf("unexpected intervention event %+v", result.UserEvent)
	}
	paused, err := store.GetRun(t.Context(), run.ID)
	if err != nil || !paused.Paused || paused.Status != execution.RunStatusRunning ||
		paused.WaitKind != execution.RunWaitKindPaused {
		t.Fatalf("intervention did not arm running pause: run=%+v err=%v", paused, err)
	}
	events, err := store.ListEvents(t.Context(), lead.ID)
	if err != nil || len(events) != 1 || events[0] != result.UserEvent {
		t.Fatalf("unexpected intervention events %+v err=%v", events, err)
	}

	retried, created, err := store.QueueIntervention(t.Context(), request)
	if err != nil || created || retried != result {
		t.Fatalf("exact retry changed intervention: result=%+v created=%t err=%v", retried, created, err)
	}
	changed := request
	changed.Message = "Use another database."
	if _, _, err := store.QueueIntervention(t.Context(), changed); !errors.Is(err, execution.ErrInterventionConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
	second := request
	second.ID = "int_two"
	if _, _, err := store.QueueIntervention(t.Context(), second); !errors.Is(err, execution.ErrInterventionInProgress) {
		t.Fatalf("expected unfinished intervention conflict, got %v", err)
	}

	waiting, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser, Reason: "The current turn finished.",
		WaitKind: execution.RunWaitKindPhaseCheckpoint, OccurredAt: requestedAt.Add(time.Second),
	})
	if err != nil || !waiting.Paused || waiting.WaitKind != execution.RunWaitKindPaused ||
		waiting.PausedFromWaitKind != execution.RunWaitKindPhaseCheckpoint {
		t.Fatalf("reach intervention boundary: run=%+v err=%v", waiting, err)
	}
	queued, err := store.GetLatestIntervention(t.Context(), run.ID)
	if err != nil || queued.Status != execution.InterventionStatusQueued ||
		queued.UpdatedAt != waiting.UpdatedAt {
		t.Fatalf("intervention did not become queued: intervention=%+v err=%v", queued, err)
	}
}

func TestExecutionStoreListsOnlyAvailableInterventionTargets(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, original := createExecutionRecords(t, db, store)
	lead := original
	lead.ID = "run_execution_test:lead"
	lead.Role = worker.RoleLead
	lead.AgentID = "codex-lead"
	reviewer := original
	reviewer.ID = "run_execution_test:reviewer"
	reviewer.Role = worker.RoleReviewer
	reviewer.AgentID = "codex-reviewer"
	reviewer.ProviderSessionID = ""
	for _, session := range []execution.Session{lead, reviewer} {
		if err := store.CreateSession(t.Context(), session); err != nil {
			t.Fatalf("create %s session: %v", session.Role, err)
		}
	}

	targets, err := store.ListInterventionTargets(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("list intervention targets: %v", err)
	}
	if len(targets) != 2 || targets[0].Role != worker.RoleLead ||
		targets[0].SessionID != lead.ID || targets[1].Role != worker.RoleReviewer ||
		targets[1].SessionID != reviewer.ID {
		t.Fatalf("unexpected intervention targets %+v", targets)
	}

	endedAt := run.UpdatedAt.Add(30 * time.Second)
	if _, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: reviewer.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusCompleted, OccurredAt: endedAt,
	}); err != nil {
		t.Fatalf("complete reviewer session: %v", err)
	}
	targets, err = store.ListInterventionTargets(t.Context(), run.ID)
	if err != nil || len(targets) != 1 || targets[0].Role != worker.RoleLead {
		t.Fatalf("terminal reviewer was exposed: targets=%+v err=%v", targets, err)
	}
	if _, _, err := store.QueueIntervention(t.Context(), execution.InterventionRequest{
		ID: "int_reviewer", RunID: run.ID, Target: worker.RoleReviewer,
		Message: "Please inspect this concern.", OccurredAt: run.UpdatedAt.Add(time.Second),
	}); !errors.Is(err, execution.ErrInterventionTargetUnavailable) {
		t.Fatalf("expected unavailable reviewer error, got %v", err)
	}

	completedAt := run.UpdatedAt.Add(time.Minute)
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusSucceeded, Reason: "Done.", OccurredAt: completedAt,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	targets, err = store.ListInterventionTargets(t.Context(), run.ID)
	if err != nil || len(targets) != 0 {
		t.Fatalf("terminal run exposed intervention targets: targets=%+v err=%v", targets, err)
	}
}

func TestExecutionStoreQueuesAtExistingWaitingCheckpoint(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, original := createExecutionRecords(t, db, store)
	lead := original
	lead.ID = "run_execution_test:lead"
	lead.Role = worker.RoleLead
	lead.AgentID = "codex-lead"
	lead.Status = execution.SessionStatusWaitingForUser
	if err := store.CreateSession(t.Context(), lead); err != nil {
		t.Fatalf("create waiting lead: %v", err)
	}
	waitingAt := run.UpdatedAt.Add(time.Second)
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser, Reason: "Review is ready.",
		WaitKind: execution.RunWaitKindRoundCap, OccurredAt: waitingAt,
	}); err != nil {
		t.Fatalf("create waiting checkpoint: %v", err)
	}

	result, _, err := store.QueueIntervention(t.Context(), execution.InterventionRequest{
		ID: "int_waiting", RunID: run.ID, Target: worker.RoleLead,
		Message: "Help resolve the disagreement.", OccurredAt: waitingAt.Add(time.Second),
	})
	if err != nil || result.Intervention.Status != execution.InterventionStatusQueued {
		t.Fatalf("queue waiting intervention: result=%+v err=%v", result, err)
	}
	paused, err := store.GetRun(t.Context(), run.ID)
	if err != nil || !paused.Paused || paused.PausedFromWaitKind != execution.RunWaitKindRoundCap {
		t.Fatalf("waiting checkpoint was not retained: run=%+v err=%v", paused, err)
	}
}
