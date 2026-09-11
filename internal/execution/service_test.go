package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type recordingStore struct {
	Store
	createdRun          Run
	createRunErr        error
	getRunResult        Run
	getRunErr           error
	listedRuns          []Run
	createdSession      Session
	createSessionErr    error
	getSessionResult    Session
	getSessionErr       error
	listedSessions      []Session
	appendedEvent       PendingEvent
	appendEventResult   Event
	appendEventCreated  bool
	createdAttempt      WorkerAttemptCheckpoint
	createAttemptResult WorkerAttemptCheckpoint
	createAttemptMade   bool
	workerEvent         PendingWorkerEvent
	workerEventResult   Event
	workerEventCreated  bool
	createdCommand      Command
	createCommandResult Command
	resolvedCommand     CommandResolution
	resolveResult       Command
}

func (s *recordingStore) CreateRun(_ context.Context, run Run) error {
	s.createdRun = run
	return s.createRunErr
}

func (s *recordingStore) GetRun(_ context.Context, _ string) (Run, error) {
	return s.getRunResult, s.getRunErr
}

func (s *recordingStore) ListRunsByFeatureID(_ context.Context, _ string) ([]Run, error) {
	return s.listedRuns, nil
}

func (s *recordingStore) CreateSession(_ context.Context, session Session) error {
	s.createdSession = session
	return s.createSessionErr
}

func (s *recordingStore) GetSession(_ context.Context, _ string) (Session, error) {
	return s.getSessionResult, s.getSessionErr
}

func (s *recordingStore) ListSessions(_ context.Context, _ string) ([]Session, error) {
	return s.listedSessions, nil
}

func (s *recordingStore) AppendEvent(
	_ context.Context,
	event PendingEvent,
) (Event, bool, error) {
	s.appendedEvent = event
	return s.appendEventResult, s.appendEventCreated, nil
}

func (s *recordingStore) CreateWorkerAttempt(
	_ context.Context,
	checkpoint WorkerAttemptCheckpoint,
) (WorkerAttemptCheckpoint, bool, error) {
	s.createdAttempt = checkpoint
	return s.createAttemptResult, s.createAttemptMade, nil
}

func (s *recordingStore) AppendWorkerEvent(
	_ context.Context,
	event PendingWorkerEvent,
) (Event, bool, error) {
	s.workerEvent = event
	return s.workerEventResult, s.workerEventCreated, nil
}

func (s *recordingStore) CreateCommand(
	_ context.Context,
	command Command,
) (Command, bool, error) {
	s.createdCommand = command
	return s.createCommandResult, true, nil
}

func (s *recordingStore) ResolveCommand(
	_ context.Context,
	resolution CommandResolution,
) (Command, error) {
	s.resolvedCommand = resolution
	return s.resolveResult, nil
}

func TestServiceCreatesRunAndSessionWithCoordinatorTime(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 0, 0, 0, 123456789, time.UTC)
	store := &recordingStore{}
	service := testService(store, fixedTime)

	run, created, err := service.CreateRun(
		t.Context(), "run_test", "fea_test", 6, 4, project.DefaultAgentProviders(),
	)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if !created {
		t.Error("expected run to be newly created")
	}
	if run != store.createdRun || run.Status != RunStatusRunning || run.StartedAt != fixedTime ||
		run.PlanningRoundLimit != 6 || run.ImplementationReviewRoundLimit != 4 {
		t.Errorf("unexpected run %+v", run)
	}
	if run.AgentProviders != project.DefaultAgentProviders() {
		t.Fatalf("default agent providers = %+v", run.AgentProviders)
	}

	session, created, err := service.CreateSession(
		t.Context(),
		"ses_test",
		run.ID,
		"agt_codex",
		worker.RoleCoder,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !created {
		t.Error("expected session to be newly created")
	}
	if session != store.createdSession ||
		session.Status != SessionStatusStarting ||
		session.StartedAt != fixedTime {
		t.Errorf("unexpected session %+v", session)
	}
}

func TestServiceListsSessionsForRun(t *testing.T) {
	expected := []Session{{ID: "ses_test", RunID: "run_test"}}
	store := &recordingStore{listedSessions: expected}
	service := testService(store, time.Now())

	actual, err := service.SessionsForRun(t.Context(), "run_test")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(actual) != 1 || actual[0] != expected[0] {
		t.Errorf("expected %+v, got %+v", expected, actual)
	}
}

func TestServiceListsRunsForFeature(t *testing.T) {
	expected := []Run{{ID: "run_test", FeatureID: "fea_test"}}
	store := &recordingStore{listedRuns: expected}
	service := testService(store, time.Now())

	actual, err := service.RunsForFeature(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(actual) != 1 || actual[0] != expected[0] {
		t.Errorf("expected %+v, got %+v", expected, actual)
	}
}

func TestServiceRetriesMatchingRunAndRejectsConflict(t *testing.T) {
	existing := Run{
		ID: "run_test", FeatureID: "fea_test", Status: RunStatusSucceeded,
		PlanningRoundLimit: 6, ImplementationReviewRoundLimit: 4,
	}
	store := &recordingStore{
		createRunErr: ErrAlreadyExists,
		getRunResult: existing,
	}
	service := testService(store, time.Now())

	actual, created, err := service.CreateRun(
		t.Context(), existing.ID, existing.FeatureID, 6, 4, project.DefaultAgentProviders(),
	)
	if err != nil {
		t.Fatalf("retry run creation: %v", err)
	}
	if created {
		t.Error("expected retry to return the existing run")
	}
	if actual != existing {
		t.Errorf("expected existing run %+v, got %+v", existing, actual)
	}
	if _, _, err := service.CreateRun(
		t.Context(), existing.ID, "fea_other", 6, 4, project.DefaultAgentProviders(),
	); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("expected error %v, got %v", ErrRecordConflict, err)
	}
	if _, _, err := service.CreateRun(
		t.Context(), existing.ID, existing.FeatureID, 0, 4, project.DefaultAgentProviders(),
	); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("expected changed snapshot error %v, got %v", ErrRecordConflict, err)
	}
	if _, _, err := service.CreateRun(
		t.Context(), existing.ID, existing.FeatureID, 6, 4,
		project.AgentProviders{Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex},
	); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("expected changed provider snapshot error %v, got %v", ErrRecordConflict, err)
	}
}

func TestServiceRecordsObservableEvent(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 0, 0, 0, 0, time.UTC)
	expected := Event{
		ID: "sev_generated", SessionID: "ses_test", Sequence: 1,
		Type: worker.EventMessage, Text: "implementation started", OccurredAt: fixedTime,
	}
	store := &recordingStore{appendEventResult: expected, appendEventCreated: true}
	service := testService(store, fixedTime)

	actual, err := service.RecordSessionEvent(t.Context(), "ses_test", worker.Event{
		Type: worker.EventMessage,
		Text: "implementation started",
	})
	if err != nil {
		t.Fatalf("record session event: %v", err)
	}
	if actual != expected {
		t.Errorf("expected event %+v, got %+v", expected, actual)
	}
	if store.appendedEvent.ID != "sev_generated" ||
		store.appendedEvent.SessionID != "ses_test" ||
		store.appendedEvent.OccurredAt != fixedTime {
		t.Errorf("unexpected pending event %+v", store.appendedEvent)
	}
}

func TestServicePublishesOnlyNewSessionEvent(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 0, 0, 0, 0, time.UTC)
	expected := Event{
		ID: "sev_test", SessionID: "ses_test", Sequence: 1,
		Type: worker.EventActivity, Text: "working", OccurredAt: fixedTime,
	}
	store := &recordingStore{appendEventResult: expected, appendEventCreated: true}
	service := testService(store, fixedTime)
	service.broker = newEventBroker(1)
	events, cancel := service.SubscribeSessionEvents("ses_test")
	defer cancel()

	if _, err := service.RecordSessionEventWithID(
		t.Context(),
		expected.ID,
		expected.SessionID,
		worker.Event{Type: expected.Type, Text: expected.Text},
	); err != nil {
		t.Fatalf("record new event: %v", err)
	}
	if actual := <-events; actual != expected {
		t.Errorf("expected published event %+v, got %+v", expected, actual)
	}

	store.appendEventCreated = false
	if _, err := service.RecordSessionEventWithID(
		t.Context(),
		expected.ID,
		expected.SessionID,
		worker.Event{Type: expected.Type, Text: expected.Text},
	); err != nil {
		t.Fatalf("retry event: %v", err)
	}
	select {
	case duplicate := <-events:
		t.Fatalf("expected retry not to publish, got %+v", duplicate)
	default:
	}
}

func TestServiceCreatesWorkerAttemptWithCoordinatorTime(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 1, 0, 0, 0, time.UTC)
	expected := WorkerAttemptCheckpoint{
		SessionID: "ses_test", AttemptID: "att_test",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	store := &recordingStore{createAttemptResult: expected, createAttemptMade: true}
	service := testService(store, fixedTime)

	actual, created, err := service.CreateWorkerAttempt(
		t.Context(),
		expected.SessionID,
		expected.AttemptID,
	)
	if err != nil {
		t.Fatalf("create worker attempt: %v", err)
	}
	if !created || actual != expected || store.createdAttempt != expected {
		t.Fatalf("unexpected worker attempt result %+v, stored %+v", actual, store.createdAttempt)
	}
}

func TestServicePublishesOnlyNewWorkerEventAfterStoreAcceptance(t *testing.T) {
	acceptedAt := time.Date(2026, time.September, 9, 1, 0, 2, 0, time.UTC)
	occurredAt := acceptedAt.Add(-time.Second)
	expected := Event{
		ID: "sev_generated", SessionID: "ses_test", Sequence: 3,
		Type: worker.EventActivity, Text: "running tests", OccurredAt: occurredAt,
		WorkerAttemptID: "att_test", WorkerEventSequence: 2,
	}
	store := &recordingStore{workerEventResult: expected, workerEventCreated: true}
	service := testService(store, acceptedAt)
	service.broker = newEventBroker(1)
	events, cancel := service.SubscribeSessionEvents(expected.SessionID)
	defer cancel()

	actual, created, err := service.RecordWorkerEvent(
		t.Context(),
		expected.SessionID,
		expected.WorkerAttemptID,
		expected.WorkerEventSequence,
		expected.OccurredAt,
		worker.Event{Type: expected.Type, Text: expected.Text},
	)
	if err != nil {
		t.Fatalf("record worker event: %v", err)
	}
	if !created || actual != expected {
		t.Fatalf("unexpected worker event result %+v, created %t", actual, created)
	}
	if store.workerEvent.ID != "sev_generated" ||
		store.workerEvent.AcceptedAt != acceptedAt ||
		store.workerEvent.OccurredAt != occurredAt {
		t.Fatalf("unexpected pending worker event %+v", store.workerEvent)
	}
	if published := <-events; published != expected {
		t.Fatalf("expected published event %+v, got %+v", expected, published)
	}

	store.workerEventCreated = false
	if _, created, err := service.RecordWorkerEvent(
		t.Context(),
		expected.SessionID,
		expected.WorkerAttemptID,
		expected.WorkerEventSequence,
		expected.OccurredAt,
		worker.Event{Type: expected.Type, Text: expected.Text},
	); err != nil || created {
		t.Fatalf("retry worker event: created %t, error %v", created, err)
	}
	select {
	case duplicate := <-events:
		t.Fatalf("expected retry not to publish, got %+v", duplicate)
	default:
	}
}

func TestServiceCreatesAndResolvesCommand(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 0, 0, 0, 0, time.UTC)
	store := &recordingStore{}
	service := testService(store, fixedTime)
	store.createCommandResult = Command{
		ID: "cmd_test", SessionID: "ses_test", Type: worker.CommandPause,
		Status: CommandStatusPending, RequestedAt: fixedTime,
	}

	created, wasCreated, err := service.CreateCommand(
		t.Context(),
		"cmd_test",
		"ses_test",
		worker.CommandPause,
		"",
	)
	if err != nil {
		t.Fatalf("create command: %v", err)
	}
	if !wasCreated {
		t.Error("expected command to be newly created")
	}
	if created != store.createCommandResult || store.createdCommand.RequestedAt != fixedTime {
		t.Errorf("unexpected created command %+v", created)
	}

	appliedAt := fixedTime.Add(time.Second)
	service.now = func() time.Time { return appliedAt }
	store.resolveResult = Command{ID: "cmd_test", Status: CommandStatusApplied}
	if _, err := service.ResolveCommand(
		t.Context(),
		"cmd_test",
		CommandStatusApplied,
		"",
	); err != nil {
		t.Fatalf("resolve command: %v", err)
	}
	if store.resolvedCommand.CommandID != "cmd_test" ||
		store.resolvedCommand.Status != CommandStatusApplied ||
		store.resolvedCommand.AppliedAt != appliedAt {
		t.Errorf("unexpected command resolution %+v", store.resolvedCommand)
	}
}

func testService(store Store, now time.Time) *Service {
	return &Service{
		store: store,
		generateID: func() string {
			return "sev_generated"
		},
		now: func() time.Time {
			return now
		},
	}
}
