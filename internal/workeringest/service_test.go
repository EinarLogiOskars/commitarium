package workeringest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type stubReader struct {
	event workerhttp.Event
	err   error
}

func (reader *stubReader) Next() (workerhttp.Event, error) {
	return reader.event, reader.err
}

type recordingRecorder struct {
	sessionID string
	attemptID string
	sequence  int64
	occurred  time.Time
	event     worker.Event
	result    execution.Event
	created   bool
	err       error
	calls     int
	previews  []execution.MessagePreview
}

func (recorder *recordingRecorder) RecordWorkerEvent(
	_ context.Context,
	sessionID string,
	attemptID string,
	sequence int64,
	occurredAt time.Time,
	event worker.Event,
) (execution.Event, bool, error) {
	recorder.calls++
	recorder.sessionID = sessionID
	recorder.attemptID = attemptID
	recorder.sequence = sequence
	recorder.occurred = occurredAt
	recorder.event = event
	return recorder.result, recorder.created, recorder.err
}

func (recorder *recordingRecorder) PublishWorkerPreview(preview execution.MessagePreview) error {
	recorder.previews = append(recorder.previews, preview)
	return nil
}

type stubFrameReader struct {
	frames []workerhttp.StreamFrame
}

func (reader *stubFrameReader) Next() (workerhttp.Event, error) {
	return workerhttp.Event{}, errors.New("legacy Next called")
}

func (reader *stubFrameReader) NextFrame() (workerhttp.StreamFrame, error) {
	if len(reader.frames) == 0 {
		return workerhttp.StreamFrame{}, errors.New("no frames")
	}
	frame := reader.frames[0]
	reader.frames = reader.frames[1:]
	return frame, nil
}

func TestIngestNextFiltersAndRecordsOneEvent(t *testing.T) {
	source := validEvent()
	expected := execution.Event{ID: "sev_test", SessionID: source.SessionID, Sequence: 1}
	recorder := &recordingRecorder{result: expected, created: true}
	service := NewService(recorder, FilterFunc(func(
		_ context.Context,
		event workerhttp.Event,
	) (workerhttp.Event, error) {
		event.Text = "using [REDACTED]"
		event.Redaction = workerhttp.RedactionMetadata{
			Count: 1, Categories: []workerhttp.RedactionCategory{workerhttp.RedactionCredential},
		}
		return event, nil
	}))

	actual, created, err := service.IngestNext(t.Context(), &stubReader{event: source})
	if err != nil {
		t.Fatalf("ingest event: %v", err)
	}
	if !created || actual != expected {
		t.Fatalf("unexpected ingestion result %+v, created %t", actual, created)
	}
	if recorder.calls != 1 ||
		recorder.sessionID != source.SessionID ||
		recorder.attemptID != source.AttemptID ||
		recorder.sequence != source.Sequence ||
		!recorder.occurred.Equal(source.OccurredAt) ||
		recorder.event.Type != worker.EventActivity ||
		recorder.event.Text != "using [REDACTED]" ||
		recorder.event.Activity == nil ||
		recorder.event.Activity.Command != "go test ./..." ||
		recorder.event.Activity.ExitCode == nil || *recorder.event.Activity.ExitCode != 0 {
		t.Fatalf("unexpected recorder call %+v", recorder)
	}
}

func TestIngestNextRelaysPreviewBeforeRecordingDurableEvent(t *testing.T) {
	source := validEvent()
	preview := workerhttp.MessagePreview{
		AttemptReference: source.AttemptReference,
		StreamID:         "item_final",
		Text:             "Visible pre",
		OccurredAt:       source.OccurredAt.Add(-time.Millisecond),
		Redaction:        workerhttp.RedactionMetadata{},
	}
	recorder := &recordingRecorder{created: true}
	service := NewService(recorder, unchangedFilter())
	reader := &stubFrameReader{frames: []workerhttp.StreamFrame{
		{Preview: &preview},
		{Event: &source},
	}}

	if _, _, err := service.IngestNext(t.Context(), reader); err != nil {
		t.Fatalf("ingest frames: %v", err)
	}
	if recorder.calls != 1 || len(recorder.previews) != 1 {
		t.Fatalf("events=%d previews=%d", recorder.calls, len(recorder.previews))
	}
	actual := recorder.previews[0]
	if actual.SessionID != preview.SessionID || actual.AttemptID != preview.AttemptID ||
		actual.StreamID != preview.StreamID || actual.Text != preview.Text ||
		!actual.OccurredAt.Equal(preview.OccurredAt) {
		t.Fatalf("unexpected relayed preview %+v", actual)
	}
}

func TestIngestNextPreservesRecoveryAssessment(t *testing.T) {
	source := validEvent()
	source.Type = workerhttp.EventRecoveryAssessment
	source.Activity = nil
	source.RecoveryAssessment = &workerhttp.RecoveryAssessment{
		Consistent: true, RequiresUserReview: false,
	}
	recorder := &recordingRecorder{created: true}
	service := NewService(recorder, unchangedFilter())

	if _, _, err := service.IngestNext(t.Context(), &stubReader{event: source}); err != nil {
		t.Fatalf("ingest recovery assessment: %v", err)
	}
	if recorder.event.RecoveryAssessment == nil ||
		!recorder.event.RecoveryAssessment.Consistent ||
		recorder.event.RecoveryAssessment.RequiresUserReview {
		t.Fatalf("unexpected recovery assessment %+v", recorder.event.RecoveryAssessment)
	}
}

func TestIngestNextFailsClosedBeforePersistence(t *testing.T) {
	filterFailure := errors.New("sentinel detected")
	tests := []struct {
		name   string
		reader *stubReader
		filter Filter
		want   error
	}{
		{
			name: "reader failure", reader: &stubReader{err: errors.New("stream failed")},
			filter: unchangedFilter(),
		},
		{
			name: "invalid source", reader: &stubReader{event: workerhttp.Event{}},
			filter: unchangedFilter(),
		},
		{
			name: "filter failure", reader: &stubReader{event: validEvent()},
			filter: FilterFunc(func(context.Context, workerhttp.Event) (workerhttp.Event, error) {
				return workerhttp.Event{}, filterFailure
			}),
			want: filterFailure,
		},
		{
			name: "invalid filtered event", reader: &stubReader{event: validEvent()},
			filter: FilterFunc(func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
				event.Text = ""
				return event, nil
			}),
		},
		{
			name: "changed identity", reader: &stubReader{event: validEvent()},
			filter: FilterFunc(func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
				event.Sequence++
				return event, nil
			}),
			want: ErrFilterChangedIdentity,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingRecorder{}
			service := NewService(recorder, test.filter)
			if _, _, err := service.IngestNext(t.Context(), test.reader); err == nil {
				t.Fatal("expected ingestion to fail")
			} else if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("expected error %v, got %v", test.want, err)
			}
			if recorder.calls != 0 {
				t.Fatalf("expected no persistence, got %d calls", recorder.calls)
			}
		})
	}
}

func validEvent() workerhttp.Event {
	return workerhttp.Event{
		AttemptReference: workerhttp.AttemptReference{
			SessionID: "ses_test", AttemptID: "att_test",
		},
		Sequence:   1,
		Type:       workerhttp.EventActivity,
		Text:       "using token_test",
		OccurredAt: time.Date(2026, time.September, 9, 1, 0, 1, 0, time.UTC),
		Redaction:  workerhttp.RedactionMetadata{},
		Activity: &workerhttp.Activity{
			Kind: workerhttp.ActivityKindCommand, Command: "go test ./...",
			ExitCode: ingestIntPointer(0),
		},
	}
}

func ingestIntPointer(value int) *int { return &value }

func unchangedFilter() Filter {
	return FilterFunc(func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
		return event, nil
	})
}
