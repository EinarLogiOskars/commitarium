package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

type acceptGoalRequest struct {
	Goal string `json:"goal"`
}

type acceptGoalResponse struct {
	FeatureID  string    `json:"feature_id"`
	SessionID  string    `json:"session_id"`
	Goal       string    `json:"goal"`
	EventID    string    `json:"event_id"`
	Sequence   int64     `json:"sequence"`
	AcceptedAt time.Time `json:"accepted_at"`
}

func (api *API) acceptGoalHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if _, err := api.execution.GetSession(r.Context(), sessionID); err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session_not_found", "session not found")
			return
		}
		log.Printf("verify goal acceptance session %q: %v", sessionID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	request := acceptGoalRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	event, err := api.controller.AcceptGoal(
		r.Context(), sessionID, request.Goal,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID},
		idempotencyKey,
	)
	if err != nil {
		switch {
		case errors.Is(err, workflow.ErrInvalidGoalAcceptance):
			writeError(w, http.StatusBadRequest, "goal_required", "accepted goal is required")
		case errors.Is(err, workflow.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different operation")
		case errors.Is(err, workflow.ErrGoalAlreadyAccepted):
			writeError(w, http.StatusConflict, "goal_already_accepted", "feature goal has already been accepted")
		case errors.Is(err, workflow.ErrGoalAcceptanceNotAllowed),
			errors.Is(err, feature.ErrNotFound):
			writeError(w, http.StatusConflict, "goal_acceptance_not_allowed", "goal can only be accepted from its waiting draft lead session")
		default:
			log.Printf("accept goal from session %q: %v", sessionID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	payload, err := workflow.DecodeGoalAcceptedPayload(event.PayloadVersion, event.Payload)
	if err != nil {
		log.Printf("decode goal acceptance event %q: %v", event.ID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, acceptGoalResponse{
		FeatureID: event.AggregateID, SessionID: payload.SessionID, Goal: payload.Goal,
		EventID: event.ID, Sequence: event.Sequence, AcceptedAt: event.OccurredAt,
	}, "goal acceptance")
}
