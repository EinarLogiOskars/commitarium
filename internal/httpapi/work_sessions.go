package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// Work sessions (ADR-019) are work orders of kind session. Their routes live
// under the work order, apart from /api/v1/sessions, which are agent
// conversations.

type sessionMessenger interface {
	SendSessionMessage(ctx context.Context, runID string, command worker.Command) (execution.Run, error)
}

type featureRenamer interface {
	Rename(ctx context.Context, projectID string, featureID string, title string) (feature.Feature, error)
}

type sessionMessageRequest struct {
	Message string `json:"message"`
}

// sendSessionMessageHandler starts the session's next agent turn.
func (api *API) sendSessionMessageHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	var body sessionMessageRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	storedFeature, err := api.features.GetByID(r.Context(), r.PathValue("projectID"), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
			return
		}
		log.Printf("load session %q: %v", r.PathValue("id"), err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if storedFeature.Kind != feature.KindSession {
		writeError(w, http.StatusConflict, "not_a_session", "only a session takes messages")
		return
	}
	messenger, ok := api.realWorkflow.(sessionMessenger)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "sessions_unavailable", "sessions need real agents")
		return
	}
	runs, err := api.execution.RunsForFeature(r.Context(), storedFeature.ID)
	if err != nil {
		log.Printf("list runs of session %q: %v", storedFeature.ID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	if len(runs) == 0 {
		writeError(w, http.StatusConflict, "session_not_started", "the session has not started")
		return
	}
	run, err := messenger.SendSessionMessage(r.Context(), runs[0].ID, worker.Command{
		ID: idempotencyKey, Type: worker.CommandMessage, Message: body.Message,
	})
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrInvalidCommand):
			writeError(w, http.StatusBadRequest, "invalid_message", "message must not be empty")
		case errors.Is(err, orchestration.ErrSessionBusy):
			writeError(w, http.StatusConflict, "session_busy", "the agent is still working on the last message")
		case errors.Is(err, execution.ErrCommandConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different message")
		case errors.Is(err, orchestration.ErrCommandNotAllowed), errors.Is(err, orchestration.ErrNotASession):
			writeError(w, http.StatusConflict, "session_not_waiting", "the session is not waiting for a message")
		default:
			log.Printf("send message to session %q: %v", storedFeature.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	api.writeRun(w, r, http.StatusAccepted, run)
}

type renameFeatureRequest struct {
	Title string `json:"title"`
}

// renameFeatureHandler renames a work order or session.
func (api *API) renameFeatureHandler(w http.ResponseWriter, r *http.Request) {
	var body renameFeatureRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	renamer, ok := api.features.(featureRenamer)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "rename_unavailable", "work orders cannot be renamed")
		return
	}
	renamed, err := renamer.Rename(r.Context(), r.PathValue("projectID"), r.PathValue("id"), body.Title)
	if err != nil {
		switch {
		case errors.Is(err, feature.ErrTitleRequired):
			writeError(w, http.StatusBadRequest, "feature_title_required", "feature title is required")
		case errors.Is(err, feature.ErrNotFound):
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
		default:
			log.Printf("rename feature %q: %v", r.PathValue("id"), err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, newFeatureResponse(renamed), "renamed feature")
}
