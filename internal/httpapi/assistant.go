package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/assistant"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// WorkOrderAssistant clarifies draft work orders into handoff briefs.
type WorkOrderAssistant interface {
	Start(ctx context.Context, projectID, featureID string, provider project.AgentProvider, model string) (assistant.Session, bool, error)
	Get(ctx context.Context, projectID, featureID string) (assistant.Session, error)
	Reply(ctx context.Context, projectID, featureID, message, idempotencyKey string) (assistant.Session, bool, error)
	AcceptBrief(ctx context.Context, projectID, featureID, idempotencyKey string) (feature.Feature, error)
	Reopen(ctx context.Context, projectID, featureID, idempotencyKey string) (feature.Feature, error)
}

type assistantMessageResponse struct {
	Role       string    `json:"role"`
	Text       string    `json:"text"`
	OccurredAt time.Time `json:"occurred_at"`
}

type assistantSessionResponse struct {
	ID        string                     `json:"id"`
	FeatureID string                     `json:"feature_id"`
	Provider  string                     `json:"provider"`
	Model     string                     `json:"model"`
	Status    string                     `json:"status"`
	Message   string                     `json:"message"`
	Messages  []assistantMessageResponse `json:"messages"`
	CreatedAt time.Time                  `json:"created_at"`
	UpdatedAt time.Time                  `json:"updated_at"`
}

func newAssistantSessionResponse(session assistant.Session) assistantSessionResponse {
	messages := make([]assistantMessageResponse, 0, len(session.Messages))
	for _, message := range session.Messages {
		messages = append(messages, assistantMessageResponse{Role: message.Role, Text: message.Text, OccurredAt: message.OccurredAt})
	}
	return assistantSessionResponse{
		ID: session.ID, FeatureID: session.FeatureID, Provider: string(session.Provider), Model: session.Model,
		Status: string(session.Status), Message: session.Message, Messages: messages,
		CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
	}
}

type startAssistantRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type assistantReplyRequest struct {
	Message string `json:"message"`
}

// startAssistantHandler opens the work order's clarification. Without an
// explicit choice the assistant runs on the work order's lead provider and
// model.
func (api *API) startAssistantHandler(w http.ResponseWriter, r *http.Request) {
	projectID, featureID := r.PathValue("projectID"), r.PathValue("id")
	storedFeature, err := api.features.GetByID(r.Context(), projectID, featureID)
	if err != nil {
		writeAssistantError(w, featureID, err)
		return
	}
	request := startAssistantRequest{}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
			return
		}
	}
	provider := project.AgentProvider(strings.TrimSpace(request.Provider))
	model := strings.TrimSpace(request.Model)
	if provider == "" {
		provider = storedFeature.AgentProviders.Lead
		if model == "" {
			model = storedFeature.AgentModels.Lead
		}
	}
	session, created, err := api.assistant.Start(r.Context(), projectID, featureID, provider, model)
	if err != nil {
		writeAssistantError(w, featureID, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, newAssistantSessionResponse(session), "assistant session")
}

func (api *API) getAssistantHandler(w http.ResponseWriter, r *http.Request) {
	session, err := api.assistant.Get(r.Context(), r.PathValue("projectID"), r.PathValue("id"))
	if err != nil {
		writeAssistantError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, newAssistantSessionResponse(session), "assistant session")
}

func (api *API) replyAssistantHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	request := assistantReplyRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	session, created, err := api.assistant.Reply(r.Context(), r.PathValue("projectID"), r.PathValue("id"), request.Message, key)
	if err != nil {
		writeAssistantError(w, r.PathValue("id"), err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, newAssistantSessionResponse(session), "assistant session")
}

func (api *API) acceptBriefHandler(w http.ResponseWriter, r *http.Request) {
	api.transitionWorkOrder(w, r, api.assistant.AcceptBrief)
}

func (api *API) reopenWorkOrderHandler(w http.ResponseWriter, r *http.Request) {
	api.transitionWorkOrder(w, r, api.assistant.Reopen)
}

func (api *API) transitionWorkOrder(
	w http.ResponseWriter,
	r *http.Request,
	transition func(context.Context, string, string, string) (feature.Feature, error),
) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	updated, err := transition(r.Context(), r.PathValue("projectID"), r.PathValue("id"), key)
	if err != nil {
		writeAssistantError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, newFeatureResponse(updated), "work order")
}

func writeAssistantError(w http.ResponseWriter, featureID string, err error) {
	switch {
	case errors.Is(err, feature.ErrNotFound):
		writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
	case errors.Is(err, assistant.ErrNotFound):
		writeError(w, http.StatusNotFound, "assistant_not_found", "this work order has no assistant conversation yet")
	case errors.Is(err, assistant.ErrNotDraft):
		writeError(w, http.StatusConflict, "feature_not_draft", "only a draft work order can be clarified or accepted")
	case errors.Is(err, assistant.ErrNotReady):
		writeError(w, http.StatusConflict, "assistant_not_ready", "the assistant is busy, or there is no brief to accept yet")
	case errors.Is(err, assistant.ErrConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "the idempotency key was used for a different message")
	case errors.Is(err, assistant.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_assistant_request", err.Error())
	case errors.Is(err, workspace.ErrProjectRepositoryNotBound):
		writeError(w, http.StatusConflict, "forgejo_repository_not_bound", "the project has no repository yet")
	default:
		log.Printf("work-order assistant for %q: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
