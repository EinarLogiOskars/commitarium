package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

const (
	eventStreamHeartbeat    = 15 * time.Second
	eventStreamWriteTimeout = 10 * time.Second
)

var errLastEventNotFound = errors.New("last event ID not found")

func (api *API) streamFeatureEventsHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	featureID := r.PathValue("id")
	if _, err := api.features.GetByID(r.Context(), projectID, featureID); err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
			return
		}
		log.Printf("verify feature %q for project %q: %v", featureID, projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming is not supported")
		return
	}

	liveEvents, unsubscribe := api.workflow.SubscribeFeatureEvents(featureID)
	defer unsubscribe()

	persistedEvents, err := api.workflow.EventsForFeature(r.Context(), featureID)
	if err != nil {
		log.Printf("list events for feature %q: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	lastSequence, err := sequenceForLastEvent(
		persistedEvents,
		strings.TrimSpace(r.Header.Get("Last-Event-ID")),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "last_event_id_not_found", "Last-Event-ID does not belong to this feature")
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
		if err := writeFeatureEvent(w, event); err != nil {
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
		case event, ok := <-liveEvents:
			if !ok {
				return
			}
			if event.Sequence <= lastSequence {
				continue
			}
			if err := writeFeatureEvent(w, event); err != nil {
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

func sequenceForLastEvent(events []workflow.Event, lastEventID string) (int64, error) {
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

func writeFeatureEvent(w http.ResponseWriter, event workflow.Event) error {
	response, err := newFeatureEventResponse(event)
	if err != nil {
		return fmt.Errorf("create event response: %w", err)
	}
	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode event response: %w", err)
	}
	if strings.ContainsAny(event.ID, "\r\n") || strings.ContainsAny(string(event.Type), "\r\n") {
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
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

func setEventStreamWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(
		time.Now().Add(eventStreamWriteTimeout),
	)
}
