package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestApprovedEnvironmentPackagesAreReadWithoutARequest(t *testing.T) {
	environments := &attentionEnvironmentService{packages: []string{"git-lfs", "libvips-dev"}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(
		&recordingProjectService{}, &recordingFeatureService{}, nil, &recordingExecutionService{},
		nil, nil, nil, nil, nil, nil, nil, nil, environments,
	)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/environment/approved-packages", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	var response struct {
		ApprovedPackages []string `json:"approved_packages"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode approved packages: %v", err)
	}
	if !slices.Equal(response.ApprovedPackages, []string{"git-lfs", "libvips-dev"}) {
		t.Fatalf("unexpected approved packages: %+v", response.ApprovedPackages)
	}
}
