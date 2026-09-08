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
	second := testSessionEvent("sev_two", 2, "editing files")
	executions := &recordingExecutionService{
		session: execution.Session{ID: "ses_test"},
		events:  []execution.Event{first, second},
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

	New(nil, nil, nil, executions, nil).ServeHTTP(recorder, request)

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
		!strings.Contains(body, `"text":"editing files"`) {
		t.Errorf("expected second event in stream, got %q", body)
	}
	if executions.unsubscribed != 1 {
		t.Errorf("expected subscription cleanup, got %d calls", executions.unsubscribed)
	}
}

func TestStreamSessionEventsDeliversLiveEvent(t *testing.T) {
	first := testSessionEvent("sev_one", 1, "starting")
	second := testSessionEvent("sev_two", 2, "running tests")
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

	New(nil, nil, nil, executions, nil).ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if !strings.Contains(body, "id: "+second.ID) ||
		!strings.Contains(body, `"text":"running tests"`) {
		t.Errorf("expected live event in stream, got %q", body)
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

	New(nil, nil, nil, executions, nil).ServeHTTP(recorder, request)

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
