package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type sessionResponse struct {
	ID                string                  `json:"id"`
	RunID             string                  `json:"run_id"`
	AgentID           string                  `json:"agent_id"`
	Role              worker.Role             `json:"role"`
	Status            execution.SessionStatus `json:"status"`
	ProviderSessionID string                  `json:"provider_session_id"`
	StartedAt         time.Time               `json:"started_at"`
	UpdatedAt         time.Time               `json:"updated_at"`
	EndedAt           *time.Time              `json:"ended_at,omitempty"`
}

type sessionEventResponse struct {
	ID         string           `json:"id"`
	Sequence   int64            `json:"sequence"`
	Type       worker.EventType `json:"type"`
	Text       string           `json:"text"`
	OccurredAt time.Time        `json:"occurred_at"`
}

type sessionCommandRequest struct {
	Type    worker.CommandType `json:"type"`
	Message string             `json:"message"`
}

type sessionCommandResponse struct {
	ID          string                  `json:"id"`
	SessionID   string                  `json:"session_id"`
	Type        worker.CommandType      `json:"type"`
	Message     string                  `json:"message"`
	Status      execution.CommandStatus `json:"status"`
	RequestedAt time.Time               `json:"requested_at"`
	AppliedAt   *time.Time              `json:"applied_at,omitempty"`
	Error       string                  `json:"error,omitempty"`
}

func (api *API) getSessionHandler(w http.ResponseWriter, r *http.Request) {
	session, err := api.execution.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session_not_found", "session not found")
			return
		}
		log.Printf("get session %q: %v", r.PathValue("id"), err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, newSessionResponse(session), "session")
}

func (api *API) getSessionEventsHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if _, err := api.execution.GetSession(r.Context(), sessionID); err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session_not_found", "session not found")
			return
		}
		log.Printf("verify session %q: %v", sessionID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	events, err := api.execution.EventsForSession(r.Context(), sessionID)
	if err != nil {
		log.Printf("list events for session %q: %v", sessionID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	response := make([]sessionEventResponse, 0, len(events))
	for _, event := range events {
		response = append(response, sessionEventResponse{
			ID: event.ID, Sequence: event.Sequence, Type: event.Type,
			Text: event.Text, OccurredAt: event.OccurredAt,
		})
	}
	writeJSON(w, http.StatusOK, response, "session events")
}

func (api *API) sendSessionCommandHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if _, err := api.execution.GetSession(r.Context(), sessionID); err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session_not_found", "session not found")
			return
		}
		log.Printf("verify commanded session %q: %v", sessionID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	request := sessionCommandRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	command, err := api.controller.SendCommand(r.Context(), sessionID, worker.Command{
		ID: idempotencyKey, Type: request.Type, Message: request.Message,
	})
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrInvalidCommand):
			writeError(w, http.StatusBadRequest, "invalid_session_command", "session command is invalid")
		case errors.Is(err, execution.ErrCommandConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different command")
		case errors.Is(err, orchestration.ErrSessionNotActive):
			writeError(w, http.StatusConflict, "session_not_active", "session is not currently active")
		case errors.Is(err, orchestration.ErrCommandNotAllowed):
			writeError(w, http.StatusConflict, "command_not_allowed", "command is not allowed for the current session state")
		default:
			log.Printf("send command %q to session %q: %v", idempotencyKey, sessionID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, newSessionCommandResponse(command), "session command")
}

func newSessionResponse(session execution.Session) sessionResponse {
	return sessionResponse{
		ID: session.ID, RunID: session.RunID, AgentID: session.AgentID,
		Role: session.Role, Status: session.Status,
		ProviderSessionID: session.ProviderSessionID,
		StartedAt:         session.StartedAt, UpdatedAt: session.UpdatedAt, EndedAt: session.EndedAt,
	}
}

func newSessionCommandResponse(command execution.Command) sessionCommandResponse {
	return sessionCommandResponse{
		ID: command.ID, SessionID: command.SessionID, Type: command.Type,
		Message: command.Message, Status: command.Status,
		RequestedAt: command.RequestedAt, AppliedAt: command.AppliedAt, Error: command.Error,
	}
}

func writeJSON(w http.ResponseWriter, status int, value any, label string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode %s response: %v", label, err)
	}
}
