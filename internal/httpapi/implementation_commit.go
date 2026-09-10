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
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type implementationCommitRequest struct {
	Message string `json:"message"`
}

type implementationPublicationResponse struct {
	ID                   string                      `json:"id"`
	RunID                string                      `json:"run_id"`
	WorkspaceID          string                      `json:"workspace_id"`
	CommitMessage        string                      `json:"commit_message"`
	RemoteCommitIDBefore string                      `json:"remote_commit_id_before"`
	LocalCommitIDBefore  string                      `json:"local_commit_id_before"`
	CommitID             string                      `json:"commit_id"`
	Status               workspace.PublicationStatus `json:"status"`
	CreatedAt            time.Time                   `json:"created_at"`
	CompletedAt          *time.Time                  `json:"completed_at,omitempty"`
}

func (api *API) publishImplementationHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	request := implementationCommitRequest{}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	request.Message = strings.TrimSpace(request.Message)
	if request.Message == "" || strings.ContainsAny(request.Message, "\r\n") ||
		len(request.Message) > workspace.MaxCommitMessageBytes {
		writeError(w, http.StatusBadRequest, "invalid_commit_message", "message must be one non-empty line of at most 200 bytes")
		return
	}
	publication, created, err := api.realWorkflow.PublishImplementation(
		r.Context(), r.PathValue("id"), key, request.Message,
	)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrNotFound):
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
		case errors.Is(err, orchestration.ErrImplementationPublicationNotAllowed),
			errors.Is(err, feature.ErrInvalidTransition),
			errors.Is(err, workspace.ErrPublicationConflict),
			errors.Is(err, workspace.ErrConflict),
			errors.Is(err, workspace.ErrBranchNotFound),
			errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict),
			errors.Is(err, workspace.ErrPullRequestConflict),
			errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusConflict, "implementation_commit_not_ready", "the implementation is not at a safe user-approved commit checkpoint, or its workspace state conflicts")
		case errors.Is(err, workspace.ErrNoChanges):
			writeError(w, http.StatusConflict, "implementation_has_no_changes", "the managed workspace has no changes to commit")
		case errors.Is(err, workspace.ErrCheckoutUnavailable), errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "implementation_commit_unavailable", "the managed checkout or Forgejo is unavailable")
		default:
			log.Printf("publish implementation for run %q: %v", r.PathValue("id"), err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", r.URL.Path)
	}
	writeJSON(w, status, implementationPublicationResponse{
		ID: publication.ID, RunID: publication.RunID, WorkspaceID: publication.WorkspaceID,
		CommitMessage:        publication.CommitMessage,
		RemoteCommitIDBefore: publication.RemoteCommitIDBefore,
		LocalCommitIDBefore:  publication.LocalCommitIDBefore,
		CommitID:             publication.CommitID, Status: publication.Status,
		CreatedAt: publication.CreatedAt, CompletedAt: publication.CompletedAt,
	}, "implementation publication")
}
