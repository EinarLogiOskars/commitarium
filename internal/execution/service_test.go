package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type recordingStore struct {
	Store
	createdRun          Run
	createRunErr        error
	getRunResult        Run
	getRunErr           error
	createdSession      Session
	createSessionErr    error
	getSessionResult    Session
	getSessionErr       error
	appendedEvent       PendingEvent
	appendEventResult   Event
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

func (s *recordingStore) CreateSession(_ context.Context, session Session) error {
	s.createdSession = session
	return s.createSessionErr
}

func (s *recordingStore) GetSession(_ context.Context, _ string) (Session, error) {
	return s.getSessionResult, s.getSessionErr
}

func (s *recordingStore) AppendEvent(
	_ context.Context,
	event PendingEvent,
) (Event, error) {
	s.appendedEvent = event
	return s.appendEventResult, nil
}

func (s *recordingStore) CreateCommand(
	_ context.Context,
	command Command,
) (Command, error) {
	s.createdCommand = command
	return s.createCommandResult, nil
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

	run, err := service.CreateRun(t.Context(), "run_test", "fea_test")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run != store.createdRun || run.Status != RunStatusRunning || run.StartedAt != fixedTime {
		t.Errorf("unexpected run %+v", run)
	}

	session, err := service.CreateSession(
		t.Context(),
		"ses_test",
		run.ID,
		"agt_codex",
		worker.RoleCoder,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session != store.createdSession ||
		session.Status != SessionStatusStarting ||
		session.StartedAt != fixedTime {
		t.Errorf("unexpected session %+v", session)
	}
}

func TestServiceRetriesMatchingRunAndRejectsConflict(t *testing.T) {
	existing := Run{ID: "run_test", FeatureID: "fea_test", Status: RunStatusSucceeded}
	store := &recordingStore{
		createRunErr: ErrAlreadyExists,
		getRunResult: existing,
	}
	service := testService(store, time.Now())

	actual, err := service.CreateRun(t.Context(), existing.ID, existing.FeatureID)
	if err != nil {
		t.Fatalf("retry run creation: %v", err)
	}
	if actual != existing {
		t.Errorf("expected existing run %+v, got %+v", existing, actual)
	}
	if _, err := service.CreateRun(t.Context(), existing.ID, "fea_other"); !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("expected error %v, got %v", ErrRecordConflict, err)
	}
}

func TestServiceRecordsObservableEvent(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 0, 0, 0, 0, time.UTC)
	expected := Event{
		ID: "sev_generated", SessionID: "ses_test", Sequence: 1,
		Type: worker.EventMessage, Text: "implementation started", OccurredAt: fixedTime,
	}
	store := &recordingStore{appendEventResult: expected}
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

func TestServiceCreatesAndResolvesCommand(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 0, 0, 0, 0, time.UTC)
	store := &recordingStore{}
	service := testService(store, fixedTime)
	store.createCommandResult = Command{
		ID: "cmd_test", SessionID: "ses_test", Type: worker.CommandPause,
		Status: CommandStatusPending, RequestedAt: fixedTime,
	}

	created, err := service.CreateCommand(
		t.Context(),
		"cmd_test",
		"ses_test",
		worker.CommandPause,
		"",
	)
	if err != nil {
		t.Fatalf("create command: %v", err)
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
