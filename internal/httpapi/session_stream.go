package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func (api *API) streamSessionEventsHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if _, err := api.execution.GetSession(r.Context(), sessionID); err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session_not_found", "session not found")
			return
		}
		log.Printf("verify streamed session %q: %v", sessionID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming is not supported")
		return
	}

	liveEvents, unsubscribe := api.execution.SubscribeSessionEvents(sessionID)
	defer unsubscribe()
	persistedEvents, err := api.execution.EventsForSession(r.Context(), sessionID)
	if err != nil {
		log.Printf("list events for streamed session %q: %v", sessionID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	lastSequence, err := sequenceForLastSessionEvent(
		persistedEvents,
		strings.TrimSpace(r.Header.Get("Last-Event-ID")),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "last_event_id_not_found", "Last-Event-ID does not belong to this session")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	setEventStreamWriteDeadline(w)
	if _, err := fmt.Fprint(w, "retry: 1000\n\n"); err != nil {
		return
	}
	flusher.Flush()
	for _, event := range persistedEvents {
		if event.Sequence <= lastSequence {
			continue
		}
		if err := writeSessionEvent(w, event); err != nil {
			return
		}
		lastSequence = event.Sequence
		flusher.Flush()
	}

	heartbeat := time.NewTicker(eventStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-liveEvents:
			if !open {
				return
			}
			if event.Sequence <= lastSequence {
				continue
			}
			if err := writeSessionEvent(w, event); err != nil {
				return
			}
			lastSequence = event.Sequence
			flusher.Flush()
		case <-heartbeat.C:
			setEventStreamWriteDeadline(w)
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func sequenceForLastSessionEvent(
	events []execution.Event,
	lastEventID string,
) (int64, error) {
	if lastEventID == "" {
		return 0, nil
	}
	for _, event := range events {
		if event.ID == lastEventID {
			return event.Sequence, nil
		}
	}
	return 0, errLastEventNotFound
}

func writeSessionEvent(w http.ResponseWriter, event execution.Event) error {
	data, err := json.Marshal(sessionEventResponse{
		ID: event.ID, Sequence: event.Sequence, Type: event.Type,
		Text: event.Text, OccurredAt: event.OccurredAt,
	})
	if err != nil {
		return fmt.Errorf("encode session event response: %w", err)
	}
	if strings.ContainsAny(event.ID, "\r\n") ||
		strings.ContainsAny(string(event.Type), "\r\n") {
		return errors.New("event metadata contains a newline")
	}
	setEventStreamWriteDeadline(w)
	if _, err := fmt.Fprintf(
		w,
		"id: %s\nevent: %s\ndata: %s\n\n",
		event.ID,
		event.Type,
		data,
	); err != nil {
		return fmt.Errorf("write session event: %w", err)
	}
	return nil
}
