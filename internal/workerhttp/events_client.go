package workerhttp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const MaxEventStreamFrameBytes = 128 * 1024

var (
	ErrStreamProtocol         = errors.New("worker event stream reported a protocol error")
	errEventStreamLineTooLong = errors.New("worker event stream line exceeds the client limit")
)

// StreamProtocolError is a deliberate protocol error sent after an SSE
// response has already started. It is separate from RemoteError because the
// HTTP status is necessarily 200 once streaming begins.
type StreamProtocolError struct {
	ProtocolError ProtocolError
}

func (streamError *StreamProtocolError) Error() string {
	if streamError == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"worker event stream reported %s: %s",
		streamError.ProtocolError.Code,
		streamError.ProtocolError.Message,
	)
}

func (streamError *StreamProtocolError) Unwrap() error {
	return ErrStreamProtocol
}

// EventReader returns one validated event per call to Next. Callers should
// durably store a returned event before calling Next again. The reader never
// reconnects automatically; the caller chooses the next cursor from its own
// durable state. Next must not be called concurrently, but Close may be used to
// interrupt a blocked Next call.
type EventReader struct {
	reference    AttemptReference
	context      context.Context
	body         io.ReadCloser
	scanner      *bufio.Scanner
	lastSequence int64
	frame        sseFrame

	closeOnce sync.Once
	closeErr  error
}

type sseFrame struct {
	id       string
	event    string
	data     string
	retry    string
	hasID    bool
	hasEvent bool
	hasData  bool
	hasRetry bool
	touched  bool
	bytes    int
}

// OpenEventStream opens one authenticated stream after the caller's durable
// event sequence. RequestTimeout is intentionally not applied: its deadline
// would also terminate the long-lived response body. The supplied context
// controls the entire stream lifetime; connection-only timeouts belong in the
// HTTP transport.
func (client *Client) OpenEventStream(
	ctx context.Context,
	reference AttemptReference,
	afterSequence int64,
) (*EventReader, error) {
	if err := reference.Validate(); err != nil {
		return nil, err
	}
	if afterSequence < 0 || afterSequence == math.MaxInt64 {
		return nil, invalid("last event sequence must be nonnegative and leave room for a following event")
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		client.baseURL+attemptPath(reference)+"/events/stream",
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: create event stream request: %v", ErrRequestFailed, err)
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+client.bearerToken)
	if afterSequence > 0 {
		request.Header.Set(LastEventIDHeader, strconv.FormatInt(afterSequence, 10))
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRequestFailed, err)
	}
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		_ = response.Body.Close()
		return nil, fmt.Errorf(
			"%w: worker returned HTTP %d",
			ErrRedirectRefused,
			response.StatusCode,
		)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		payload, readErr := readClientResponse(response)
		if readErr != nil {
			return nil, readErr
		}
		return nil, decodeRemoteError(response.StatusCode, payload)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, invalidClientResponse("event stream status", nil)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		_ = response.Body.Close()
		return nil, invalidClientResponse("event stream Content-Type", err)
	}

	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), MaxEventStreamFrameBytes+1)
	scanner.Split(splitEventStreamLines)
	return &EventReader{
		reference:    reference,
		context:      ctx,
		body:         response.Body,
		scanner:      scanner,
		lastSequence: afterSequence,
	}, nil
}

// Next blocks until it receives one complete activity event, the stream ends,
// or the request context is canceled. SSE retry advice and heartbeat comments
// are transport details and are not returned as agent activity.
func (reader *EventReader) Next() (Event, error) {
	for reader.scanner.Scan() {
		rawLine := reader.scanner.Bytes()
		reader.frame.touched = true
		reader.frame.bytes += len(rawLine)
		if reader.frame.bytes > MaxEventStreamFrameBytes {
			return Event{}, reader.failInvalid("frame exceeds the client limit", nil)
		}
		lineBytes := rawLine
		if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\n' {
			lineBytes = lineBytes[:len(lineBytes)-1]
		}
		if len(lineBytes) > 0 && lineBytes[len(lineBytes)-1] == '\r' {
			lineBytes = lineBytes[:len(lineBytes)-1]
		}
		line := string(lineBytes)
		if line == "" {
			frame := reader.frame
			reader.frame = sseFrame{}
			event, skipped, err := reader.decodeFrame(frame)
			if err != nil {
				_ = reader.Close()
				return Event{}, err
			}
			if skipped {
				continue
			}
			reader.lastSequence = event.Sequence
			return event, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if err := reader.addField(line); err != nil {
			return Event{}, reader.failInvalid("frame field", err)
		}
	}

	if err := reader.scanner.Err(); err != nil {
		_ = reader.Close()
		if contextErr := reader.context.Err(); contextErr != nil {
			return Event{}, fmt.Errorf("%w: %w", ErrRequestFailed, contextErr)
		}
		if errors.Is(err, errEventStreamLineTooLong) {
			return Event{}, invalidClientResponse("event stream line exceeds the client limit", err)
		}
		return Event{}, fmt.Errorf("%w: read event stream: %w", ErrRequestFailed, err)
	}
	_ = reader.Close()
	if reader.frame.touched {
		return Event{}, invalidClientResponse("event stream ended inside a frame", nil)
	}
	return Event{}, io.EOF
}

func (reader *EventReader) addField(line string) error {
	name, value, found := strings.Cut(line, ":")
	if !found {
		value = ""
	}
	if strings.HasPrefix(value, " ") {
		value = strings.TrimPrefix(value, " ")
	}

	switch name {
	case "id":
		if reader.frame.hasID {
			return errors.New("id field is duplicated")
		}
		reader.frame.hasID = true
		reader.frame.id = value
	case "event":
		if reader.frame.hasEvent {
			return errors.New("event field is duplicated")
		}
		reader.frame.hasEvent = true
		reader.frame.event = value
	case "data":
		if reader.frame.hasData {
			return errors.New("data field is duplicated")
		}
		reader.frame.hasData = true
		reader.frame.data = value
	case "retry":
		if reader.frame.hasRetry {
			return errors.New("retry field is duplicated")
		}
		reader.frame.hasRetry = true
		reader.frame.retry = value
	default:
		return fmt.Errorf("field %q is not supported", name)
	}
	return nil
}

func (reader *EventReader) decodeFrame(frame sseFrame) (Event, bool, error) {
	if !frame.hasID && !frame.hasEvent && !frame.hasData && !frame.hasRetry {
		return Event{}, true, nil
	}
	if frame.hasRetry {
		if frame.hasID || frame.hasEvent || frame.hasData {
			return Event{}, false, invalidClientResponse("event stream retry frame", nil)
		}
		if _, err := parsePositiveDecimal(frame.retry); err != nil {
			return Event{}, false, invalidClientResponse("event stream retry value", err)
		}
		return Event{}, true, nil
	}
	if frame.event == "protocol_error" {
		return Event{}, false, decodeStreamProtocolError(frame)
	}
	if !frame.hasID || !frame.hasEvent || !frame.hasData {
		return Event{}, false, invalidClientResponse("event stream activity frame", nil)
	}

	sequence, err := parsePositiveDecimal(frame.id)
	if err != nil {
		return Event{}, false, invalidClientResponse("event stream event ID", err)
	}
	if reader.lastSequence == math.MaxInt64 || sequence != reader.lastSequence+1 {
		return Event{}, false, invalidClientResponse("event stream event sequence", nil)
	}
	var event Event
	if err := decodeStrictJSON([]byte(frame.data), &event); err != nil {
		return Event{}, false, invalidClientResponse("event stream event data", err)
	}
	if err := event.Validate(); err != nil {
		return Event{}, false, invalidClientResponse("event stream event", err)
	}
	if event.AttemptReference != reader.reference ||
		event.Sequence != sequence ||
		string(event.Type) != frame.event {
		return Event{}, false, invalidClientResponse("event stream event identity", nil)
	}
	return event, false, nil
}

func decodeStreamProtocolError(frame sseFrame) error {
	if frame.hasID || !frame.hasEvent || !frame.hasData {
		return invalidClientResponse("event stream protocol error frame", nil)
	}
	var response ErrorResponse
	if err := decodeStrictJSON([]byte(frame.data), &response); err != nil {
		return invalidClientResponse("event stream protocol error data", err)
	}
	if err := response.Validate(); err != nil {
		return invalidClientResponse("event stream protocol error", err)
	}
	return &StreamProtocolError{ProtocolError: response.Error}
}

func (reader *EventReader) failInvalid(part string, cause error) error {
	_ = reader.Close()
	return invalidClientResponse("event stream "+part, cause)
}

func splitEventStreamLines(data []byte, atEOF bool) (int, []byte, error) {
	if index := bytes.IndexByte(data, '\n'); index >= 0 {
		lineLength := index + 1
		if lineLength > MaxEventStreamFrameBytes {
			return 0, nil, errEventStreamLineTooLong
		}
		return lineLength, data[:lineLength], nil
	}
	if len(data) > MaxEventStreamFrameBytes {
		return 0, nil, errEventStreamLineTooLong
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func parsePositiveDecimal(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("value is empty")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("value is not an unsigned decimal integer")
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return 0, errors.New("value is not a positive decimal integer")
	}
	if strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("value is not in canonical decimal form")
	}
	return parsed, nil
}

// Close releases the HTTP response body. It is safe to call more than once and
// may be used to unblock a concurrent Next call when the underlying transport
// supports closing a response body concurrently with reads.
func (reader *EventReader) Close() error {
	reader.closeOnce.Do(func() {
		reader.closeErr = reader.body.Close()
	})
	return reader.closeErr
}
