package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
)

type featureDeletionStub struct {
	result    workorder.Result
	err       error
	projectID string
	featureID string
}

func (stub *featureDeletionStub) Delete(_ context.Context, projectID, featureID string) (workorder.Result, error) {
	stub.projectID = projectID
	stub.featureID = featureID
	return stub.result, stub.err
}

func TestDeleteFeatureReturnsExplicitMergedChangeDisposition(t *testing.T) {
	deletion := &featureDeletionStub{result: workorder.Result{
		ProjectID: "prj_test", FeatureID: "fea_test", Deleted: true, MergedChangesRemain: true,
	}}
	handler := NewWithWorkspaceRealWorkflowAndDeletionService(
		nil, nil, nil, nil, nil, nil, nil, nil, deletion,
	)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/prj_test/features/fea_test", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || deletion.projectID != "prj_test" || deletion.featureID != "fea_test" {
		t.Fatalf("unexpected deletion response: code=%d deletion=%+v body=%s", response.Code, deletion, response.Body.String())
	}
	var result workorder.Result
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatalf("decode deletion response: %v", err)
	}
	if !result.Deleted || !result.MergedChangesRemain {
		t.Fatalf("completed deletion did not state that merged changes remain: %+v", result)
	}
}

func TestDeleteFeatureRefusesActiveRunAndReturnsMissingAsNotFound(t *testing.T) {
	deletion := &featureDeletionStub{err: workorder.ErrActive}
	handler := NewWithWorkspaceRealWorkflowAndDeletionService(
		nil, nil, nil, nil, nil, nil, nil, nil, deletion,
	)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/prj_test/features/fea_test", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !containsAll(response.Body.String(), `"code":"feature_active"`) {
		t.Fatalf("unexpected active response: %d %s", response.Code, response.Body.String())
	}

	deletion.err = feature.ErrNotFound
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodDelete, "/api/v1/projects/prj_test/features/fea_test", nil,
	))
	if response.Code != http.StatusNotFound || !containsAll(response.Body.String(), `"code":"feature_not_found"`) {
		t.Fatalf("unexpected missing response: %d %s", response.Code, response.Body.String())
	}
}
