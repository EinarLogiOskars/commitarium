package workeringest

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type stubCheckpointReader struct {
	checkpoint execution.WorkerAttemptCheckpoint
	err        error
}

func (reader *stubCheckpointReader) GetWorkerAttempt(
	context.Context,
	string,
) (execution.WorkerAttemptCheckpoint, error) {
	return reader.checkpoint, reader.err
}

type ingestStep struct {
	event   execution.Event
	created bool
	err     error
}

type stubIngester struct {
	steps []ingestStep
	next  int
}

func (ingester *stubIngester) IngestNext(
	context.Context,
	Reader,
) (execution.Event, bool, error) {
	step := ingester.steps[ingester.next]
	ingester.next++
	return step.event, step.created, step.err
}

type sliceEventStream struct {
	events []workerhttp.Event
	next   int
	err    error
	closed bool
}

func (stream *sliceEventStream) Next() (workerhttp.Event, error) {
	if stream.next < len(stream.events) {
		event := stream.events[stream.next]
		stream.next++
		return event, nil
	}
	if stream.err != nil {
		return workerhttp.Event{}, stream.err
	}
	return workerhttp.Event{}, io.EOF
}

func (stream *sliceEventStream) Close() error {
	stream.closed = true
	return nil
}

type stubAttemptSource struct {
	stream      EventStream
	openErr     error
	attempt     workerhttp.Attempt
	inspectErr  error
	opened      workerhttp.AttemptReference
	after       int64
	inspectCall int
}

func (source *stubAttemptSource) OpenEventStream(
	_ context.Context,
	reference workerhttp.AttemptReference,
	afterSequence int64,
) (EventStream, error) {
	source.opened = reference
	source.after = afterSequence
	return source.stream, source.openErr
}

func (source *stubAttemptSource) GetAttempt(
	context.Context,
	workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	source.inspectCall++
	return source.attempt, source.inspectErr
}

func TestPumpUsesDurableCursorAndCompletesCaughtUpTerminalAttempt(t *testing.T) {
	now := time.Date(2026, time.September, 9, 2, 0, 0, 0, time.UTC)
	checkpoint := execution.WorkerAttemptCheckpoint{
		SessionID: "ses_test", AttemptID: "att_test", LastEventSequence: 4,
		CreatedAt: now, UpdatedAt: now,
	}
	stream := &sliceEventStream{}
	source := &stubAttemptSource{
		stream:  stream,
		attempt: validPumpAttempt(checkpoint.SessionID, checkpoint.AttemptID, workerhttp.AttemptStateTerminal, 6),
	}
	ingester := &stubIngester{steps: []ingestStep{
		{event: acceptedPumpEvent(checkpoint, 5), created: true},
		{event: acceptedPumpEvent(checkpoint, 6), created: false},
		{err: io.EOF},
	}}
	pump := NewPump(&stubCheckpointReader{checkpoint: checkpoint}, ingester, source)

	result, err := pump.Run(t.Context(), checkpoint.SessionID)
	if err != nil {
		t.Fatalf("run pump: %v", err)
	}
	wantReference := workerhttp.AttemptReference{SessionID: checkpoint.SessionID, AttemptID: checkpoint.AttemptID}
	if source.opened != wantReference || source.after != 4 {
		t.Fatalf("opened stream for %+v after %d", source.opened, source.after)
	}
	if result.StartedAfterSequence != 4 || result.LastAcceptedSequence != 6 ||
		result.NewEvents != 1 || !result.AttemptInspected || source.inspectCall != 1 {
		t.Fatalf("unexpected pump result %+v", result)
	}
	if !stream.closed {
		t.Fatal("expected pump to close the event stream")
	}
}

func TestPumpClassifiesNonterminalStreamEnd(t *testing.T) {
	tests := []struct {
		name       string
		state      workerhttp.AttemptState
		latest     int64
		want       error
		checkpoint int64
	}{
		{name: "active", state: workerhttp.AttemptStateRunning, want: ErrStreamEndedActive},
		{name: "indeterminate", state: workerhttp.AttemptStateIndeterminate, want: ErrAttemptIndeterminate},
		{name: "terminal events remaining", state: workerhttp.AttemptStateTerminal, latest: 1, want: ErrTerminalEventsRemaining},
		{name: "worker cursor behind", state: workerhttp.AttemptStateTerminal, latest: 1, checkpoint: 2, want: ErrAttemptStateConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, time.September, 9, 2, 0, 0, 0, time.UTC)
			checkpoint := execution.WorkerAttemptCheckpoint{
				SessionID: "ses_test", AttemptID: "att_test",
				LastEventSequence: test.checkpoint, CreatedAt: now, UpdatedAt: now,
			}
			source := &stubAttemptSource{
				stream:  &sliceEventStream{},
				attempt: validPumpAttempt(checkpoint.SessionID, checkpoint.AttemptID, test.state, test.latest),
			}
			pump := NewPump(
				&stubCheckpointReader{checkpoint: checkpoint},
				&stubIngester{steps: []ingestStep{{err: io.EOF}}},
				source,
			)

			result, err := pump.Run(t.Context(), checkpoint.SessionID)
			if !errors.Is(err, test.want) {
				t.Fatalf("expected error %v, got %v", test.want, err)
			}
			if !result.AttemptInspected {
				t.Fatal("expected worker attempt inspection")
			}
		})
	}
}

func TestPumpInspectsAttemptAfterStreamFailure(t *testing.T) {
	streamFailure := errors.New("connection reset")
	now := time.Date(2026, time.September, 9, 2, 0, 0, 0, time.UTC)
	checkpoint := execution.WorkerAttemptCheckpoint{
		SessionID: "ses_test", AttemptID: "att_test", CreatedAt: now, UpdatedAt: now,
	}
	source := &stubAttemptSource{
		stream:  &sliceEventStream{},
		attempt: validPumpAttempt(checkpoint.SessionID, checkpoint.AttemptID, workerhttp.AttemptStateRunning, 0),
	}
	pump := NewPump(
		&stubCheckpointReader{checkpoint: checkpoint},
		&stubIngester{steps: []ingestStep{{err: streamFailure}}},
		source,
	)

	result, err := pump.Run(t.Context(), checkpoint.SessionID)
	if !errors.Is(err, streamFailure) {
		t.Fatalf("expected stream failure, got %v", err)
	}
	if !result.AttemptInspected || source.inspectCall != 1 {
		t.Fatalf("expected inspected result, got %+v", result)
	}
}

func TestPumpResumesFromSQLiteCursorAfterDatabaseReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "coordinator.db")
	db, err := database.OpenSQLite(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	store := database.NewExecutionStore(db)
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := createPumpExecutionRecords(t, db, store)
	executionService := execution.NewService(store)
	if _, _, err := executionService.CreateWorkerAttempt(t.Context(), "ses_pump", "att_pump"); err != nil {
		t.Fatalf("create worker attempt: %v", err)
	}
	firstStream := &sliceEventStream{events: []workerhttp.Event{
		pumpWorkerEvent(1, workerhttp.EventActivity, "Inspecting the repository", now.Add(time.Second)),
	}}
	firstSource := &stubAttemptSource{
		stream:  firstStream,
		attempt: validPumpAttempt("ses_pump", "att_pump", workerhttp.AttemptStateRunning, 1),
	}
	firstPump := NewPump(executionService, NewService(executionService, unchangedFilter()), firstSource)
	firstResult, err := firstPump.Run(t.Context(), "ses_pump")
	if !errors.Is(err, ErrStreamEndedActive) {
		t.Fatalf("expected active stream end, got result %+v error %v", firstResult, err)
	}
	if firstResult.LastAcceptedSequence != 1 || firstResult.NewEvents != 1 {
		t.Fatalf("unexpected first pump result %+v", firstResult)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close interrupted coordinator database: %v", err)
	}

	reopened, err := database.OpenSQLite(t.Context(), databasePath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := database.Migrate(t.Context(), reopened); err != nil {
		t.Fatalf("migrate reopened database: %v", err)
	}
	reopenedStore := database.NewExecutionStore(reopened)
	reopenedExecution := execution.NewService(reopenedStore)
	secondStream := &sliceEventStream{events: []workerhttp.Event{
		pumpWorkerEvent(2, workerhttp.EventActivity, "Running tests", now.Add(2*time.Second)),
		pumpWorkerEvent(3, workerhttp.EventAttemptTerminal, "Implementation completed", now.Add(3*time.Second)),
	}}
	secondSource := &stubAttemptSource{
		stream:  secondStream,
		attempt: validPumpAttempt("ses_pump", "att_pump", workerhttp.AttemptStateTerminal, 3),
	}
	secondPump := NewPump(
		reopenedExecution,
		NewService(reopenedExecution, unchangedFilter()),
		secondSource,
	)
	secondResult, err := secondPump.Run(t.Context(), "ses_pump")
	if err != nil {
		t.Fatalf("resume pump: result %+v error %v", secondResult, err)
	}
	if secondSource.after != 1 || secondResult.StartedAfterSequence != 1 ||
		secondResult.LastAcceptedSequence != 3 || secondResult.NewEvents != 2 {
		t.Fatalf("unexpected resumed pump result %+v, opened after %d", secondResult, secondSource.after)
	}
	events, err := reopenedStore.ListEvents(t.Context(), "ses_pump")
	if err != nil {
		t.Fatalf("list resumed activity: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected three nonduplicated events, got %+v", events)
	}
	for index, event := range events {
		if event.WorkerEventSequence != int64(index+1) {
			t.Fatalf("event %d has worker sequence %d", index, event.WorkerEventSequence)
		}
	}
}

func acceptedPumpEvent(
	checkpoint execution.WorkerAttemptCheckpoint,
	sequence int64,
) execution.Event {
	return execution.Event{
		ID: "sev_test", SessionID: checkpoint.SessionID, Sequence: sequence,
		Type: worker.EventActivity, Text: "activity", OccurredAt: checkpoint.CreatedAt,
		WorkerAttemptID: checkpoint.AttemptID, WorkerEventSequence: sequence,
	}
}

func validPumpAttempt(
	sessionID string,
	attemptID string,
	state workerhttp.AttemptState,
	latestSequence int64,
) workerhttp.Attempt {
	startedAt := time.Date(2026, time.September, 9, 2, 0, 0, 0, time.UTC)
	attempt := workerhttp.Attempt{
		AttemptReference: workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID},
		Mode:             workerhttp.AttemptModeStart,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "profile_test", ProjectID: "prj_test", FeatureID: "fea_test",
			Role: workerhttp.RoleCoder, WorkspaceID: "workspace_test",
		},
		ProviderSessionID: "provider_test", State: state,
		LatestEventSequence: latestSequence, StartedAt: startedAt, UpdatedAt: startedAt,
	}
	if state == workerhttp.AttemptStateTerminal {
		endedAt := startedAt.Add(time.Minute)
		attempt.UpdatedAt = endedAt
		attempt.EndedAt = &endedAt
		attempt.Result = &workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
			Summary: "Implementation completed",
		}
	}
	return attempt
}

func createPumpExecutionRecords(
	t *testing.T,
	db *sql.DB,
	store *database.ExecutionStore,
) time.Time {
	t.Helper()
	now := time.Date(2026, time.September, 9, 2, 0, 0, 0, time.UTC)
	if err := database.NewProjectStore(db).Create(t.Context(), project.Project{
		ID: "prj_pump", Name: "Pump test", CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := database.NewFeatureStore(db).Create(t.Context(), feature.Feature{
		ID: "fea_pump", ProjectID: "prj_pump", Title: "Pump test",
		State: feature.StateDraft, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	run := execution.Run{
		ID: "run_pump", FeatureID: "fea_pump", Status: execution.RunStatusRunning,
		StartedAt: now, UpdatedAt: now,
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := store.CreateSession(t.Context(), execution.Session{
		ID: "ses_pump", RunID: run.ID, AgentID: "agt_pump", Role: worker.RoleCoder,
		Status: execution.SessionStatusRunning, ProviderSessionID: "provider_test",
		StartedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return now
}

func pumpWorkerEvent(
	sequence int64,
	eventType workerhttp.EventType,
	text string,
	occurredAt time.Time,
) workerhttp.Event {
	return workerhttp.Event{
		AttemptReference: workerhttp.AttemptReference{SessionID: "ses_pump", AttemptID: "att_pump"},
		Sequence:         sequence, Type: eventType, Text: text, OccurredAt: occurredAt,
		Redaction: workerhttp.RedactionMetadata{},
	}
}
