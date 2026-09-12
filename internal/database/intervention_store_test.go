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

func TestExecutionStoreDeliversAndCompletesInterventionWithoutUnpausingRun(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, original := createExecutionRecords(t, db, store)
	now := run.UpdatedAt.Add(time.Second)
	if _, err := store.TransitionSession(t.Context(), execution.SessionTransition{
		SessionID: original.ID, Expected: execution.SessionStatusRunning,
		Status: execution.SessionStatusCompleted, OccurredAt: now,
	}); err != nil {
		t.Fatalf("complete fixture session: %v", err)
	}
	lead := original
	lead.ID = run.ID + ":lead"
	lead.AgentID = "codex-lead"
	lead.Role = worker.RoleLead
	lead.Status = execution.SessionStatusWaitingForUser
	lead.StartedAt = now
	lead.UpdatedAt = now
	lead.EndedAt = nil
	if err := store.CreateSession(t.Context(), lead); err != nil {
		t.Fatalf("create intervention lead: %v", err)
	}
	previous := execution.WorkerAttemptCheckpoint{
		SessionID: lead.ID, AttemptID: lead.ID + ":turn:1",
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateWorkerAttempt(t.Context(), previous); err != nil {
		t.Fatalf("create prior worker attempt: %v", err)
	}
	if _, err := store.TransitionRun(t.Context(), execution.RunTransition{
		RunID: run.ID, Expected: execution.RunStatusRunning,
		Status: execution.RunStatusWaitingForUser, Reason: "Waiting for the user.",
		WaitKind: execution.RunWaitKindClarification, OccurredAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("create safe boundary: %v", err)
	}
	queued, _, err := store.QueueIntervention(t.Context(), execution.InterventionRequest{
		ID: "int_delivery", RunID: run.ID, Target: worker.RoleLead,
		Message: "Keep the explanation concise.", OccurredAt: now.Add(2 * time.Second),
	})
	if err != nil || queued.Intervention.Status != execution.InterventionStatusQueued {
		t.Fatalf("queue intervention: result=%+v err=%v", queued, err)
	}

	attemptID := lead.ID + ":intervention:delivery"
	admission := execution.InterventionTurnAdmission{
		InterventionID:            queued.Intervention.ID,
		PreviousAttemptID:         previous.AttemptID,
		PreviousLastEventSequence: previous.LastEventSequence,
		NextAttempt: execution.WorkerAttemptCheckpoint{
			SessionID: lead.ID, AttemptID: attemptID,
			CreatedAt: now.Add(3 * time.Second), UpdatedAt: now.Add(3 * time.Second),
		},
		RunReason: "The lead is answering.", OccurredAt: now.Add(3 * time.Second),
	}
	admitted, err := store.BeginInterventionTurn(t.Context(), admission)
	if err != nil || !admitted {
		t.Fatalf("begin intervention turn: admitted=%t err=%v", admitted, err)
	}
	if admitted, err = store.BeginInterventionTurn(t.Context(), admission); err != nil || admitted {
		t.Fatalf("exact admission retry: admitted=%t err=%v", admitted, err)
	}
	answering, err := store.GetLatestIntervention(t.Context(), run.ID)
	if err != nil || answering.Status != execution.InterventionStatusBeingAnswered ||
		answering.AttemptID != attemptID {
		t.Fatalf("unexpected answering intervention=%+v err=%v", answering, err)
	}
	activeLead, err := store.GetSession(t.Context(), lead.ID)
	if err != nil || activeLead.Status != execution.SessionStatusRunning {
		t.Fatalf("lead was not activated: session=%+v err=%v", activeLead, err)
	}
	paused, err := store.GetRun(t.Context(), run.ID)
	if err != nil || !paused.Paused || paused.Status != execution.RunStatusWaitingForUser ||
		paused.WaitKind != execution.RunWaitKindPaused {
		t.Fatalf("delivery changed pause boundary: run=%+v err=%v", paused, err)
	}

	completion := execution.InterventionCompletion{
		InterventionID: queued.Intervention.ID, AttemptID: attemptID,
		ProviderSessionID: lead.ProviderSessionID,
		Effect:            worker.InterventionEffectGuidanceApplied,
		RunReason:         "The lead answered.", OccurredAt: now.Add(4 * time.Second),
	}
	answered, completed, err := store.CompleteIntervention(t.Context(), completion)
	if err != nil || !completed || answered.Status != execution.InterventionStatusAnswered ||
		answered.Effect != worker.InterventionEffectGuidanceApplied || answered.AnsweredAt == nil {
		t.Fatalf("complete intervention: intervention=%+v completed=%t err=%v", answered, completed, err)
	}
	if retried, completed, err := store.CompleteIntervention(t.Context(), completion); err != nil || completed ||
		retried.ID != answered.ID || retried.Status != answered.Status || retried.Effect != answered.Effect ||
		retried.AnsweredAt == nil || !retried.AnsweredAt.Equal(*answered.AnsweredAt) {
		t.Fatalf("exact completion retry: intervention=%+v completed=%t err=%v", retried, completed, err)
	}
	restingLead, err := store.GetSession(t.Context(), lead.ID)
	if err != nil || restingLead.Status != execution.SessionStatusWaitingForUser {
		t.Fatalf("lead did not return to rest: session=%+v err=%v", restingLead, err)
	}
	stillPaused, err := store.GetRun(t.Context(), run.ID)
	if err != nil || !stillPaused.Paused || stillPaused.Status != execution.RunStatusWaitingForUser ||
		stillPaused.WaitKind != execution.RunWaitKindPaused {
		t.Fatalf("completion resumed workflow: run=%+v err=%v", stillPaused, err)
	}
}
