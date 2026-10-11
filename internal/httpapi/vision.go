package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// VisionService keeps each project's vision and the conversations in which
// the assistant writes it (ADR-020).
type VisionService interface {
	Get(ctx context.Context, projectID string) (vision.Vision, error)
	Save(ctx context.Context, projectID string, plan featureartifact.VisionPlan, expectedRevision int) (vision.Vision, error)
	LinkWorkOrder(ctx context.Context, projectID, itemID, featureID string) (vision.Vision, error)
	Start(ctx context.Context, projectID, agentID, model string, mode vision.Mode, message, idempotencyKey string) (vision.Session, bool, error)
	GetSession(ctx context.Context, projectID, sessionID string) (vision.Session, error)
	Reply(ctx context.Context, projectID, sessionID, message, idempotencyKey string) (vision.Session, bool, error)
}

func (api *API) getVisionHandler(w http.ResponseWriter, r *http.Request) {
	stored, err := api.visions.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeVisionError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, stored, "vision")
}

type saveVisionRequest struct {
	ExpectedRevision int                        `json:"expected_revision"`
	Plan             featureartifact.VisionPlan `json:"plan"`
}

func (api *API) saveVisionHandler(w http.ResponseWriter, r *http.Request) {
	var body saveVisionRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	stored, err := api.visions.Save(r.Context(), r.PathValue("id"), body.Plan, body.ExpectedRevision)
	if err != nil {
		writeVisionError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, stored, "vision")
}

type startVisionRequest struct {
	Mode    vision.Mode `json:"mode"`
	Message string      `json:"message"`
	// Agent and Model default to the project's lead agent and model.
	Agent string `json:"agent,omitempty"`
	Model string `json:"model,omitempty"`
}

func (api *API) startVisionHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	var body startVisionRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	if body.Agent == "" || body.Model == "" {
		stored, err := api.projects.GetByID(r.Context(), r.PathValue("id"))
		if err != nil {
			writeVisionError(w, r.PathValue("id"), err)
			return
		}
		if body.Agent == "" {
			body.Agent = stored.AgentProviders.LeadAgent
		}
		if body.Model == "" {
			body.Model = stored.AgentModels.Lead
		}
	}
	session, created, err := api.visions.Start(r.Context(), r.PathValue("id"), body.Agent, body.Model, body.Mode, body.Message, key)
	if err != nil {
		writeVisionError(w, r.PathValue("id"), err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, session, "vision conversation")
}

func (api *API) getVisionSessionHandler(w http.ResponseWriter, r *http.Request) {
	session, err := api.visions.GetSession(r.Context(), r.PathValue("id"), r.PathValue("sessionID"))
	if err != nil {
		writeVisionError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusOK, session, "vision conversation")
}

func (api *API) replyVisionHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
		return
	}
	var body sessionMessageRequest
	if !decodeSingleJSON(w, r, &body) {
		return
	}
	session, _, err := api.visions.Reply(r.Context(), r.PathValue("id"), r.PathValue("sessionID"), body.Message, key)
	if err != nil {
		writeVisionError(w, r.PathValue("id"), err)
		return
	}
	writeJSON(w, http.StatusAccepted, session, "vision conversation")
}

type visionWorkOrderResponse struct {
	WorkOrder featureResponse `json:"work_order"`
	Vision    vision.Vision   `json:"vision"`
}

// startVisionWorkOrderHandler creates the draft work order that delivers an
// item without sub-items, and links it to the item. An item that already has
// one answers it.
func (api *API) startVisionWorkOrderHandler(w http.ResponseWriter, r *http.Request) {
	projectID, itemID := r.PathValue("id"), r.PathValue("itemID")
	stored, err := api.visions.Get(r.Context(), projectID)
	if err != nil {
		writeVisionError(w, projectID, err)
		return
	}
	item, label, found := vision.Find(stored.Plan, itemID)
	if !found || len(item.Items) > 0 || (item.Done && item.FeatureID == "") {
		writeError(w, http.StatusConflict, "vision_item_not_startable", "only an item without sub-items that is not done starts a work order")
		return
	}
	if item.FeatureID != "" {
		if existing, err := api.features.GetByID(r.Context(), projectID, item.FeatureID); err == nil {
			writeJSON(w, http.StatusOK, visionWorkOrderResponse{WorkOrder: newFeatureResponse(existing), Vision: stored}, "vision work order")
			return
		}
	}
	if api.toolchains != nil {
		manifest, err := api.projectToolchain(r.Context(), projectID)
		if err != nil {
			api.writeToolchainError(w, projectID, "check before work-order creation", err)
			return
		}
		if manifest.Status != toolchain.StatusConfigured {
			writeError(w, http.StatusConflict, "project_toolchain_required", "choose or detect the project toolchain before creating a work order")
			return
		}
	}
	description := item.Description
	if description != "" {
		description += "\n\n"
	}
	description += "Part of the project's vision as " + label + "."
	created, err := api.features.Create(r.Context(), projectID, item.Title, description, feature.SettingsOverrides{})
	if err != nil {
		log.Printf("create the work order for vision item %q: %v", itemID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	linked, err := api.visions.LinkWorkOrder(r.Context(), projectID, itemID, created.ID)
	if err != nil {
		writeVisionError(w, projectID, err)
		return
	}
	writeJSON(w, http.StatusCreated, visionWorkOrderResponse{WorkOrder: newFeatureResponse(created), Vision: linked}, "vision work order")
}

func writeVisionError(w http.ResponseWriter, projectID string, err error) {
	switch {
	case errors.Is(err, vision.ErrNotFound):
		writeError(w, http.StatusNotFound, "vision_not_found", err.Error())
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", "project not found")
	case errors.Is(err, vision.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_vision", err.Error())
	case errors.Is(err, vision.ErrConflict):
		writeError(w, http.StatusConflict, "vision_conflict", err.Error())
	case errors.Is(err, vision.ErrNotReady):
		writeError(w, http.StatusConflict, "vision_assistant_busy", err.Error())
	case errors.Is(err, vision.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "vision_unavailable", err.Error())
	default:
		log.Printf("vision for project %q: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
