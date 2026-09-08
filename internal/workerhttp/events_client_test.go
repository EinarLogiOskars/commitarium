package workerhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientAndServerRoundTripEventStream(t *testing.T) {
	reference := validAttemptReference()
	live := make(chan Event, 2)
	live <- validWorkerEvent(reference, 4, EventActivity, "Running Go tests")
	live <- validWorkerEvent(reference, 5, EventAttemptTerminal, "Implementation completed")
	close(live)
	source := &recordingEventSource{stream: EventStream{
		Replay: []Event{
			validWorkerEvent(reference, 2, EventMessage, "Inspecting the repository"),
			validWorkerEvent(reference, 3, EventActivity, "Editing the worker client"),
		},
		ReplayThrough: 3,
		Live:          live,
		Close:         func() {},
	}}
	workerServer := newTestServer(
		t,
		eventStreamServerConfig(source),
		&recordingService{attempt: validServerAttempt()},
	)
	client := newTestClient(t, workerServer, time.Second)

	reader, err := client.OpenEventStream(context.Background(), reference, 1)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer reader.Close()
	for wantSequence := int64(2); wantSequence <= 5; wantSequence++ {
		event, err := reader.Next()
		if err != nil {
			t.Fatalf("read event %d: %v", wantSequence, err)
		}
		if event.Sequence != wantSequence || event.AttemptReference != reference {
			t.Fatalf("event = %+v, want sequence %d for %+v", event, wantSequence, reference)
		}
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end error = %v, want EOF", err)
	}
	if source.afterSequence != 1 || source.reference != reference {
		t.Fatalf("source received reference=%+v after=%d", source.reference, source.afterSequence)
	}
}

func TestClientEventStreamSendsAuthenticationAndDurableCursor(t *testing.T) {
	reference := validAttemptReference()
	event := validWorkerEvent(reference, 8, EventActivity, "Reviewing changes")
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != attemptPath(reference)+"/events/stream" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", got)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+testBearerToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get(LastEventIDHeader); got != "7" {
			t.Errorf("Last-Event-ID = %q, want 7", got)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = fmt.Fprint(w, workerEventFrame(t, event))
	})
	client := newTestClient(t, handler, time.Second)

	reader, err := client.OpenEventStream(context.Background(), reference, 7)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer reader.Close()
	eventResult, err := reader.Next()
	if err != nil || eventResult.Sequence != 8 {
		t.Fatalf("event=%+v error=%v", eventResult, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("network calls = %d, want 1", got)
	}
}

func TestClientEventStreamRejectsInvalidInputBeforeNetworkCall(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected network call")
	})
	client := newTestClientWithTransport(t, transport, time.Second)

	tests := []struct {
		name      string
		reference AttemptReference
		after     int64
	}{
		{name: "invalid reference", reference: AttemptReference{}, after: 0},
		{name: "negative cursor", reference: validAttemptReference(), after: -1},
		{name: "exhausted cursor", reference: validAttemptReference(), after: math.MaxInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.OpenEventStream(context.Background(), test.reference, test.after)
			if !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("error = %v, want ErrInvalidContract", err)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("network calls = %d, want 0", got)
	}
}

func TestClientEventStreamReturnsTypedHTTPError(t *testing.T) {
	workerServer := newTestServer(
		t,
		testServerConfig(),
		&recordingService{attempt: validServerAttempt()},
	)
	client := newTestClient(t, workerServer, time.Second)

	_, err := client.OpenEventStream(context.Background(), validAttemptReference(), 0)
	if !errors.Is(err, ErrRemote) {
		t.Fatalf("error = %v, want ErrRemote", err)
	}
	var remoteError *RemoteError
	if !errors.As(err, &remoteError) || remoteError.ProtocolError.Code != ErrorUnsupportedOperation {
		t.Fatalf("unexpected remote error: %v", err)
	}
}

func TestClientEventStreamValidatesHTTPResponse(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantError   error
	}{
		{name: "wrong content type", status: http.StatusOK, contentType: "application/json", body: `{}`, wantError: ErrInvalidResponse},
		{name: "unexpected success", status: http.StatusNoContent, contentType: "text/event-stream", wantError: ErrInvalidResponse},
		{name: "redirect", status: http.StatusTemporaryRedirect, contentType: "text/plain", wantError: ErrRedirectRefused},
		{name: "malformed HTTP error", status: http.StatusNotFound, contentType: "application/json", body: `{`, wantError: ErrInvalidResponse},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.status,
					Header:     http.Header{"Content-Type": []string{test.contentType}},
					Body:       io.NopCloser(strings.NewReader(test.body)),
					Request:    request,
				}, nil
			})
			client := newTestClientWithTransport(t, transport, time.Second)
			_, err := client.OpenEventStream(context.Background(), validAttemptReference(), 0)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestClientEventReaderIgnoresRetryAndHeartbeatFrames(t *testing.T) {
	event := validWorkerEvent(validAttemptReference(), 1, EventActivity, "Running tests")
	body := "retry: 1000\n\n: keep-alive\n\n" + workerEventFrame(t, event)
	reader := openStaticEventReader(t, body, 0)

	got, err := reader.Next()
	if err != nil || !reflect.DeepEqual(got, event) {
		t.Fatalf("event=%+v error=%v, want %+v", got, err, event)
	}
}

func TestClientEventReaderReturnsDistinctStreamProtocolError(t *testing.T) {
	body := `event: protocol_error
data: {"error":{"code":"invalid_event_stream","message":"worker produced an invalid event stream","retryable":true}}

`
	reader := openStaticEventReader(t, body, 0)

	_, err := reader.Next()
	if !errors.Is(err, ErrStreamProtocol) || errors.Is(err, ErrRemote) {
		t.Fatalf("error = %v, want only ErrStreamProtocol", err)
	}
	var streamError *StreamProtocolError
	if !errors.As(err, &streamError) || streamError.ProtocolError.Code != ErrorInvalidEventStream {
		t.Fatalf("unexpected stream protocol error: %v", err)
	}
}

func TestClientEventReaderRejectsMalformedOrContradictoryFrames(t *testing.T) {
	reference := validAttemptReference()
	validEvent := validWorkerEvent(reference, 1, EventActivity, "Inspecting files")
	wrongReference := reference
	wrongReference.AttemptID = "att_wrong"
	wrongAttemptEvent := validWorkerEvent(wrongReference, 1, EventActivity, "Wrong attempt")

	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: "unknown: value\n\n"},
		{name: "duplicate data", body: "id: 1\nevent: activity\ndata: {}\ndata: {}\n\n"},
		{name: "retry mixed with activity", body: "retry: 1000\nid: 1\n\n"},
		{name: "invalid retry", body: "retry: soon\n\n"},
		{name: "missing ID", body: "event: activity\ndata: " + marshalTestJSON(t, validEvent) + "\n\n"},
		{name: "nondecimal ID", body: "id: first\nevent: activity\ndata: " + marshalTestJSON(t, validEvent) + "\n\n"},
		{name: "noncanonical ID", body: "id: 01\nevent: activity\ndata: " + marshalTestJSON(t, validEvent) + "\n\n"},
		{name: "sequence gap", body: workerEventFrame(t, withWorkerEventSequence(validEvent, 2))},
		{name: "event name mismatch", body: strings.Replace(workerEventFrame(t, validEvent), "event: activity", "event: message", 1)},
		{name: "wrong attempt", body: workerEventFrame(t, wrongAttemptEvent)},
		{name: "unknown JSON field", body: "id: 1\nevent: activity\ndata: " + strings.TrimSuffix(marshalTestJSON(t, validEvent), "}") + `,"extra":true}` + "\n\n"},
		{name: "invalid protocol error", body: "id: 1\nevent: protocol_error\ndata: {}\n\n"},
		{name: "unfinished frame", body: "id: 1\nevent: activity\n"},
		{name: "oversized line", body: strings.Repeat("x", MaxEventStreamFrameBytes+1) + "\n\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := openStaticEventReader(t, test.body, 0)
			_, err := reader.Next()
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestClientEventStreamDoesNotApplyOrdinaryRequestTimeout(t *testing.T) {
	eventFrame := workerEventFrame(
		t,
		validWorkerEvent(validAttemptReference(), 1, EventActivity, "Still connected"),
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			time.Sleep(30 * time.Millisecond)
			_, _ = io.WriteString(writer, eventFrame)
			_ = writer.Close()
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       reader,
			Request:    request,
		}, nil
	})
	client := newTestClientWithTransport(t, transport, 2*time.Millisecond)
	reader, err := client.OpenEventStream(context.Background(), validAttemptReference(), 0)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer reader.Close()

	event, err := reader.Next()
	if err != nil || event.Sequence != 1 {
		t.Fatalf("event=%+v error=%v; ordinary request timeout affected stream", event, err)
	}
}

func TestClientEventReaderRejectsDuplicateSequence(t *testing.T) {
	reference := validAttemptReference()
	first := validWorkerEvent(reference, 1, EventActivity, "First")
	duplicate := validWorkerEvent(reference, 1, EventActivity, "Duplicate")
	reader := openStaticEventReader(t, workerEventFrame(t, first)+workerEventFrame(t, duplicate), 0)

	if _, err := reader.Next(); err != nil {
		t.Fatalf("read first event: %v", err)
	}
	if _, err := reader.Next(); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("duplicate error = %v, want ErrInvalidResponse", err)
	}
}

func TestClientEventReaderCancellationStopsBlockedRead(t *testing.T) {
	requestContext, cancel := context.WithCancel(context.Background())
	writerReady := make(chan struct{})
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			close(writerReady)
			<-request.Context().Done()
			_ = writer.CloseWithError(request.Context().Err())
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       reader,
			Request:    request,
		}, nil
	})
	client := newTestClientWithTransport(t, transport, time.Second)
	reader, err := client.OpenEventStream(requestContext, validAttemptReference(), 0)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	<-writerReady
	done := make(chan error, 1)
	go func() {
		_, nextErr := reader.Next()
		done <- nextErr
	}()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrRequestFailed) || !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want request failure and context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked Next did not stop after cancellation")
	}
}

func TestClientEventReaderCloseIsIdempotentAndUnblocksRead(t *testing.T) {
	body := newBlockingReadCloser()
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
			Request:    request,
		}, nil
	})
	client := newTestClientWithTransport(t, transport, time.Second)
	reader, err := client.OpenEventStream(context.Background(), validAttemptReference(), 0)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, nextErr := reader.Next()
		done <- nextErr
	}()
	<-body.readStarted
	if err := reader.Close(); err != nil {
		t.Fatalf("close event reader: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close event reader again: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Next")
	}
	if got := body.closeCalls.Load(); got != 1 {
		t.Fatalf("body Close calls = %d, want 1", got)
	}
}

func openStaticEventReader(t *testing.T, body string, afterSequence int64) *EventReader {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, body)
	})
	client := newTestClient(t, handler, time.Second)
	reader, err := client.OpenEventStream(context.Background(), validAttemptReference(), afterSequence)
	if err != nil {
		t.Fatalf("open static event stream: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader
}

func workerEventFrame(t *testing.T, event Event) string {
	t.Helper()
	return fmt.Sprintf(
		"id: %d\nevent: %s\ndata: %s\n\n",
		event.Sequence,
		event.Type,
		marshalTestJSON(t, event),
	)
}

func withWorkerEventSequence(event Event, sequence int64) Event {
	event.Sequence = sequence
	return event
}

type blockingReadCloser struct {
	readStarted chan struct{}
	closed      chan struct{}
	startOnce   sync.Once
	closeOnce   sync.Once
	closeCalls  atomic.Int32
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{
		readStarted: make(chan struct{}),
		closed:      make(chan struct{}),
	}
}

func (body *blockingReadCloser) Read([]byte) (int, error) {
	body.startOnce.Do(func() { close(body.readStarted) })
	<-body.closed
	return 0, io.EOF
}

func (body *blockingReadCloser) Close() error {
	body.closeCalls.Add(1)
	body.closeOnce.Do(func() { close(body.closed) })
	return nil
}
