package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestStreamSessionEventsResumesAfterLastEventID(t *testing.T) {
	first := testSessionEvent("sev_one", 1, "starting")
	second := testSessionEvent("sev_two", 2, "I’ll inspect the project first.")
	second.Activity = &worker.Activity{Kind: worker.ActivityKindNarration}
	third := testSessionEvent("sev_three", 3, "editing files")
	third.Activity = &worker.Activity{
		Kind: worker.ActivityKindCommand, Command: "pnpm test", ExitCode: httpIntPointer(0),
	}
	executions := &recordingExecutionService{
		session: execution.Session{ID: "ses_test"},
		events:  []execution.Event{first, second, third},
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/sessions/ses_test/events/stream",
		nil,
	)
	request.Header.Set("Last-Event-ID", first.ID)
	cancelledContext, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(cancelledContext)
	recorder := httptest.NewRecorder()

	New(nil, nil, nil, executions, nil, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Errorf("expected event-stream content type, got %q", contentType)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "id: "+first.ID) {
		t.Errorf("expected first event to be skipped, got %q", body)
	}
	if !strings.Contains(body, "id: "+second.ID) ||
		!strings.Contains(body, `"text":"I’ll inspect the project first."`) ||
		!strings.Contains(body, `"activity":{"kind":"narration"}`) ||
		!strings.Contains(body, "id: "+third.ID) ||
		!strings.Contains(body, `"text":"editing files"`) ||
		!strings.Contains(body, `"activity":{"kind":"command","command":"pnpm test","exit_code":0}`) {
		t.Errorf("expected structured events in stream, got %q", body)
	}
	if executions.unsubscribed != 1 {
		t.Errorf("expected subscription cleanup, got %d calls", executions.unsubscribed)
	}
}

func TestStreamSessionEventsDeliversLiveEvent(t *testing.T) {
	first := testSessionEvent("sev_one", 1, "starting")
	second := testSessionEvent("sev_two", 2, "running tests")
	second.StreamID = "item_final"
	liveEvents := make(chan execution.Event, 1)
	liveEvents <- second
	close(liveEvents)
	executions := &recordingExecutionService{
		session:      execution.Session{ID: "ses_test"},
		events:       []execution.Event{first},
		subscription: liveEvents,
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/sessions/ses_test/events/stream",
		nil,
	)
	request.Header.Set("Last-Event-ID", first.ID)
	recorder := httptest.NewRecorder()

	New(nil, nil, nil, executions, nil, nil).ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if !strings.Contains(body, "id: "+second.ID) ||
		!strings.Contains(body, `"text":"running tests"`) ||
		!strings.Contains(body, `"stream_id":"item_final"`) {
		t.Errorf("expected live event in stream, got %q", body)
	}
}

func TestStreamSessionEventsDeliversIdlessMessagePreview(t *testing.T) {
	liveEvents := make(chan execution.Event, 1)
	livePreviews := make(chan execution.MessagePreview, 1)
	livePreviews <- execution.MessagePreview{
		SessionID: "ses_test", AttemptID: "att_test", StreamID: "item_final",
		Text: "Answer in prog", OccurredAt: time.Now().UTC(),
	}
	close(livePreviews)
	final := testSessionEvent("sev_final", 1, "Answer in progress")
	final.StreamID = "item_final"
	liveEvents <- final
	close(liveEvents)
	executions := &recordingExecutionService{
		session:             execution.Session{ID: "ses_test"},
		subscription:        liveEvents,
		previewSubscription: livePreviews,
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/sessions/ses_test/events/stream",
		nil,
	)
	recorder := httptest.NewRecorder()

	New(nil, nil, nil, executions, nil, nil).ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if !strings.Contains(body, "event: message_preview\n") ||
		!strings.Contains(body, `data: {"stream_id":"item_final","text":"Answer in prog"}`) {
		t.Fatalf("expected preview frame, got %q", body)
	}
	previewStart := strings.Index(body, "event: message_preview\n")
	previewEnd := strings.Index(body[previewStart:], "\n\n")
	if strings.Contains(body[previewStart:previewStart+previewEnd], "id:") {
		t.Fatalf("preview unexpectedly had SSE id: %q", body)
	}
	if finalStart := strings.Index(body, "id: sev_final"); finalStart < previewStart {
		t.Fatalf("durable final preceded its preview: %q", body)
	}
}

func TestStreamSessionEventsRejectsUnknownLastEventID(t *testing.T) {
	executions := &recordingExecutionService{
		session: execution.Session{ID: "ses_test"},
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/sessions/ses_test/events/stream",
		nil,
	)
	request.Header.Set("Last-Event-ID", "sev_unknown")
	recorder := httptest.NewRecorder()

	New(nil, nil, nil, executions, nil, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"code":"last_event_id_not_found"`) {
		t.Errorf("unexpected response %q", recorder.Body.String())
	}
}

func testSessionEvent(id string, sequence int64, text string) execution.Event {
	return execution.Event{
		ID: id, SessionID: "ses_test", Sequence: sequence,
		Type: worker.EventActivity, Text: text,
		OccurredAt: time.Date(2026, time.September, 9, 1, 0, int(sequence), 0, time.UTC),
	}
}
