package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func noVisionWorkers(string) (workerhttp.Service, error) { return nil, errors.New("no workers") }

func TestVisionItemStartsOneWorkOrder(t *testing.T) {
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	visions, err := vision.NewService(t.TempDir(), t.TempDir(), projects, noVisionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := visions.Save(t.Context(), "prj_test", featureartifact.VisionPlan{
		Statement: "A to-do app people enjoy.", Confidence: "high",
		Priorities: []featureartifact.VisionPriority{{Level: 0, Items: []featureartifact.VisionItem{
			{ID: "lists", Title: "Lists", Items: []featureartifact.VisionItem{
				{ID: "create-list", Title: "Create a list", Description: "Name it."},
			}},
		}}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	features := &recordingFeatureService{createResult: feature.Feature{
		ID: "fea_list", ProjectID: "prj_test", Title: "Create a list", State: feature.StateDraft,
	}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, visions)
	start := func(itemID string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/v1/projects/prj_test/vision/items/"+itemID+"/work-order", nil))
		return recorder
	}

	if recorder := start("lists"); recorder.Code != http.StatusConflict {
		t.Fatalf("a group started a work order: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder := start("create-list")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	if features.receivedTitle != "Create a list" ||
		!strings.Contains(features.receivedDescription, "Name it.") ||
		!strings.Contains(features.receivedDescription, "P0.1.1") {
		t.Fatalf("work order %q %q", features.receivedTitle, features.receivedDescription)
	}
	var body visionWorkOrderResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Vision.Plan.Priorities[0].Items[0].Items[0].FeatureID != "fea_list" {
		t.Fatalf("item not linked: %+v", body.Vision.Plan)
	}
	features.getResult = features.createResult
	if recorder := start("create-list"); recorder.Code != http.StatusOK || features.createCalls != 1 {
		t.Fatalf("second start: %d, %d creates", recorder.Code, features.createCalls)
	}
}

func TestVisionGetAnswersNotFoundBeforeOneIsWritten(t *testing.T) {
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	visions, err := vision.NewService(t.TempDir(), t.TempDir(), projects, noVisionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewWithWorkspaceRealWorkflowDeletionAndModels(
		projects, &recordingFeatureService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, visions,
	).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/projects/prj_test/vision", nil))
	if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "vision_not_found") {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
}
