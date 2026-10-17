package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type runResponse struct {
	ID                         string                       `json:"id"`
	FeatureID                  string                       `json:"feature_id"`
	Status                     execution.RunStatus          `json:"status"`
	Reason                     string                       `json:"reason,omitempty"`
	WaitKind                   execution.RunWaitKind        `json:"wait_kind,omitempty"`
	Paused                     bool                         `json:"paused"`
	DialogueLimits             dialogueLimitsResponse       `json:"dialogue_limits"`
	AgentProviders             agentProvidersResponse       `json:"agent_providers"`
	AgentModels                agentModelsResponse          `json:"agent_models"`
	MergePolicy                project.MergePolicy          `json:"merge_policy"`
	AutonomyPolicy             project.AutonomyPolicy       `json:"autonomy_policy"`
	IndependentAcceptanceTests bool                         `json:"independent_acceptance_tests"`
	PlanVersion                int                          `json:"plan_version"`
	StartedAt                  time.Time                    `json:"started_at"`
	UpdatedAt                  time.Time                    `json:"updated_at"`
	EndedAt                    *time.Time                   `json:"ended_at,omitempty"`
	Sessions                   []sessionResponse            `json:"sessions"`
	InterventionTargets        []interventionTargetResponse `json:"intervention_targets"`
	Intervention               *interventionResponse        `json:"intervention,omitempty"`
	Recovery                   *recoveryResponse            `json:"recovery,omitempty"`
	LoginRequired              *loginRequiredResponse       `json:"login_required,omitempty"`
	UsageLimit                 *usageLimitResponse          `json:"usage_limit,omitempty"`
	Retrying                   *retryingResponse            `json:"retrying,omitempty"`
	Steps                      workStepsResponse            `json:"steps"`
}

// loginRequiredResponse names the agent a paused run waits for to log in
// again (ADR-017).
type loginRequiredResponse struct {
	Agent string      `json:"agent"`
	Role  worker.Role `json:"role"`
}

type runLoginWaits interface {
	LoginWaitForRun(context.Context, string) (execution.LoginWait, error)
}

// usageLimitResponse names the agent whose usage limit a paused run waits
// on, and when it resets, when known.
type usageLimitResponse struct {
	Agent    string      `json:"agent"`
	Role     worker.Role `json:"role"`
	ResetsAt *time.Time  `json:"resets_at,omitempty"`
}

// retryingResponse is the automatic retry an interrupted turn waits for:
// which one, of how many, why, and when it is due.
type retryingResponse struct {
	Cause string    `json:"cause"`
	Retry int       `json:"retry"`
	Of    int       `json:"of"`
	DueAt time.Time `json:"due_at"`
}

type runRetryWaits interface {
	RetryWaitForRun(context.Context, string) (execution.RetryWait, error)
}

// retryingFor returns the automatic retry a blocked run waits for, if any.
func retryingFor(ctx context.Context, executions any, run execution.Run) (*retryingResponse, error) {
	waits, ok := executions.(runRetryWaits)
	if !ok || run.Status != execution.RunStatusWaitingForUser || run.Paused ||
		run.WaitKind != execution.RunWaitKindBlocker {
		return nil, nil
	}
	wait, err := waits.RetryWaitForRun(ctx, run.ID)
	if err != nil || wait.DueAt == nil {
		return nil, err
	}
	return &retryingResponse{
		Cause: wait.Cause, Retry: wait.Retries + 1, Of: orchestration.AutomaticRetries, DueAt: *wait.DueAt,
	}, nil
}

type runLimitWaits interface {
	LimitWaitForRun(context.Context, string) (execution.LimitWait, error)
	LimitWaits(context.Context) ([]execution.LimitWait, error)
}

type recoveryResponse struct {
	Action           orchestration.RecoveryAction   `json:"action"`
	Role             worker.Role                    `json:"role,omitempty"`
	Provider         project.AgentProvider          `json:"provider,omitempty"`
	Classification   string                         `json:"classification"`
	Evidence         []string                       `json:"evidence"`
	MissingResult    string                         `json:"missing_result,omitempty"`
	ProcessStatus    string                         `json:"process_status"`
	Successor        string                         `json:"successor"`
	AvailableActions []orchestration.RecoveryAction `json:"available_actions"`
}

func newRecoveryResponse(
	action orchestration.RecoveryAction,
	run execution.Run,
	sessions []sessionResponse,
	providers agentProvidersResponse,
) *recoveryResponse {
	classification := "incomplete_result"
	processStatus := "confirmed"
	successor := "resume_then_fresh"
	missingResult := "structured_result"
	if action == orchestration.RecoveryActionRecheck {
		classification, successor = "rechecking_durable_effects", "none"
		missingResult = ""
	}
	if action == orchestration.RecoveryActionReplace {
		successor = "fresh_conversation"
	}
	if run.Status == execution.RunStatusRunning {
		processStatus = "successor_running"
	} else {
		successor = "none"
		missingResult = ""
		classification = "confirmed_from_effects"
	}
	if action == orchestration.RecoveryActionLeavePaused {
		classification, processStatus, successor = "left_paused", "paused", "none"
		missingResult = ""
	} else if run.Status == execution.RunStatusWaitingForUser && run.WaitKind == execution.RunWaitKindBlocker {
		classification, processStatus, successor = "unresolved", "paused", "none"
		if action != orchestration.RecoveryActionRecheck {
			missingResult = "structured_result"
		}
	} else if run.Status == execution.RunStatusWaitingForUser {
		processStatus = "paused"
	}
	role, provider := recoveryRoleAndProvider(sessions, providers)
	evidence := make([]string, 0, 1)
	if run.Reason != "" {
		evidence = append(evidence, run.Reason)
	}
	return &recoveryResponse{
		Action: action, Role: role, Provider: provider,
		Classification: classification, Evidence: evidence, MissingResult: missingResult,
		ProcessStatus: processStatus, Successor: successor,
		AvailableActions: []orchestration.RecoveryAction{
			orchestration.RecoveryActionRecheck, orchestration.RecoveryActionContinue,
			orchestration.RecoveryActionReplace, orchestration.RecoveryActionLeavePaused,
		},
	}
}

func recoveryRoleAndProvider(
	sessions []sessionResponse,
	providers agentProvidersResponse,
) (worker.Role, project.AgentProvider) {
	for index := len(sessions) - 1; index >= 0; index-- {
		role := sessions[index].Role
		if role == worker.RoleLead {
			return role, providers.Lead
		}
		if role == worker.RoleReviewer {
			return role, providers.Reviewer
		}
	}
	return "", ""
}

type interventionTargetResponse struct {
	Role      worker.Role `json:"role"`
	SessionID string      `json:"session_id"`
}

type interventionResponse struct {
	ID          string                       `json:"id"`
	SessionID   string                       `json:"session_id"`
	Target      worker.Role                  `json:"target"`
	Message     string                       `json:"message"`
	Status      execution.InterventionStatus `json:"status"`
	Effect      worker.InterventionEffect    `json:"effect,omitempty"`
	RequestedAt time.Time                    `json:"requested_at"`
	UpdatedAt   time.Time                    `json:"updated_at"`
	AnsweredAt  *time.Time                   `json:"answered_at,omitempty"`
	ResolvedAt  *time.Time                   `json:"resolved_at,omitempty"`
}

// startRunRequest holds the settings a work order starts with. Every field
// is optional and falls back to the work order's current settings.
type startRunRequest struct {
	AgentProviders             *agentProvidersRequest  `json:"agent_providers"`
	AgentModels                *agentModelsRequest     `json:"agent_models"`
	AutonomyPolicy             *project.AutonomyPolicy `json:"autonomy_policy"`
	MergePolicy                *project.MergePolicy    `json:"merge_policy"`
	IndependentAcceptanceTests *bool                   `json:"independent_acceptance_tests"`
	Steps                      *workStepsRequest       `json:"steps"`
	// PhaseChoices are the optional planning models and efforts (ADR-031).
	PhaseChoices *project.PhaseChoices `json:"phase_choices"`
}

// workStepsRequest names a work order's optional steps (ADR-018); an omitted
// step keeps the work order's current choice.
type workStepsRequest struct {
	// Clarification false starts a Draft work order with its title and
	// description as the goal. It is not stored: it only lets Start ready a
	// draft.
	Clarification  *bool `json:"clarification"`
	DocumentedPlan *bool `json:"documented_plan"`
	Reviewer       *bool `json:"reviewer"`
}

// workStepsResponse reports which optional steps a work order or run takes.
type workStepsResponse struct {
	DocumentedPlan bool `json:"documented_plan"`
	Reviewer       bool `json:"reviewer"`
}

func newWorkStepsResponse(steps project.WorkSteps) workStepsResponse {
	return workStepsResponse{DocumentedPlan: !steps.SkipPlan, Reviewer: !steps.SkipReviewer}
}

type startSettingsChooser interface {
	ChooseStartSettings(context.Context, string, string, feature.SettingsOverrides) (feature.Feature, error)
}

// readStartRequest decodes an optional Start body; an empty body is nil. It
// writes the error response itself and reports whether to continue.
func readStartRequest(w http.ResponseWriter, r *http.Request) (*startRunRequest, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		writeError(w, http.StatusBadRequest, "invalid_body", "start request is too large")
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, true
	}
	var request startRunRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "start request must be a JSON object with known fields")
		return nil, false
	}
	return &request, true
}

// skipsClarification reports whether a Start request starts a draft without
// clarification (ADR-018).
func (request *startRunRequest) skipsClarification() bool {
	return request != nil && request.Steps != nil && request.Steps.Clarification != nil &&
		!*request.Steps.Clarification
}

// readyWithoutClarification readies a draft work order whose Start skips
// clarification: its title and description become the handoff brief's goal,
// and the normal Start follows.
func (api *API) readyWithoutClarification(
	ctx context.Context,
	storedFeature feature.Feature,
	idempotencyKey string,
) (feature.Feature, error) {
	briefs, ok := api.artifacts.(HandoffBriefService)
	if !ok {
		return feature.Feature{}, errStartWithoutClarificationUnavailable
	}
	expected := 0
	current, err := api.artifacts.GetFeatureArtifact(ctx, storedFeature.ID, featureartifact.KindHandoffBrief)
	if err == nil {
		expected = current.Revision
	} else if !errors.Is(err, workflow.ErrArtifactNotFound) {
		return feature.Feature{}, err
	}
	goal := storedFeature.Title
	if description := strings.TrimSpace(storedFeature.Description); description != "" {
		goal += "\n\n" + description
	}
	if storedFeature.Kind == feature.KindSession {
		goal = sessionGoal
	}
	actor := workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}
	if _, err := briefs.PutHandoffBrief(
		ctx, storedFeature.ID, expected,
		featureartifact.HandoffBrief{
			Goal: goal, Areas: []string{}, Considerations: []string{}, OpenQuestions: []string{},
		},
		actor, idempotencyKey+":brief",
	); err != nil {
		return feature.Feature{}, err
	}
	if _, err := api.workflow.TransitionFeature(
		ctx, storedFeature.ID, feature.StateReady, actor, idempotencyKey+":ready",
	); err != nil {
		return feature.Feature{}, err
	}
	return api.features.GetByID(ctx, storedFeature.ProjectID, storedFeature.ID)
}

// sessionGoal is a session's goal (ADR-019): the user directs it message by
// message.
const sessionGoal = "Interactive session: the user directs each change in conversation with the agent."

var errStartWithoutClarificationUnavailable = errors.New("starting without clarification is unavailable")

// chooseStartSettings applies and saves the settings in a Start request. It
// writes the error response itself and reports whether to continue.
func (api *API) chooseStartSettings(
	w http.ResponseWriter,
	r *http.Request,
	storedFeature feature.Feature,
	request *startRunRequest,
) (feature.Feature, bool) {
	if request == nil {
		return storedFeature, true
	}
	var overrides feature.SettingsOverrides
	if request.AgentProviders != nil {
		providers, err := api.decodeAgentProviders(r.Context(), request.AgentProviders, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_agent_providers", "agent_providers must name existing lead and reviewer agents")
			return feature.Feature{}, false
		}
		overrides.AgentProviders = &providers
	}
	if request.AgentModels != nil {
		models, err := decodeAgentModels(request.AgentModels, false)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_agent_models", "agent_models must include explicit lead and reviewer model IDs")
			return feature.Feature{}, false
		}
		overrides.AgentModels = &models
	}
	overrides.MergePolicy = request.MergePolicy
	overrides.AutonomyPolicy = request.AutonomyPolicy
	overrides.IndependentAcceptanceTests = request.IndependentAcceptanceTests
	if request.Steps != nil {
		steps := storedFeature.Steps
		if request.Steps.DocumentedPlan != nil {
			steps.SkipPlan = !*request.Steps.DocumentedPlan
		}
		if request.Steps.Reviewer != nil {
			steps.SkipReviewer = !*request.Steps.Reviewer
		}
		overrides.Steps = &steps
	}
	if request.PhaseChoices != nil {
		providers := storedFeature.AgentProviders
		if overrides.AgentProviders != nil {
			providers = *overrides.AgentProviders
		}
		models := storedFeature.AgentModels
		if overrides.AgentModels != nil {
			models = *overrides.AgentModels
		}
		skipReviewer := storedFeature.Steps.SkipReviewer
		if overrides.Steps != nil {
			skipReviewer = overrides.Steps.SkipReviewer
		}
		if err := api.checkPhaseChoices(r.Context(), providers, models, *request.PhaseChoices, skipReviewer); err != nil {
			if !writeChoiceError(w, err) {
				log.Printf("check phase choices for feature %q: %v", storedFeature.ID, err)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
			return feature.Feature{}, false
		}
		overrides.PhaseChoices = request.PhaseChoices
	}
	chooser, ok := api.features.(startSettingsChooser)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "start_settings_unavailable", "choosing settings at Start is unavailable")
		return feature.Feature{}, false
	}
	// The models were checked against the agents' workers before (startChoice
	// and checkWorkChoice).
	chosen, err := chooser.ChooseStartSettings(r.Context(), storedFeature.ProjectID, storedFeature.ID, overrides)
	if err != nil {
		switch {
		case errors.Is(err, feature.ErrNotReady):
			writeError(w, http.StatusConflict, "feature_not_startable", "only a ready work order can be started")
		case errors.Is(err, project.ErrInvalidAgentProviders):
			writeError(w, http.StatusBadRequest, "invalid_agent_providers", "agent_providers must name existing lead and reviewer agents")
		case errors.Is(err, project.ErrInvalidAgentModels):
			writeError(w, http.StatusBadRequest, "invalid_agent_models", "agent_models must include explicit lead and reviewer model IDs")
		case errors.Is(err, project.ErrInvalidMergePolicy):
			writeError(w, http.StatusBadRequest, "invalid_merge_policy", "merge_policy must be require_user_approval or auto_after_gates")
		case errors.Is(err, project.ErrInvalidAutonomyPolicy):
			writeError(w, http.StatusBadRequest, "invalid_autonomy_policy", "autonomy_policy must be review_each_phase or run_to_completion")
		case errors.Is(err, project.ErrInvalidPhaseChoices):
			writeError(w, http.StatusBadRequest, "invalid_phase_choices", err.Error())
		case errors.Is(err, project.ErrInvalidWorkSteps):
			writeError(w, http.StatusBadRequest, "invalid_steps", "a reviewer requires a documented plan")
		default:
			log.Printf("choose start settings for feature %q: %v", storedFeature.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return feature.Feature{}, false
	}
	return chosen, true
}

func (api *API) startRunHandler(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}

	projectID := r.PathValue("projectID")
	featureID := r.PathValue("id")
	storedFeature, err := api.features.GetByID(r.Context(), projectID, featureID)
	if err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
			return
		}
		log.Printf("verify feature %q for run start: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	runID := runIDForKey(idempotencyKey)
	if existing, err := api.execution.GetRun(r.Context(), runID); err == nil {
		if existing.FeatureID != featureID {
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different run")
			return
		}
		w.Header().Set("Location", "/api/v1/runs/"+existing.ID)
		api.writeRun(w, r, http.StatusAccepted, existing)
		return
	} else if !errors.Is(err, execution.ErrNotFound) {
		log.Printf("check existing run %q: %v", runID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	request, ok := readStartRequest(w, r)
	if !ok {
		return
	}
	startedRun, ok := api.startFeature(w, r, storedFeature, idempotencyKey, request)
	if !ok {
		return
	}
	w.Header().Set("Location", "/api/v1/runs/"+startedRun.ID)
	api.writeRun(w, r, http.StatusAccepted, startedRun)
}

func (api *API) getRunHandler(w http.ResponseWriter, r *http.Request) {
	run, err := api.execution.GetRun(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			writeError(w, http.StatusNotFound, "run_not_found", "run not found")
			return
		}
		log.Printf("get run %q: %v", r.PathValue("id"), err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	api.writeRun(w, r, http.StatusOK, run)
}

func (api *API) listFeatureRunsHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	featureID := r.PathValue("id")
	if _, err := api.features.GetByID(r.Context(), projectID, featureID); err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
			return
		}
		log.Printf("verify feature %q for run history: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}

	runs, err := api.execution.RunsForFeature(r.Context(), featureID)
	if err != nil {
		log.Printf("list runs for feature %q: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	response := make([]runResponse, 0, len(runs))
	for _, run := range runs {
		runResponse, err := api.newRunResponse(r.Context(), run)
		if err != nil {
			log.Printf("build run %q for feature history: %v", run.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return
		}
		response = append(response, runResponse)
	}
	writeJSON(w, http.StatusOK, response, "feature run list")
}

func (api *API) writeRun(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	run execution.Run,
) {
	response, err := api.newRunResponse(r.Context(), run)
	if err != nil {
		log.Printf("build run response for %q: %v", run.ID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	writeJSON(w, status, response, "run")
}

func (api *API) newRunResponse(
	ctx context.Context,
	run execution.Run,
) (runResponse, error) {
	agentProviders, err := run.AgentProviders.Normalize()
	if err != nil {
		return runResponse{}, err
	}
	sessions, err := api.execution.SessionsForRun(ctx, run.ID)
	if err != nil {
		return runResponse{}, err
	}
	response := runResponse{
		ID: run.ID, FeatureID: run.FeatureID, Status: run.Status,
		Reason: run.Reason, WaitKind: run.WaitKind, Paused: run.Paused,
		DialogueLimits: dialogueLimitsResponse{
			PlanningRounds:             run.PlanningRoundLimit,
			ImplementationReviewRounds: run.ImplementationReviewRoundLimit,
		},
		AgentProviders: newAgentProvidersResponse(agentProviders),
		AgentModels: agentModelsResponse{
			Lead: run.AgentModels.Lead, Reviewer: run.AgentModels.Reviewer,
		},
		MergePolicy: run.MergePolicy, AutonomyPolicy: run.AutonomyPolicy,
		IndependentAcceptanceTests: run.IndependentAcceptanceTests,
		Steps:                      newWorkStepsResponse(run.Steps),
		PlanVersion:                run.PlanVersion,
		StartedAt:                  run.StartedAt, UpdatedAt: run.UpdatedAt,
		EndedAt: run.EndedAt, Sessions: make([]sessionResponse, 0, len(sessions)),
		InterventionTargets: make([]interventionTargetResponse, 0),
	}
	for _, session := range sessions {
		response.Sessions = append(response.Sessions, newSessionResponse(session))
	}
	if waits, ok := api.execution.(runLoginWaits); ok &&
		run.Status == execution.RunStatusWaitingForUser && run.WaitKind == execution.RunWaitKindBlocker {
		wait, err := waits.LoginWaitForRun(ctx, run.ID)
		if err != nil {
			return runResponse{}, err
		}
		if wait.AgentID != "" {
			response.LoginRequired = &loginRequiredResponse{Agent: wait.AgentID, Role: wait.Role}
		}
	}
	if waits, ok := api.execution.(runLimitWaits); ok &&
		run.Status == execution.RunStatusWaitingForUser && run.WaitKind == execution.RunWaitKindBlocker {
		wait, err := waits.LimitWaitForRun(ctx, run.ID)
		if err != nil {
			return runResponse{}, err
		}
		if wait.AgentID != "" {
			response.UsageLimit = &usageLimitResponse{Agent: wait.AgentID, Role: wait.Role, ResetsAt: wait.ResetsAt}
		}
	}
	if response.Retrying, err = retryingFor(ctx, api.execution, run); err != nil {
		return runResponse{}, err
	}
	targets, err := api.execution.InterventionTargetsForRun(ctx, run.ID)
	if err != nil {
		return runResponse{}, err
	}
	for _, target := range targets {
		response.InterventionTargets = append(response.InterventionTargets, interventionTargetResponse{
			Role: target.Role, SessionID: target.SessionID,
		})
	}
	intervention, err := api.execution.GetLatestIntervention(ctx, run.ID)
	if err == nil {
		response.Intervention = &interventionResponse{
			ID: intervention.ID, SessionID: intervention.SessionID,
			Target: intervention.Target, Message: intervention.Message,
			Status: intervention.Status, Effect: intervention.Effect,
			RequestedAt: intervention.RequestedAt,
			UpdatedAt:   intervention.UpdatedAt, AnsweredAt: intervention.AnsweredAt,
			ResolvedAt: intervention.ResolvedAt,
		}
	} else if !errors.Is(err, execution.ErrNotFound) {
		return runResponse{}, err
	}
	return response, nil
}

func runIDForKey(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return "run_" + hex.EncodeToString(digest[:16])
}

// startFeature readies a draft that skips clarification (a session always
// does), applies the Start settings, and starts the run. It writes the error
// response itself and reports whether it started.
func (api *API) startFeature(
	w http.ResponseWriter,
	r *http.Request,
	storedFeature feature.Feature,
	idempotencyKey string,
	request *startRunRequest,
) (execution.Run, bool) {
	runID := runIDForKey(idempotencyKey)
	projectID, featureID := storedFeature.ProjectID, storedFeature.ID
	readies := storedFeature.State == feature.StateDraft &&
		(request.skipsClarification() || storedFeature.Kind == feature.KindSession)
	if storedFeature.State != feature.StateReady && !readies {
		writeError(w, http.StatusConflict, "feature_not_startable", "only a ready work order can be started")
		return execution.Run{}, false
	}
	// The choice of agents and models is checked before anything changes, so
	// a refused Start leaves a draft a draft (ADR-027).
	providers, models, skipReviewer, ok := api.startChoice(w, r, storedFeature, request)
	if !ok {
		return execution.Run{}, false
	}
	if err := api.checkWorkChoice(r.Context(), providers, models, skipReviewer); err != nil {
		if !writeChoiceError(w, err) {
			log.Printf("check the agents of feature %q: %v", storedFeature.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return execution.Run{}, false
	}
	var err error
	if readies {
		readied, err := api.readyWithoutClarification(r.Context(), storedFeature, idempotencyKey)
		switch {
		case errors.Is(err, featureartifact.ErrInvalidArtifact):
			writeError(w, http.StatusBadRequest, "invalid_goal", "the work order's title and description do not make a valid goal")
			return execution.Run{}, false
		case errors.Is(err, errStartWithoutClarificationUnavailable):
			writeError(w, http.StatusServiceUnavailable, "start_without_clarification_unavailable", err.Error())
			return execution.Run{}, false
		case err != nil:
			log.Printf("ready feature %q without clarification: %v", storedFeature.ID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			return execution.Run{}, false
		}
		storedFeature = readied
	}
	if storedFeature.State != feature.StateReady {
		writeError(w, http.StatusConflict, "feature_not_startable", "only a ready work order can be started")
		return execution.Run{}, false
	}
	// Agents, models, and policies are chosen at Start; an empty body starts
	// with the work order's current settings, which must name its agents.
	chosen, ok := api.chooseStartSettings(w, r, storedFeature, request)
	if !ok {
		return execution.Run{}, false
	}
	storedFeature = chosen
	goal := storedFeature.Title
	if storedFeature.Description != "" {
		goal += ": " + storedFeature.Description
	}
	var startedRun execution.Run
	if storedFeature.AgentModels.Lead != "" || storedFeature.AgentModels.Reviewer != "" {
		modelStarter, ok := api.starter.(ModelRunStarter)
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "agent_models_unavailable", "model-specific run admission is unavailable")
			return execution.Run{}, false
		}
		startedRun, _, err = modelStarter.StartWithModels(
			r.Context(), runID, projectID, featureID, goal, project.DefaultDialogueLimits(),
			storedFeature.AgentProviders, storedFeature.AgentModels,
			storedFeature.MergePolicy, storedFeature.AutonomyPolicy,
		)
	} else {
		startedRun, _, err = api.starter.Start(
			r.Context(), runID, projectID, featureID, goal, project.DefaultDialogueLimits(),
			storedFeature.AgentProviders, storedFeature.MergePolicy, storedFeature.AutonomyPolicy,
		)
	}
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrRecordConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different run")
		case errors.Is(err, orchestration.ErrStepsUnsupported):
			writeError(w, http.StatusConflict, "steps_unsupported", "this combination of work-order steps is not supported yet")
		case errors.Is(err, orchestration.ErrFeatureNotReady):
			writeError(w, http.StatusConflict, "feature_not_startable", "only a ready work order with a handoff brief can be started")
		case errors.Is(err, orchestration.ErrInvalidRunRequest):
			writeError(w, http.StatusBadRequest, "invalid_run", "run request is invalid")
		case errors.Is(err, workspace.ErrProjectRepositoryNotBound):
			writeError(w, http.StatusConflict, "forgejo_repository_not_bound", "project must be bound to a Forgejo repository before starting a real-provider run")
		case errors.Is(err, workspace.ErrBranchNotFound), errors.Is(err, project.ErrForgejoRepositoryNotReady):
			writeError(w, http.StatusConflict, "forgejo_repository_not_ready", "the bound Forgejo repository or its default branch is not ready")
		case errors.Is(err, workspace.ErrConflict), errors.Is(err, workspace.ErrBranchConflict),
			errors.Is(err, workspace.ErrCheckoutConflict):
			writeError(w, http.StatusConflict, "workspace_conflict", "the selected project's managed checkout disagrees with its durable workspace; user review is required")
		case errors.Is(err, workspace.ErrCheckoutUnavailable):
			writeError(w, http.StatusServiceUnavailable, "checkout_unavailable", "the selected project's managed checkout cannot be prepared right now")
		case errors.Is(err, project.ErrForgejoUnavailable):
			writeError(w, http.StatusServiceUnavailable, "forgejo_unavailable", "Forgejo workspace preparation is unavailable")
		default:
			log.Printf("start run %q for feature %q: %v", runID, featureID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
		return execution.Run{}, false
	}

	return startedRun, true
}
