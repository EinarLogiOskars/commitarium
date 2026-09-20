package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

type featureArtifactResponse struct {
	FeatureID string               `json:"feature_id"`
	Kind      featureartifact.Kind `json:"kind"`
	Revision  int                  `json:"revision"`
	Document  json.RawMessage      `json:"document"`
	UpdatedBy eventActorResponse   `json:"updated_by"`
	UpdatedAt time.Time            `json:"updated_at"`
}

type putGoalDraftRequest struct {
	ExpectedRevision int                       `json:"expected_revision"`
	Document         featureartifact.GoalDraft `json:"document"`
}

type transitionPlanStepRequest struct {
	PlanVersion int                        `json:"plan_version"`
	Status      featureartifact.StepStatus `json:"status"`
	CommitID    string                     `json:"commit_id"`
}

func (api *API) getFeatureArtifactHandler(w http.ResponseWriter, r *http.Request) {
	projectID, featureID := r.PathValue("projectID"), r.PathValue("id")
	if !api.requireFeature(w, r, projectID, featureID) {
		return
	}
	kind := featureartifact.Kind(r.PathValue("kind"))
	if !kind.IsValid() {
		writeError(w, http.StatusNotFound, "artifact_not_found", "feature artifact not found")
		return
	}
	artifact, err := api.artifacts.GetFeatureArtifact(r.Context(), featureID, kind)
	if err != nil {
		writeFeatureArtifactError(w, featureID, err)
		return
	}
	writeJSON(w, http.StatusOK, newFeatureArtifactResponse(artifact), "feature artifact")
}

func (api *API) putGoalDraftArtifactHandler(w http.ResponseWriter, r *http.Request) {
	projectID, featureID := r.PathValue("projectID"), r.PathValue("id")
	if !api.requireFeature(w, r, projectID, featureID) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	request := putGoalDraftRequest{}
	if err := decodeArtifactJSONBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	artifact, err := api.artifacts.PutGoalDraft(
		r.Context(), featureID, request.ExpectedRevision, request.Document,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID}, key,
	)
	if err != nil {
		writeFeatureArtifactError(w, featureID, err)
		return
	}
	writeJSON(w, http.StatusOK, newFeatureArtifactResponse(artifact), "goal draft artifact")
}

func (api *API) transitionImplementationPlanStepHandler(w http.ResponseWriter, r *http.Request) {
	projectID, featureID := r.PathValue("projectID"), r.PathValue("id")
	if !api.requireFeature(w, r, projectID, featureID) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	request := transitionPlanStepRequest{}
	if err := decodeArtifactJSONBody(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	artifact, err := api.artifacts.TransitionImplementationPlanStep(
		r.Context(), featureID, request.PlanVersion, r.PathValue("stepID"),
		request.Status, request.CommitID,
		workflow.Actor{Kind: workflow.ActorKindAgent, ID: "implementation-lead"}, key,
	)
	if err != nil {
		writeFeatureArtifactError(w, featureID, err)
		return
	}
	writeJSON(w, http.StatusOK, newFeatureArtifactResponse(artifact), "implementation plan step")
}

func decodeArtifactJSONBody(r *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 256*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("request body must contain valid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func (api *API) requireFeature(w http.ResponseWriter, r *http.Request, projectID, featureID string) bool {
	if _, err := api.features.GetByID(r.Context(), projectID, featureID); err != nil {
		if errors.Is(err, feature.ErrNotFound) {
			writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
			return false
		}
		log.Printf("verify feature %q for project %q: %v", featureID, projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return false
	}
	return true
}

func newFeatureArtifactResponse(artifact workflow.FeatureArtifact) featureArtifactResponse {
	return featureArtifactResponse{
		FeatureID: artifact.FeatureID, Kind: artifact.Kind, Revision: artifact.Revision,
		Document:  json.RawMessage(artifact.Document),
		UpdatedBy: eventActorResponse{Kind: artifact.Actor.Kind, ID: artifact.Actor.ID},
		UpdatedAt: artifact.UpdatedAt,
	}
}

func writeFeatureArtifactError(w http.ResponseWriter, featureID string, err error) {
	switch {
	case errors.Is(err, workflow.ErrArtifactNotFound):
		writeError(w, http.StatusNotFound, "artifact_not_found", "feature artifact not found")
	case errors.Is(err, workflow.ErrArtifactConflict):
		writeError(w, http.StatusConflict, "artifact_revision_conflict", "feature artifact changed; reload its latest revision")
	case errors.Is(err, workflow.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used for a different operation")
	case errors.Is(err, featureartifact.ErrInvalidArtifact):
		writeError(w, http.StatusBadRequest, "invalid_feature_artifact", "feature artifact is invalid")
	case errors.Is(err, feature.ErrNotFound):
		writeError(w, http.StatusNotFound, "feature_not_found", "feature not found")
	default:
		log.Printf("feature artifact for %q: %v", featureID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
