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
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type planningMessageResponse struct {
	ID         string           `json:"id"`
	Sequence   int64            `json:"sequence"`
	SessionID  string           `json:"session_id"`
	AgentID    string           `json:"agent_id"`
	Role       worker.Role      `json:"role"`
	Type       worker.EventType `json:"type"`
	Text       string           `json:"text"`
	OccurredAt time.Time        `json:"occurred_at"`
}

func (api *API) getPlanningMessagesHandler(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if !api.requirePlanningRun(w, r, runID) {
		return
	}
	messages, err := api.execution.PlanningMessagesForRun(r.Context(), runID)
	if err != nil {
		log.Printf("list planning messages for run %q: %v", runID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	response := make([]planningMessageResponse, 0, len(messages))
	for _, message := range messages {
		response = append(response, newPlanningMessageResponse(message))
	}
	writeJSON(w, http.StatusOK, response, "planning messages")
}

func (api *API) streamPlanningMessagesHandler(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if !api.requirePlanningRun(w, r, runID) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming is not supported")
		return
	}

	live, unsubscribe := api.execution.SubscribePlanningMessages(runID)
	defer unsubscribe()
	persisted, err := api.execution.PlanningMessagesForRun(r.Context(), runID)
	if err != nil {
		log.Printf("list streamed planning messages for run %q: %v", runID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	lastSequence, err := sequenceForLastPlanningMessage(
		persisted, strings.TrimSpace(r.Header.Get("Last-Event-ID")),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, "last_event_id_not_found", "Last-Event-ID does not belong to this planning conversation")
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
	for _, message := range persisted {
		if message.Sequence <= lastSequence {
			continue
		}
		if err := writePlanningMessage(w, message); err != nil {
			return
		}
		lastSequence = message.Sequence
		flusher.Flush()
	}

	heartbeat := time.NewTicker(eventStreamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case message, open := <-live:
			if !open {
				return
			}
			if message.Sequence <= lastSequence {
				continue
			}
			if err := writePlanningMessage(w, message); err != nil {
				return
			}
			lastSequence = message.Sequence
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

func (api *API) requirePlanningRun(w http.ResponseWriter, r *http.Request, runID string) bool {
	if _, err := api.execution.GetRun(r.Context(), runID); err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		} else {
			log.Printf("verify planning run %q: %v", runID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return false
	}
	return true
}

func sequenceForLastPlanningMessage(messages []execution.PlanningMessage, id string) (int64, error) {
	if id == "" {
		return 0, nil
	}
	for _, message := range messages {
		if message.Event.ID == id {
			return message.Sequence, nil
		}
	}
	return 0, errLastEventNotFound
}

func writePlanningMessage(w http.ResponseWriter, message execution.PlanningMessage) error {
	response := newPlanningMessageResponse(message)
	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode planning message: %w", err)
	}
	if strings.ContainsAny(response.ID, "\r\n") {
		return errors.New("planning message ID contains a newline")
	}
	setEventStreamWriteDeadline(w)
	if _, err := fmt.Fprintf(w, "id: %s\nevent: planning_message\ndata: %s\n\n", response.ID, data); err != nil {
		return fmt.Errorf("write planning message: %w", err)
	}
	return nil
}

func newPlanningMessageResponse(message execution.PlanningMessage) planningMessageResponse {
	return planningMessageResponse{
		ID: message.Event.ID, Sequence: message.Sequence,
		SessionID: message.Event.SessionID, AgentID: message.AgentID,
		Role: message.Role, Type: message.Event.Type,
		Text: message.Event.Text, OccurredAt: message.Event.OccurredAt,
	}
}
