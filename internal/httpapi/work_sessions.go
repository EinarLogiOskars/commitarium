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

type sessionFinisher interface {
	FinishSession(ctx context.Context, runID string, idempotencyKey string) (execution.Run, error)
}

type featureRenamer interface {
	Rename(ctx context.Context, projectID string, featureID string, title string) (feature.Feature, error)
}

type sessionMessageRequest struct {
	Message string `json:"message"`
	// Attachments name the project's documents attached to it.
	Attachments []string `json:"attachments,omitempty"`
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
	runID, ok := api.sessionRun(w, r)
	if !ok {
		return
	}
	// The agent reads the files in its checkout, named in the message.
	message, ok := api.attachedMessage(w, r, r.PathValue("projectID"), body.Message, body.Attachments)
	if !ok {
		return
	}
	messenger, ok := api.realWorkflow.(sessionMessenger)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "sessions_unavailable", "sessions need real agents")
		return
	}
	run, err := messenger.SendSessionMessage(r.Context(), runID, worker.Command{
		ID: idempotencyKey, Type: worker.CommandMessage, Message: message,
	})
	if err != nil {
		writeSessionError(w, r.PathValue("id"), "send a message to", err)
		return
	}
	api.writeRun(w, r, http.StatusAccepted, run)
}

// finishSessionHandler asks the session's agent to commit and publish the
// work for the merge gate.
func (api *API) finishSessionHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	runID, ok := api.sessionRun(w, r)
	if !ok {
		return
	}
	finisher, ok := api.realWorkflow.(sessionFinisher)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "sessions_unavailable", "sessions need real agents")
		return
	}
	run, err := finisher.FinishSession(r.Context(), runID, idempotencyKey)
	if err != nil {
		writeSessionError(w, r.PathValue("id"), "finish", err)
		return
	}
	api.writeRun(w, r, http.StatusAccepted, run)
}

// sessionRun finds a session's run. It writes the error response itself and
// reports whether to continue.
func (api *API) sessionRun(w http.ResponseWriter, r *http.Request) (string, bool) {
	storedFeature, err := api.features.GetByID(r.Context(), r.PathValue("projectID"), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
			return "", false
		}
		log.Printf("load session %q: %v", r.PathValue("id"), err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return "", false
	}
	if storedFeature.Kind != feature.KindSession {
		writeError(w, http.StatusConflict, "not_a_session", "only a session takes messages")
		return "", false
	}
	runs, err := api.execution.RunsForFeature(r.Context(), storedFeature.ID)
	if err != nil {
		log.Printf("list runs of session %q: %v", storedFeature.ID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return "", false
	}
	if len(runs) == 0 {
		writeError(w, http.StatusConflict, "session_not_started", "the session has not started")
		return "", false
	}
	return runs[0].ID, true
}

func writeSessionError(w http.ResponseWriter, featureID string, action string, err error) {
	switch {
	case errors.Is(err, execution.ErrInvalidCommand):
		writeError(w, http.StatusBadRequest, "invalid_message", "message must not be empty")
	case errors.Is(err, orchestration.ErrSessionBusy):
		writeError(w, http.StatusConflict, "session_busy", "the agent is still working on the last message")
	case errors.Is(err, execution.ErrCommandConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different message")
	case errors.Is(err, orchestration.ErrCommandNotAllowed), errors.Is(err, orchestration.ErrNotASession):
		writeError(w, http.StatusConflict, "session_not_waiting", "the session is not waiting for this")
	default:
		log.Printf("%s session %q: %v", action, featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
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
