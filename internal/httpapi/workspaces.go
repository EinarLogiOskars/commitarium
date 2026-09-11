package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type workspaceResponse struct {
	ID              string               `json:"id"`
	ProjectID       string               `json:"project_id"`
	FeatureID       string               `json:"feature_id"`
	Repository      repositoryRef        `json:"repository"`
	BaseBranch      string               `json:"base_branch"`
	Branch          string               `json:"branch"`
	BaseCommitID    string               `json:"base_commit_id"`
	Status          workspace.Status     `json:"status"`
	BranchCreatedAt *time.Time           `json:"branch_created_at,omitempty"`
	Checkout        *checkoutResponse    `json:"checkout,omitempty"`
	PullRequest     *pullRequestResponse `json:"pull_request,omitempty"`
	Merge           *mergeResponse       `json:"merge,omitempty"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type checkoutResponse struct {
	WorkspaceID  string    `json:"workspace_id"`
	RelativePath string    `json:"relative_path"`
	CreatedAt    time.Time `json:"created_at"`
}

type pullRequestResponse struct {
	Number     int64     `json:"number"`
	URL        string    `json:"url"`
	Draft      bool      `json:"draft"`
	RecordedAt time.Time `json:"recorded_at"`
}

type mergeResponse struct {
	ApprovedCommitID string     `json:"approved_commit_id"`
	ReadyAt          time.Time  `json:"ready_at"`
	MergeCommitID    string     `json:"merge_commit_id,omitempty"`
	MergedAt         *time.Time `json:"merged_at,omitempty"`
}

type repositoryRef struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func (api *API) getWorkspaceHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	featureID := r.PathValue("id")
	stored, err := api.workspaces.Get(r.Context(), projectID, featureID)
	if err != nil {
		switch {
		case errors.Is(err, feature.ErrNotFound):
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
		case errors.Is(err, workspace.ErrNotFound):
			writeError(w, http.StatusNotFound, "workspace_not_found", "feature workspace not found")
		default:
			log.Printf("get workspace for feature %q: %v", featureID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, stored)
}

func (api *API) prepareWorkspaceHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	featureID := r.PathValue("id")
	prepared, created, err := api.workspaces.Prepare(r.Context(), projectID, featureID)
	if err != nil {
		switch {
		case errors.Is(err, feature.ErrNotFound), errors.Is(err, project.ErrNotFound):
			writeError(w, http.StatusNotFound, "feature_not_found", "feature or project not found")
		case errors.Is(err, workspace.ErrGoalNotAccepted):
			writeError(w, http.StatusConflict, "goal_not_accepted", "feature goal must be accepted before preparing its workspace")
		case errors.Is(err, workspace.ErrFeatureNotDraft):
			writeError(w, http.StatusConflict, "workspace_preparation_not_allowed", "feature workspace can only be prepared while the feature is a draft")
		case errors.Is(err, workspace.ErrProjectRepositoryNotBound):
			writeError(w, http.StatusConflict, "forgejo_repository_not_bound", "project must be bound to a Forgejo repository before preparing its workspace")
		case errors.Is(err, workspace.ErrBranchNotFound), errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusConflict, "forgejo_repository_not_ready", "the bound Forgejo repository or its default branch is not ready")
		case errors.Is(err, workspace.ErrConflict), errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict),
			errors.Is(err, workspace.ErrPullRequestConflict):
			writeError(w, http.StatusConflict, "workspace_conflict", "the stored workspace, Forgejo branch, managed checkout, or draft pull request disagrees; user review is required")
		case errors.Is(err, workspace.ErrCheckoutUnavailable):
			writeError(w, http.StatusServiceUnavailable, "checkout_unavailable", "the managed checkout cannot be prepared right now")
		case errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "forgejo_unavailable", "Forgejo workspace preparation is unavailable")
		default:
			log.Printf("prepare workspace for feature %q: %v", featureID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", r.URL.Path)
	}
	writeWorkspaceJSON(w, status, prepared)
}

func writeWorkspaceJSON(w http.ResponseWriter, status int, stored workspace.Workspace) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(newWorkspaceResponse(stored)); err != nil {
		log.Printf("encode workspace response: %v", err)
	}
}

func newWorkspaceResponse(stored workspace.Workspace) workspaceResponse {
	response := workspaceResponse{
		ID: stored.ID, ProjectID: stored.ProjectID, FeatureID: stored.FeatureID,
		Repository: repositoryRef{Owner: stored.RepositoryOwner, Name: stored.RepositoryName},
		BaseBranch: stored.BaseBranch, Branch: stored.Branch,
		BaseCommitID: stored.BaseCommitID, Status: stored.Status,
		BranchCreatedAt: stored.BranchCreatedAt,
		CreatedAt:       stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
	}
	if stored.CheckoutReady() {
		response.Checkout = &checkoutResponse{
			WorkspaceID: stored.ID, RelativePath: stored.CheckoutRelativePath,
			CreatedAt: *stored.CheckoutCreatedAt,
		}
	}
	if stored.PullRequestReady() {
		response.PullRequest = &pullRequestResponse{
			Number: stored.PullRequestNumber, URL: stored.PullRequestURL,
			Draft: stored.MergeCommitID == "", RecordedAt: *stored.PullRequestRecordedAt,
		}
	}
	if stored.MergeReadyAt != nil {
		response.Merge = &mergeResponse{
			ApprovedCommitID: stored.ApprovedCommitID, ReadyAt: *stored.MergeReadyAt,
			MergeCommitID: stored.MergeCommitID, MergedAt: stored.MergedAt,
		}
	}
	return response
}
