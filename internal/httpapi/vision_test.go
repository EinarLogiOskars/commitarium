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

// A slice of a written plan starts a work order whose brief carries the
// plan: the first slice commits it, and a later one reads it at its path
// (PLAN_MODE_PLAN.md PM6).
func TestASliceWorkOrderCarriesTheWrittenPlan(t *testing.T) {
	projects := &recordingProjectService{getByIDResult: project.Project{ID: "prj_test"}}
	visions, err := vision.NewService(t.TempDir(), t.TempDir(), projects, noVisionWorkers)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, _, err := visions.StartPlanning(ctx, "prj_test", "A to-do app.", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := visions.UpdatePlanning(ctx, "prj_test", vision.PlanningUpdate{
		Document: "# To-do app\n\n- [ ] 1. Lists\n- [ ] 2. Sharing",
		Items:    []featureartifact.PlanItem{{Title: "Lists"}, {Title: "Sharing"}},
	}); err != nil {
		t.Fatal(err)
	}
	features := &recordingFeatureService{createResult: feature.Feature{ID: "fea_x", ProjectID: "prj_test", State: feature.StateDraft}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		projects, features, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, visions)
	start := func(itemID string) string {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
			"/api/v1/projects/prj_test/vision/items/"+itemID+"/work-order", nil))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
		}
		return features.receivedDescription
	}
	if first := start("lists"); !strings.Contains(first, "Commit the plan, as it is, at docs/plans/to-do-app.md") ||
		!strings.Contains(first, "- [ ] 2. Sharing") {
		t.Fatalf("the first slice's brief = %q", first)
	}
	if later := start("sharing"); !strings.Contains(later, "read the plan there") ||
		!strings.Contains(later, "docs/plans/to-do-app.md") || strings.Contains(later, "2. Sharing\n") {
		t.Fatalf("a later slice's brief = %q", later)
	}
}
