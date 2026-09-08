package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestStreamFeatureEventsResumesAfterLastEventID(t *testing.T) {
	first := testStateChangedEvent(t, "evt_one", 1, feature.StateDraft, feature.StatePlanning)
	second := testStateChangedEvent(t, "evt_two", 2, feature.StatePlanning, feature.StateImplementing)
	features := &recordingFeatureService{
		getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"},
	}
	workflows := &recordingWorkflowService{eventsResult: []workflow.Event{first, second}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_test/features/fea_test/events/stream",
		nil,
	)
	request.Header.Set("Last-Event-ID", first.ID)
	cancelledContext, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(cancelledContext)
	recorder := httptest.NewRecorder()

	New(nil, features, workflows, nil, nil, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Errorf("expected event-stream content type, got %q", contentType)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "id: "+first.ID) {
		t.Errorf("expected first event to be skipped, got %q", body)
	}
	if !strings.Contains(body, "id: "+second.ID) ||
		!strings.Contains(body, `"state":"implementing"`) {
		t.Errorf("expected second event in stream, got %q", body)
	}
	if workflows.unsubscribed != 1 {
		t.Errorf("expected subscription cleanup, got %d calls", workflows.unsubscribed)
	}
}

func TestStreamFeatureEventsRejectsUnknownLastEventID(t *testing.T) {
	features := &recordingFeatureService{
		getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"},
	}
	workflows := &recordingWorkflowService{}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_test/features/fea_test/events/stream",
		nil,
	)
	request.Header.Set("Last-Event-ID", "evt_unknown")
	recorder := httptest.NewRecorder()

	New(nil, features, workflows, nil, nil, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"code":"last_event_id_not_found"`) {
		t.Errorf("unexpected response %q", recorder.Body.String())
	}
	if workflows.unsubscribed != 1 {
		t.Errorf("expected subscription cleanup, got %d calls", workflows.unsubscribed)
	}
}

func TestStreamFeatureEventsDeliversLiveEvent(t *testing.T) {
	first := testStateChangedEvent(t, "evt_one", 1, feature.StateDraft, feature.StatePlanning)
	second := testStateChangedEvent(t, "evt_two", 2, feature.StatePlanning, feature.StateImplementing)
	liveEvents := make(chan workflow.Event, 1)
	liveEvents <- second
	close(liveEvents)
	features := &recordingFeatureService{
		getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"},
	}
	workflows := &recordingWorkflowService{
		eventsResult: []workflow.Event{first},
		subscription: liveEvents,
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_test/features/fea_test/events/stream",
		nil,
	)
	request.Header.Set("Last-Event-ID", first.ID)
	recorder := httptest.NewRecorder()

	New(nil, features, workflows, nil, nil, nil).ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if !strings.Contains(body, "id: "+second.ID) ||
		!strings.Contains(body, `"state":"implementing"`) {
		t.Errorf("expected live event in stream, got %q", body)
	}
}

func testStateChangedEvent(
	t *testing.T,
	id string,
	sequence int64,
	previousState feature.State,
	state feature.State,
) workflow.Event {
	t.Helper()
	payload, err := workflow.EncodeFeatureStateChangedPayload(previousState, state)
	if err != nil {
		t.Fatalf("encode test payload: %v", err)
	}
	return workflow.Event{
		ID:             id,
		AggregateID:    "fea_test",
		Type:           workflow.EventTypeFeatureStateChanged,
		Actor:          workflow.Actor{Kind: workflow.ActorKindAgent, ID: "agt_coder"},
		OccurredAt:     time.Date(2026, time.September, 8, 20, 0, int(sequence), 0, time.UTC),
		Sequence:       sequence,
		PayloadVersion: workflow.FeatureStateChangedPayloadVersion,
		IdempotencyKey: "cmd_" + id,
		Payload:        payload,
	}
}
