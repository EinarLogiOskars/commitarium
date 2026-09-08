package workerhttp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type recordingEventSource struct {
	stream EventStream
	err    error

	calls         int
	reference     AttemptReference
	afterSequence int64
}

func (source *recordingEventSource) OpenEventStream(
	_ context.Context,
	reference AttemptReference,
	afterSequence int64,
) (EventStream, error) {
	source.calls++
	source.reference = reference
	source.afterSequence = afterSequence
	return source.stream, source.err
}

func TestNewServerValidatesEventStreamConfiguration(t *testing.T) {
	closedEvents := make(chan Event)
	close(closedEvents)
	source := &recordingEventSource{stream: EventStream{
		Live:  closedEvents,
		Close: func() {},
	}}

	tests := []struct {
		name   string
		config ServerConfig
	}{
		{
			name: "replay capability without source",
			config: withServerCapabilities(
				testServerConfig(),
				[]Capability{CapabilityStart, CapabilityEventReplay},
			),
		},
		{
			name: "source without replay capability",
			config: func() ServerConfig {
				config := testServerConfig()
				config.EventSource = source
				return config
			}(),
		},
		{
			name: "negative heartbeat",
			config: func() ServerConfig {
				config := eventStreamServerConfig(source)
				config.EventStreamHeartbeat = -time.Second
				return config
			}(),
		},
		{
			name: "negative write timeout",
			config: func() ServerConfig {
				config := eventStreamServerConfig(source)
				config.EventStreamWriteTimeout = -time.Second
				return config
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewServer(test.config, &recordingService{attempt: validServerAttempt()})
			if !errors.Is(err, ErrInvalidServerConfig) {
				t.Fatalf("NewServer error = %v, want ErrInvalidServerConfig", err)
			}
		})
	}
}

func TestEventStreamRequiresAuthenticationMethodAndCapability(t *testing.T) {
	reference := validAttemptReference()
	path := eventStreamPath(reference)
	closedEvents := make(chan Event)
	close(closedEvents)
	source := &recordingEventSource{stream: EventStream{
		Live:  closedEvents,
		Close: func() {},
	}}
	server := newTestServer(t, eventStreamServerConfig(source), &recordingService{attempt: validServerAttempt()})

	unauthorized := serve(server, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}
	assertProtocolError(t, unauthorized, ErrorUnauthorized)

	wrongMethod := serve(server, authorizedRequest(http.MethodPost, path, ""))
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong-method status = %d, want %d", wrongMethod.Code, http.StatusMethodNotAllowed)
	}
	assertProtocolError(t, wrongMethod, ErrorMethodNotAllowed)
	if got := wrongMethod.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want %q", got, http.MethodGet)
	}

	withoutCapability := newTestServer(
		t,
		testServerConfig(),
		&recordingService{attempt: validServerAttempt()},
	)
	unsupported := serve(withoutCapability, authorizedRequest(http.MethodGet, path, ""))
	if unsupported.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsupported status = %d, want %d", unsupported.Code, http.StatusUnprocessableEntity)
	}
	assertProtocolError(t, unsupported, ErrorUnsupportedOperation)

	if source.calls != 0 {
		t.Fatalf("event source calls = %d, want 0", source.calls)
	}
}

func TestEventStreamReplaysAfterSequenceThenDeliversLiveEvents(t *testing.T) {
	reference := validAttemptReference()
	live := make(chan Event, 2)
	live <- validWorkerEvent(reference, 4, EventActivity, "Running Go tests")
	live <- validWorkerEvent(reference, 5, EventAttemptTerminal, "Implementation completed")
	close(live)
	closeCalls := 0
	source := &recordingEventSource{stream: EventStream{
		Replay: []Event{
			validWorkerEvent(reference, 2, EventMessage, "I will inspect the repository first."),
			validWorkerEvent(reference, 3, EventActivity, "Editing the worker transport"),
		},
		ReplayThrough: 3,
		Live:          live,
		Close:         func() { closeCalls++ },
	}}
	server := newTestServer(t, eventStreamServerConfig(source), &recordingService{attempt: validServerAttempt()})
	request := authorizedRequest(http.MethodGet, eventStreamPath(reference), "")
	request.Header.Set(LastEventIDHeader, "1")

	response := serve(server, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-cache, no-store" {
		t.Fatalf("Cache-Control = %q, want no-cache, no-store", got)
	}
	if got := response.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	if source.calls != 1 || source.reference != reference || source.afterSequence != 1 {
		t.Fatalf(
			"OpenEventStream call = %d, reference = %+v, after = %d",
			source.calls,
			source.reference,
			source.afterSequence,
		)
	}
	if closeCalls != 1 {
		t.Fatalf("stream Close calls = %d, want 1", closeCalls)
	}

	body := response.Body.String()
	if !strings.HasPrefix(body, "retry: 1000\n\n") {
		t.Fatalf("stream does not begin with retry advice: %q", body)
	}
	for sequence := int64(2); sequence <= 5; sequence++ {
		if count := strings.Count(body, fmt.Sprintf("id: %d\n", sequence)); count != 1 {
			t.Errorf("event sequence %d occurs %d times, want once; body=%s", sequence, count, body)
		}
	}
	if strings.Contains(body, "id: 1\n") {
		t.Fatalf("stream replayed the acknowledged event: %s", body)
	}
	if !strings.Contains(body, "event: activity\n") ||
		!strings.Contains(body, `"text":"Running Go tests"`) {
		t.Fatalf("stream omitted structured activity data: %s", body)
	}
}

func TestEventStreamWithoutCursorStartsAtFirstEvent(t *testing.T) {
	reference := validAttemptReference()
	live := make(chan Event)
	close(live)
	source := &recordingEventSource{stream: EventStream{
		Replay:        []Event{validWorkerEvent(reference, 1, EventActivity, "Inspecting repository state")},
		ReplayThrough: 1,
		Live:          live,
		Close:         func() {},
	}}
	server := newTestServer(t, eventStreamServerConfig(source), &recordingService{attempt: validServerAttempt()})

	response := serve(server, authorizedRequest(http.MethodGet, eventStreamPath(reference), ""))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "id: 1\n") {
		t.Fatalf("unexpected initial stream response: status=%d body=%s", response.Code, response.Body.String())
	}
	if source.afterSequence != 0 {
		t.Fatalf("after sequence = %d, want 0", source.afterSequence)
	}
}

func TestEventStreamRejectsInvalidLastEventIDBeforeOpeningSource(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "blank", values: []string{""}},
		{name: "zero", values: []string{"0"}},
		{name: "negative", values: []string{"-1"}},
		{name: "not decimal", values: []string{"event_one"}},
		{name: "maximum sequence", values: []string{strconv.FormatInt(math.MaxInt64, 10)}},
		{name: "duplicate", values: []string{"1", "2"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			closedEvents := make(chan Event)
			close(closedEvents)
			source := &recordingEventSource{stream: EventStream{Live: closedEvents, Close: func() {}}}
			server := newTestServer(t, eventStreamServerConfig(source), &recordingService{attempt: validServerAttempt()})
			request := authorizedRequest(http.MethodGet, eventStreamPath(validAttemptReference()), "")
			for _, value := range test.values {
				request.Header.Add(LastEventIDHeader, value)
			}

			response := serve(server, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			assertProtocolError(t, response, ErrorInvalidRequest)
			if source.calls != 0 {
				t.Fatalf("event source calls = %d, want 0", source.calls)
			}
		})
	}
}

func TestEventStreamReturnsSourceErrorBeforeStreaming(t *testing.T) {
	source := &recordingEventSource{err: NewServiceError(
		ErrorNotFound,
		"attempt event stream was not found",
		false,
		errors.New("journal entry absent"),
	)}
	server := newTestServer(t, eventStreamServerConfig(source), &recordingService{attempt: validServerAttempt()})

	response := serve(server, authorizedRequest(http.MethodGet, eventStreamPath(validAttemptReference()), ""))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	assertProtocolError(t, response, ErrorNotFound)
}

func TestEventStreamRejectsInvalidReplayBeforeStreaming(t *testing.T) {
	reference := validAttemptReference()
	wrongReference := reference
	wrongReference.AttemptID = "att_wrong"
	closedEvents := func() <-chan Event {
		channel := make(chan Event)
		close(channel)
		return channel
	}

	tests := []struct {
		name   string
		stream EventStream
	}{
		{
			name:   "missing live channel",
			stream: EventStream{Close: func() {}},
		},
		{
			name:   "missing close function",
			stream: EventStream{Live: closedEvents()},
		},
		{
			name: "boundary before cursor",
			stream: EventStream{
				ReplayThrough: 0,
				Live:          closedEvents(),
				Close:         func() {},
			},
		},
		{
			name: "incomplete replay",
			stream: EventStream{
				ReplayThrough: 3,
				Live:          closedEvents(),
				Close:         func() {},
			},
		},
		{
			name: "sequence gap",
			stream: EventStream{
				Replay:        []Event{validWorkerEvent(reference, 3, EventActivity, "Skipped an event")},
				ReplayThrough: 2,
				Live:          closedEvents(),
				Close:         func() {},
			},
		},
		{
			name: "wrong attempt",
			stream: EventStream{
				Replay:        []Event{validWorkerEvent(wrongReference, 2, EventActivity, "Wrong attempt")},
				ReplayThrough: 2,
				Live:          closedEvents(),
				Close:         func() {},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			closeCalls := 0
			if test.stream.Close != nil {
				test.stream.Close = func() { closeCalls++ }
			}
			source := &recordingEventSource{stream: test.stream}
			server := newTestServer(t, eventStreamServerConfig(source), &recordingService{attempt: validServerAttempt()})
			request := authorizedRequest(http.MethodGet, eventStreamPath(reference), "")
			request.Header.Set(LastEventIDHeader, "1")

			response := serve(server, request)
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusInternalServerError, response.Body.String())
			}
			assertProtocolError(t, response, ErrorInvalidEventStream)
			if test.stream.Close != nil && closeCalls != 1 {
				t.Fatalf("stream Close calls = %d, want 1", closeCalls)
			}
		})
	}
}

func TestInvalidLiveEventPublishesProtocolErrorAndClosesStream(t *testing.T) {
	reference := validAttemptReference()
	live := make(chan Event, 1)
	live <- validWorkerEvent(reference, 2, EventActivity, "Sequence one is missing")
	close(live)
	source := &recordingEventSource{stream: EventStream{
		Live:  live,
		Close: func() {},
	}}
	server := newTestServer(t, eventStreamServerConfig(source), &recordingService{attempt: validServerAttempt()})

	response := serve(server, authorizedRequest(http.MethodGet, eventStreamPath(reference), ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: protocol_error\n") ||
		!strings.Contains(body, `"code":"invalid_event_stream"`) {
		t.Fatalf("stream omitted terminal protocol error: %s", body)
	}
	if strings.Contains(body, "id: 2\n") {
		t.Fatalf("invalid live event was published: %s", body)
	}
}

func TestEventStreamSendsHeartbeatAndClosesSubscriptionOnDisconnect(t *testing.T) {
	live := make(chan Event)
	closed := make(chan struct{})
	source := &recordingEventSource{stream: EventStream{
		Live:  live,
		Close: func() { close(closed) },
	}}
	config := eventStreamServerConfig(source)
	config.EventStreamHeartbeat = 2 * time.Millisecond
	server := newTestServer(t, config, &recordingService{attempt: validServerAttempt()})
	requestContext, cancel := context.WithCancel(context.Background())
	request := authorizedRequest(http.MethodGet, eventStreamPath(validAttemptReference()), "").WithContext(requestContext)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(response, request)
		close(done)
	}()

	time.Sleep(15 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after request cancellation")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("stream subscription was not closed")
	}
	if !strings.Contains(response.Body.String(), ": keep-alive\n\n") {
		t.Fatalf("stream did not send a heartbeat: %s", response.Body.String())
	}
}

func eventStreamServerConfig(source EventSource) ServerConfig {
	config := testServerConfig()
	config.Capabilities = append(config.Capabilities, CapabilityEventReplay)
	config.EventSource = source
	return config
}

func eventStreamPath(reference AttemptReference) string {
	return attemptPath(reference) + "/events/stream"
}

func validWorkerEvent(
	reference AttemptReference,
	sequence int64,
	eventType EventType,
	text string,
) Event {
	return Event{
		AttemptReference: reference,
		Sequence:         sequence,
		Type:             eventType,
		Text:             text,
		OccurredAt:       time.Date(2026, time.September, 8, 14, 0, int(sequence), 0, time.UTC),
		Redaction:        RedactionMetadata{},
	}
}
