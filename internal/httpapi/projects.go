package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

const maxProjectImportBytes = 512 * 1024 * 1024

type createProjectRequest struct {
	Name           string                 `json:"name"`
	RecoveryPolicy project.RecoveryPolicy `json:"recovery_policy"`
	MergePolicy    project.MergePolicy    `json:"merge_policy"`
	DialogueLimits *dialogueLimitsRequest `json:"dialogue_limits"`
	AgentProviders *agentProvidersRequest `json:"agent_providers"`
}

type projectResponse struct {
	ID                string                     `json:"id"`
	Name              string                     `json:"name"`
	RecoveryPolicy    project.RecoveryPolicy     `json:"recovery_policy"`
	MergePolicy       project.MergePolicy        `json:"merge_policy"`
	DialogueLimits    dialogueLimitsResponse     `json:"dialogue_limits"`
	AgentProviders    agentProvidersResponse     `json:"agent_providers"`
	ForgejoRepository *forgejoRepositoryResponse `json:"forgejo_repository,omitempty"`
	CreatedAt         time.Time                  `json:"created_at"`
}

type dialogueLimitsRequest struct {
	PlanningRounds             *int `json:"planning_rounds"`
	ImplementationReviewRounds *int `json:"implementation_review_rounds"`
}

type dialogueLimitsResponse struct {
	PlanningRounds             int `json:"planning_rounds"`
	ImplementationReviewRounds int `json:"implementation_review_rounds"`
}

type agentProvidersRequest struct {
	Lead     *project.AgentProvider `json:"lead"`
	Reviewer *project.AgentProvider `json:"reviewer"`
}

type agentProvidersResponse struct {
	Lead     project.AgentProvider `json:"lead"`
	Reviewer project.AgentProvider `json:"reviewer"`
}

type forgejoRepositoryResponse struct {
	Owner         string    `json:"owner"`
	Name          string    `json:"name"`
	DefaultBranch string    `json:"default_branch"`
	BoundAt       time.Time `json:"bound_at"`
}

type bindForgejoRepositoryRequest struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

type importProjectMetadata struct {
	Name           string                 `json:"name"`
	RecoveryPolicy project.RecoveryPolicy `json:"recovery_policy"`
	MergePolicy    project.MergePolicy    `json:"merge_policy"`
	DialogueLimits *dialogueLimitsRequest `json:"dialogue_limits"`
	AgentProviders *agentProvidersRequest `json:"agent_providers"`
	DefaultBranch  string                 `json:"default_branch"`
}

func (api *API) importProjectHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxProjectImportBytes)
	if err := r.ParseMultipartForm(1024 * 1024); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "project_import_too_large", "project import exceeds the 512 MiB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_project_import", "request must be multipart/form-data")
		return
	}
	defer r.MultipartForm.RemoveAll()
	if len(r.MultipartForm.Value) != 1 || len(r.MultipartForm.Value["metadata"]) != 1 ||
		len(r.MultipartForm.File) != 1 || len(r.MultipartForm.File["bundle"]) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_project_import", "request must contain exactly one metadata field and one bundle file")
		return
	}
	metadata := importProjectMetadata{}
	decoder := json.NewDecoder(bytes.NewBufferString(r.MultipartForm.Value["metadata"][0]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_project_import_metadata", "metadata must contain exactly one valid JSON object with no unknown fields")
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_project_import_metadata", "metadata must contain exactly one valid JSON object with no unknown fields")
		return
	}
	limits, err := decodeDialogueLimits(metadata.DialogueLimits, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_dialogue_limits", "dialogue_limits must include non-negative planning_rounds and implementation_review_rounds; zero means unlimited")
		return
	}
	agentProviders, err := decodeAgentProviders(metadata.AgentProviders, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_agent_providers", "agent_providers must include lead and reviewer set to codex or claude")
		return
	}
	bundle, err := r.MultipartForm.File["bundle"][0].Open()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_git_bundle", "Git bundle cannot be read")
		return
	}
	defer bundle.Close()
	imported, created, err := api.projectImporter.Import(r.Context(), project.ImportSpec{
		ImportID: r.PathValue("importID"), Name: metadata.Name,
		RecoveryPolicy: metadata.RecoveryPolicy, MergePolicy: metadata.MergePolicy,
		DialogueLimits: limits,
		AgentProviders: agentProviders,
		DefaultBranch:  metadata.DefaultBranch,
	}, bundle)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrInvalidImportID):
			writeError(w, http.StatusBadRequest, "invalid_project_import_id", "project import ID must be a safe non-empty identifier")
		case errors.Is(err, project.ErrNameRequired):
			writeError(w, http.StatusBadRequest, "project_name_required", "project name is required")
		case errors.Is(err, project.ErrInvalidRecoveryPolicy):
			writeError(w, http.StatusBadRequest, "invalid_recovery_policy", "recovery_policy must be approval_required or automatic")
		case errors.Is(err, project.ErrInvalidMergePolicy):
			writeError(w, http.StatusBadRequest, "invalid_merge_policy", "merge_policy must be require_user_approval or auto_after_gates")
		case errors.Is(err, project.ErrInvalidDialogueLimits):
			writeError(w, http.StatusBadRequest, "invalid_dialogue_limits", "dialogue limits must be non-negative; zero means unlimited")
		case errors.Is(err, project.ErrInvalidAgentProviders):
			writeError(w, http.StatusBadRequest, "invalid_agent_providers", "lead and reviewer providers must be codex or claude")
		case errors.Is(err, project.ErrInvalidDefaultBranch):
			writeError(w, http.StatusBadRequest, "invalid_default_branch", "default_branch must be a valid Git branch name")
		case errors.Is(err, project.ErrInvalidGitBundle):
			writeError(w, http.StatusBadRequest, "invalid_git_bundle", "bundle must be a valid Git bundle containing the requested default branch")
		case errors.Is(err, project.ErrImportConflict),
			errors.Is(err, project.ErrForgejoRepositoryNotFound),
			errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusConflict, "project_import_conflict", "project import ID or internal repository conflicts with different existing state")
		case errors.Is(err, project.ErrImportUnavailable), errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "project_import_unavailable", "project import is temporarily unavailable")
		default:
			log.Printf("import project %q: %v", r.PathValue("importID"), err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Location", "/api/v1/projects/"+imported.ID)
	w.Header().Set("Content-Type", "application/json")
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	if err := json.NewEncoder(w).Encode(newProjectResponse(imported)); err != nil {
		log.Printf("encode imported project response: %v", err)
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}

func (api *API) createProjectHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	request := createProjectRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_json",
			"request body must contain valid JSON",
		)
		return
	}
	dialogueLimits, err := decodeDialogueLimits(request.DialogueLimits, true)
	if err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_dialogue_limits",
			"dialogue_limits must include non-negative planning_rounds and implementation_review_rounds; zero means unlimited",
		)
		return
	}
	agentProviders, err := decodeAgentProviders(request.AgentProviders, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_agent_providers", "agent_providers must include lead and reviewer set to codex or claude")
		return
	}

	createdProject, err := api.projects.Create(
		r.Context(),
		request.Name,
		request.RecoveryPolicy,
		dialogueLimits,
		agentProviders,
		request.MergePolicy,
	)
	if err != nil {
		if errors.Is(err, project.ErrNameRequired) {
			writeError(
				w,
				http.StatusBadRequest,
				"project_name_required",
				"project name is required",
			)
			return
		}
		if errors.Is(err, project.ErrInvalidRecoveryPolicy) {
			writeError(
				w,
				http.StatusBadRequest,
				"invalid_recovery_policy",
				"recovery_policy must be approval_required or automatic",
			)
			return
		}
		if errors.Is(err, project.ErrInvalidMergePolicy) {
			writeError(w, http.StatusBadRequest, "invalid_merge_policy", "merge_policy must be require_user_approval or auto_after_gates")
			return
		}
		if errors.Is(err, project.ErrInvalidDialogueLimits) {
			writeError(
				w,
				http.StatusBadRequest,
				"invalid_dialogue_limits",
				"dialogue limits must be non-negative; zero means unlimited",
			)
			return
		}
		if errors.Is(err, project.ErrInvalidAgentProviders) {
			writeError(w, http.StatusBadRequest, "invalid_agent_providers", "lead and reviewer providers must be codex or claude")
			return
		}
		log.Printf("create project: %v", err)
		writeError(
			w,
			http.StatusInternalServerError,
			"internal_error",
			"internal server error",
		)
		return
	}

	w.Header().Set(
		"Location",
		"/api/v1/projects/"+createdProject.ID,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	if err := json.NewEncoder(w).Encode(newProjectResponse(createdProject)); err != nil {
		log.Printf("encode project response: %v", err)
	}
}

type mergePolicyRequest struct {
	MergePolicy project.MergePolicy `json:"merge_policy"`
}

func (api *API) updateProjectMergePolicyHandler(w http.ResponseWriter, r *http.Request) {
	request := mergePolicyRequest{}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one valid JSON object with no unknown fields")
		return
	}
	updated, err := api.projects.UpdateMergePolicy(r.Context(), r.PathValue("id"), request.MergePolicy)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrNotFound):
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
		case errors.Is(err, project.ErrInvalidMergePolicy):
			writeError(w, http.StatusBadRequest, "invalid_merge_policy", "merge_policy must be require_user_approval or auto_after_gates")
		default:
			log.Printf("update project merge policy %q: %v", r.PathValue("id"), err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, newProjectResponse(updated), "project")
}

func (api *API) updateProjectAgentProvidersHandler(w http.ResponseWriter, r *http.Request) {
	request := agentProvidersRequest{}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || ensureJSONEOF(decoder) != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain exactly one valid JSON object with no unknown fields")
		return
	}
	providers, err := decodeAgentProviders(&request, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_agent_providers", "request must include lead and reviewer set to codex or claude")
		return
	}
	updated, err := api.projects.UpdateAgentProviders(r.Context(), r.PathValue("id"), providers)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrNotFound):
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
		case errors.Is(err, project.ErrInvalidAgentProviders):
			writeError(w, http.StatusBadRequest, "invalid_agent_providers", "lead and reviewer providers must be codex or claude")
		default:
			log.Printf("update project agent providers %q: %v", r.PathValue("id"), err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(newProjectResponse(updated)); err != nil {
		log.Printf("encode project response: %v", err)
	}
}

func decodeAgentProviders(
	request *agentProvidersRequest,
	useDefaultsWhenOmitted bool,
) (project.AgentProviders, error) {
	if request == nil && useDefaultsWhenOmitted {
		return project.DefaultAgentProviders(), nil
	}
	if request == nil || request.Lead == nil || request.Reviewer == nil {
		return project.AgentProviders{}, project.ErrInvalidAgentProviders
	}
	return project.AgentProviders{Lead: *request.Lead, Reviewer: *request.Reviewer}.Normalize()
}

func (api *API) updateProjectDialogueLimitsHandler(w http.ResponseWriter, r *http.Request) {
	request := dialogueLimitsRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	limits, err := decodeDialogueLimits(&request, false)
	if err != nil {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_dialogue_limits",
			"request must include non-negative planning_rounds and implementation_review_rounds; zero means unlimited",
		)
		return
	}
	updated, err := api.projects.UpdateDialogueLimits(r.Context(), r.PathValue("id"), limits)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrNotFound):
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
		case errors.Is(err, project.ErrInvalidDialogueLimits):
			writeError(w, http.StatusBadRequest, "invalid_dialogue_limits", "dialogue limits must be non-negative; zero means unlimited")
		default:
			log.Printf("update project dialogue limits %q: %v", r.PathValue("id"), err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(newProjectResponse(updated)); err != nil {
		log.Printf("encode project response: %v", err)
	}
}

func decodeDialogueLimits(
	request *dialogueLimitsRequest,
	useDefaultsWhenOmitted bool,
) (project.DialogueLimits, error) {
	if request == nil && useDefaultsWhenOmitted {
		return project.DefaultDialogueLimits(), nil
	}
	if request == nil || request.PlanningRounds == nil || request.ImplementationReviewRounds == nil {
		return project.DialogueLimits{}, project.ErrInvalidDialogueLimits
	}
	limits := project.DialogueLimits{
		PlanningRounds:             *request.PlanningRounds,
		ImplementationReviewRounds: *request.ImplementationReviewRounds,
	}
	return limits, limits.Validate()
}

func (api *API) listProjectsHandler(w http.ResponseWriter, r *http.Request) {
	projects, err := api.projects.List(r.Context())
	if err != nil {
		log.Printf("list projects: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	response := make([]projectResponse, 0, len(projects))
	for _, storedProject := range projects {
		response = append(response, newProjectResponse(storedProject))
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("encode project list response: %v", err)
	}
}

func (api *API) getProjectByIDHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	id := r.PathValue("id")

	foundProject, err := api.projects.GetByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(
				w,
				http.StatusNotFound,
				"project_not_found",
				"project not found",
			)
			return
		}
		log.Printf("get project %q: %v", id, err)

		writeError(
			w,
			http.StatusInternalServerError,
			"internal_error",
			"internal server error",
		)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(newProjectResponse(foundProject)); err != nil {
		log.Printf("encode project response: %v", err)
	}
}

func (api *API) bindForgejoRepositoryHandler(w http.ResponseWriter, r *http.Request) {
	request := bindForgejoRepositoryRequest{}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain valid JSON")
		return
	}
	projectID := r.PathValue("id")
	boundProject, err := api.projects.BindForgejoRepository(
		r.Context(), projectID, request.Owner, request.Name,
	)
	if err != nil {
		switch {
		case errors.Is(err, project.ErrNotFound):
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
		case errors.Is(err, project.ErrForgejoOwnerRequired):
			writeError(w, http.StatusBadRequest, "forgejo_owner_required", "Forgejo repository owner is required")
		case errors.Is(err, project.ErrForgejoRepositoryNameRequired):
			writeError(w, http.StatusBadRequest, "forgejo_repository_name_required", "Forgejo repository name is required")
		case errors.Is(err, project.ErrInvalidForgejoRepositoryCoordinate):
			writeError(w, http.StatusBadRequest, "invalid_forgejo_repository", "Forgejo repository owner and name must not contain slashes")
		case errors.Is(err, project.ErrForgejoRepositoryNotFound):
			writeError(w, http.StatusNotFound, "forgejo_repository_not_found", "Forgejo repository not found")
		case errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusConflict, "forgejo_repository_not_ready", "Forgejo repository must be non-empty, unarchived, and have a default branch")
		case errors.Is(err, project.ErrForgejoRepositoryAlreadyBound):
			writeError(w, http.StatusConflict, "forgejo_repository_already_bound", "project already has a different Forgejo repository")
		case errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "forgejo_unavailable", "Forgejo repository verification is unavailable")
		default:
			log.Printf("bind Forgejo repository to project %q: %v", projectID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(newProjectResponse(boundProject)); err != nil {
		log.Printf("encode bound project response: %v", err)
	}
}

func newProjectResponse(storedProject project.Project) projectResponse {
	agentProviders, err := storedProject.AgentProviders.Normalize()
	if err != nil {
		agentProviders = storedProject.AgentProviders
	}
	response := projectResponse{
		ID: storedProject.ID, Name: storedProject.Name,
		RecoveryPolicy: storedProject.RecoveryPolicy,
		MergePolicy:    storedProject.MergePolicy,
		DialogueLimits: dialogueLimitsResponse{
			PlanningRounds:             storedProject.DialogueLimits.PlanningRounds,
			ImplementationReviewRounds: storedProject.DialogueLimits.ImplementationReviewRounds,
		},
		AgentProviders: agentProvidersResponse{
			Lead: agentProviders.Lead, Reviewer: agentProviders.Reviewer,
		},
		CreatedAt: storedProject.CreatedAt,
	}
	if storedProject.ForgejoRepository != nil {
		response.ForgejoRepository = &forgejoRepositoryResponse{
			Owner:         storedProject.ForgejoRepository.Owner,
			Name:          storedProject.ForgejoRepository.Name,
			DefaultBranch: storedProject.ForgejoRepository.DefaultBranch,
			BoundAt:       storedProject.ForgejoRepository.BoundAt,
		}
	}
	return response
}
