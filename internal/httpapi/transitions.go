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

const localUserID = "usr_local"

type transitionFeatureRequest struct {
	State feature.State `json:"state"`
}

type transitionFeatureResponse struct {
	FeatureID  string        `json:"feature_id"`
	State      feature.State `json:"state"`
	EventID    string        `json:"event_id"`
	Sequence   int64         `json:"sequence"`
	OccurredAt time.Time     `json:"occurred_at"`
}

type featureEventResponse struct {
	ID             string             `json:"id"`
	Type           workflow.EventType `json:"type"`
	Actor          eventActorResponse `json:"actor"`
	OccurredAt     time.Time          `json:"occurred_at"`
	Sequence       int64              `json:"sequence"`
	PayloadVersion int                `json:"payload_version"`
	PreviousState  feature.State      `json:"previous_state,omitempty"`
	State          feature.State      `json:"state,omitempty"`
	Goal           string             `json:"goal,omitempty"`
	SessionID      string             `json:"session_id,omitempty"`
}

type eventActorResponse struct {
	Kind workflow.ActorKind `json:"kind"`
	ID   string             `json:"id"`
}

func (api *API) transitionFeatureHandler(w http.ResponseWriter, r *http.Request) {
	request := transitionFeatureRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	if !request.State.IsValid() {
		writeError(w, http.StatusBadRequest, "invalid_feature_state", "feature state is not recognized")
		return
	}

	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}

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

	event, err := api.workflow.TransitionFeature(
		r.Context(),
		featureID,
		request.State,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID},
		idempotencyKey,
	)
	if err != nil {
		switch {
		case errors.Is(err, feature.ErrInvalidTransition):
			writeError(w, http.StatusConflict, "invalid_feature_transition", "feature cannot transition to the requested state")
		case errors.Is(err, workflow.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different command")
		default:
			log.Printf("transition feature %q: %v", featureID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}

	payload, err := workflow.DecodeFeatureStateChangedPayload(event.PayloadVersion, event.Payload)
	if err != nil {
		log.Printf("decode transition event %q: %v", event.ID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(transitionFeatureResponse{
		FeatureID:  featureID,
		State:      payload.State,
		EventID:    event.ID,
		Sequence:   event.Sequence,
		OccurredAt: event.OccurredAt,
	}); err != nil {
		log.Printf("encode feature transition response: %v", err)
	}
}

func (api *API) getFeatureEventsHandler(w http.ResponseWriter, r *http.Request) {
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

	events, err := api.workflow.EventsForFeature(r.Context(), featureID)
	if err != nil {
		log.Printf("list events for feature %q: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	response := make([]featureEventResponse, 0, len(events))
	for _, event := range events {
		eventResponse, err := newFeatureEventResponse(event)
		if err != nil {
			log.Printf("decode workflow event %q: %v", event.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		response = append(response, eventResponse)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode feature events response: %v", err)
	}
}

func newFeatureEventResponse(event workflow.Event) (featureEventResponse, error) {
	response := featureEventResponse{
		ID:   event.ID,
		Type: event.Type,
		Actor: eventActorResponse{
			Kind: event.Actor.Kind,
			ID:   event.Actor.ID,
		},
		OccurredAt:     event.OccurredAt,
		Sequence:       event.Sequence,
		PayloadVersion: event.PayloadVersion,
	}
	switch event.Type {
	case workflow.EventTypeFeatureStateChanged:
		payload, err := workflow.DecodeFeatureStateChangedPayload(event.PayloadVersion, event.Payload)
		if err != nil {
			return featureEventResponse{}, err
		}
		response.PreviousState = payload.PreviousState
		response.State = payload.State
	case workflow.EventTypeGoalAccepted:
		payload, err := workflow.DecodeGoalAcceptedPayload(event.PayloadVersion, event.Payload)
		if err != nil {
			return featureEventResponse{}, err
		}
		response.Goal = payload.Goal
		response.SessionID = payload.SessionID
	default:
		return featureEventResponse{}, fmt.Errorf("unsupported workflow event type %q", event.Type)
	}
	return response, nil
}
