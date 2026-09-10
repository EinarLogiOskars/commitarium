package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type runResponse struct {
	ID             string                 `json:"id"`
	FeatureID      string                 `json:"feature_id"`
	Status         execution.RunStatus    `json:"status"`
	Reason         string                 `json:"reason,omitempty"`
	DialogueLimits dialogueLimitsResponse `json:"dialogue_limits"`
	StartedAt      time.Time              `json:"started_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	EndedAt        *time.Time             `json:"ended_at,omitempty"`
	Sessions       []sessionResponse      `json:"sessions"`
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

	if storedFeature.State != feature.StateDraft ||
		storedFeature.AcceptedGoal != "" || storedFeature.GoalAcceptedAt != nil {
		writeError(w, http.StatusConflict, "feature_not_startable", "feature is not available to start a new run")
		return
	}
	storedProject, err := api.projects.GetByID(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		log.Printf("load project %q for run start: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	goal := storedFeature.Title
	if storedFeature.Description != "" {
		goal += ": " + storedFeature.Description
	}
	startedRun, _, err := api.starter.Start(
		r.Context(), runID, projectID, featureID, goal, storedProject.DialogueLimits,
	)
	if err != nil {
		switch {
		case errors.Is(err, execution.ErrRecordConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different run")
		case errors.Is(err, orchestration.ErrInvalidRunRequest):
			writeError(w, http.StatusBadRequest, "invalid_run", "run request is invalid")
		default:
			log.Printf("start run %q for feature %q: %v", runID, featureID, err)
			writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		}
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
	sessions, err := api.execution.SessionsForRun(ctx, run.ID)
	if err != nil {
		return runResponse{}, err
	}
	response := runResponse{
		ID: run.ID, FeatureID: run.FeatureID, Status: run.Status,
		Reason: run.Reason,
		DialogueLimits: dialogueLimitsResponse{
			PlanningRounds:             run.PlanningRoundLimit,
			ImplementationReviewRounds: run.ImplementationReviewRoundLimit,
		},
		StartedAt: run.StartedAt, UpdatedAt: run.UpdatedAt,
		EndedAt: run.EndedAt, Sessions: make([]sessionResponse, 0, len(sessions)),
	}
	for _, session := range sessions {
		response.Sessions = append(response.Sessions, newSessionResponse(session))
	}
	return response, nil
}

func runIDForKey(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return "run_" + hex.EncodeToString(digest[:16])
}
