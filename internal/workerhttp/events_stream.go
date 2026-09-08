package workerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	LastEventIDHeader         = "Last-Event-ID"
	defaultStreamHeartbeat    = 15 * time.Second
	defaultStreamWriteTimeout = 10 * time.Second
	streamReconnectDelayMilli = 1000
)

// EventSource provides one consistent handoff from durable replay to live
// delivery. OpenEventStream must subscribe to future events at the same
// boundary represented by ReplayThrough, so no event can occur between the
// history query and the live subscription without appearing in either Replay
// or Live.
type EventSource interface {
	OpenEventStream(
		ctx context.Context,
		reference AttemptReference,
		afterSequence int64,
	) (EventStream, error)
}

// EventStream contains every event after the requested cursor through
// ReplayThrough. Live begins with ReplayThrough+1. A completed attempt uses a
// closed Live channel. Close releases only this subscriber; it must not stop
// the provider process.
type EventStream struct {
	Replay        []Event
	ReplayThrough int64
	Live          <-chan Event
	Close         func()
}

func validateEventStreamConfig(
	config ServerConfig,
	supported map[Capability]struct{},
) (time.Duration, time.Duration, error) {
	_, advertisesReplay := supported[CapabilityEventReplay]
	if advertisesReplay && config.EventSource == nil {
		return 0, 0, fmt.Errorf(
			"%w: event source is required when event replay is advertised",
			ErrInvalidServerConfig,
		)
	}
	if !advertisesReplay && config.EventSource != nil {
		return 0, 0, fmt.Errorf(
			"%w: event source requires the event replay capability",
			ErrInvalidServerConfig,
		)
	}
	if config.EventStreamHeartbeat < 0 {
		return 0, 0, fmt.Errorf(
			"%w: event stream heartbeat cannot be negative",
			ErrInvalidServerConfig,
		)
	}
	if config.EventStreamWriteTimeout < 0 {
		return 0, 0, fmt.Errorf(
			"%w: event stream write timeout cannot be negative",
			ErrInvalidServerConfig,
		)
	}

	heartbeat := config.EventStreamHeartbeat
	if heartbeat == 0 {
		heartbeat = defaultStreamHeartbeat
	}
	writeTimeout := config.EventStreamWriteTimeout
	if writeTimeout == 0 {
		writeTimeout = defaultStreamWriteTimeout
	}
	return heartbeat, writeTimeout, nil
}

func (server *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	reference, ok := server.readReference(w, r)
	if !ok || !server.requireCapability(w, CapabilityEventReplay) {
		return
	}
	afterSequence, ok := server.readLastEventSequence(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		server.writeProtocolError(w, http.StatusInternalServerError, ProtocolError{
			Code:      ErrorInvalidEventStream,
			Message:   "HTTP response writer does not support streaming",
			Retryable: true,
		})
		return
	}

	stream, err := server.eventSource.OpenEventStream(r.Context(), reference, afterSequence)
	if err != nil {
		server.writeServiceError(w, err)
		return
	}
	if stream.Close != nil {
		defer stream.Close()
	}
	replay, err := prepareEventReplay(reference, afterSequence, stream)
	if err != nil {
		server.writeProtocolError(w, http.StatusInternalServerError, invalidEventStreamError())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	server.setStreamWriteDeadline(w)
	if _, err := fmt.Fprintf(w, "retry: %d\n\n", streamReconnectDelayMilli); err != nil {
		return
	}
	flusher.Flush()

	lastSequence := afterSequence
	for index, payload := range replay {
		event := stream.Replay[index]
		if err := server.writeEventFrame(w, event, payload); err != nil {
			return
		}
		lastSequence = event.Sequence
		flusher.Flush()
	}

	heartbeat := time.NewTicker(server.heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-stream.Live:
			if !open {
				return
			}
			payload, err := validateAndEncodeLiveEvent(reference, lastSequence, event)
			if err != nil {
				server.writeStreamProtocolError(w, flusher)
				return
			}
			if err := server.writeEventFrame(w, event, payload); err != nil {
				return
			}
			lastSequence = event.Sequence
			flusher.Flush()
		case <-heartbeat.C:
			server.setStreamWriteDeadline(w)
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (server *Server) readLastEventSequence(w http.ResponseWriter, r *http.Request) (int64, bool) {
	values := r.Header.Values(LastEventIDHeader)
	if len(values) == 0 {
		return 0, true
	}
	if len(values) != 1 {
		server.writeInvalidLastEventID(w)
		return 0, false
	}
	value := strings.TrimSpace(values[0])
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence < 1 || sequence == math.MaxInt64 {
		server.writeInvalidLastEventID(w)
		return 0, false
	}
	return sequence, true
}

func (server *Server) writeInvalidLastEventID(w http.ResponseWriter) {
	server.writeProtocolError(w, http.StatusBadRequest, ProtocolError{
		Code:    ErrorInvalidRequest,
		Message: LastEventIDHeader + " must be one positive decimal event sequence",
	})
}

func prepareEventReplay(
	reference AttemptReference,
	afterSequence int64,
	stream EventStream,
) ([][]byte, error) {
	if stream.Live == nil || stream.Close == nil {
		return nil, errors.New("event stream requires a live channel and close function")
	}
	if stream.ReplayThrough < afterSequence {
		return nil, errors.New("event replay boundary precedes the requested cursor")
	}
	if int64(len(stream.Replay)) != stream.ReplayThrough-afterSequence {
		return nil, errors.New("event replay does not cover its declared boundary")
	}

	payloads := make([][]byte, len(stream.Replay))
	for index, event := range stream.Replay {
		expectedSequence := afterSequence + int64(index) + 1
		if err := validateStreamEvent(reference, expectedSequence, event); err != nil {
			return nil, err
		}
		payload, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("encode replayed event: %w", err)
		}
		payloads[index] = payload
	}
	return payloads, nil
}

func validateAndEncodeLiveEvent(
	reference AttemptReference,
	lastSequence int64,
	event Event,
) ([]byte, error) {
	if lastSequence == math.MaxInt64 {
		return nil, errors.New("event sequence is exhausted")
	}
	if err := validateStreamEvent(reference, lastSequence+1, event); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode live event: %w", err)
	}
	return payload, nil
}

func validateStreamEvent(
	reference AttemptReference,
	expectedSequence int64,
	event Event,
) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if event.AttemptReference != reference {
		return errors.New("event belongs to a different attempt")
	}
	if event.Sequence != expectedSequence {
		return fmt.Errorf(
			"event sequence is %d, expected %d",
			event.Sequence,
			expectedSequence,
		)
	}
	return nil
}

func (server *Server) writeEventFrame(w http.ResponseWriter, event Event, payload []byte) error {
	server.setStreamWriteDeadline(w)
	_, err := fmt.Fprintf(
		w,
		"id: %d\nevent: %s\ndata: %s\n\n",
		event.Sequence,
		event.Type,
		payload,
	)
	return err
}

func (server *Server) writeStreamProtocolError(w http.ResponseWriter, flusher http.Flusher) {
	payload, err := json.Marshal(ErrorResponse{Error: invalidEventStreamError()})
	if err != nil {
		return
	}
	server.setStreamWriteDeadline(w)
	if _, err := fmt.Fprintf(w, "event: protocol_error\ndata: %s\n\n", payload); err == nil {
		flusher.Flush()
	}
}

func (server *Server) setStreamWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(server.writeTimeout))
}

func invalidEventStreamError() ProtocolError {
	return ProtocolError{
		Code:      ErrorInvalidEventStream,
		Message:   "worker produced an invalid event stream",
		Retryable: true,
	}
}
