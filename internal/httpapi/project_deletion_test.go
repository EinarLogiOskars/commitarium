package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectdeletion"
)

type projectDeletionStub struct {
	result    projectdeletion.Result
	err       error
	projectID string
	key       string
	force     bool
}

func (stub *projectDeletionStub) Delete(_ context.Context, projectID, key string, force bool) (projectdeletion.Result, error) {
	stub.projectID, stub.key, stub.force = projectID, key, force
	return stub.result, stub.err
}

func projectDeletionHandler(stub *projectDeletionStub) http.Handler {
	return newAPI(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stub)
}

func TestDeleteProjectRequiresKeyAndForwardsForce(t *testing.T) {
	stub := &projectDeletionStub{result: projectdeletion.Result{ProjectID: "prj_test", Deleted: true}}
	handler := projectDeletionHandler(stub)

	missingKey := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/prj_test", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, missingKey)
	if response.Code != http.StatusBadRequest || !containsAll(response.Body.String(), `"code":"idempotency_key_required"`) {
		t.Fatalf("missing-key response=%d %s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/prj_test?force=true", nil)
	request.Header.Set("Idempotency-Key", "delete-project-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.projectID != "prj_test" || stub.key != "delete-project-1" || !stub.force {
		t.Fatalf("delete response=%d stub=%+v body=%s", response.Code, stub, response.Body.String())
	}
}

func TestDeleteProjectMapsStableErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "missing", err: project.ErrNotFound, status: http.StatusNotFound, code: "project_not_found"},
		{name: "active", err: projectdeletion.ErrActive, status: http.StatusConflict, code: "project_has_active_run"},
		{name: "conflict", err: projectdeletion.ErrConflict, status: http.StatusConflict, code: "idempotency_conflict"},
		{name: "unsafe", err: projectdeletion.ErrUnsafeArtifacts, status: http.StatusConflict, code: "project_deletion_conflict"},
		{name: "unavailable", err: project.ErrForgejoUnavailable, status: http.StatusServiceUnavailable, code: "project_deletion_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &projectDeletionStub{err: test.err}
			request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/prj_test", nil)
			request.Header.Set("Idempotency-Key", "delete-project-1")
			response := httptest.NewRecorder()
			projectDeletionHandler(stub).ServeHTTP(response, request)
			if response.Code != test.status || !containsAll(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDeleteProjectRejectsBodyAndInvalidForceWithoutCallingService(t *testing.T) {
	for _, target := range []string{
		"/api/v1/projects/prj_test?force=yes",
		"/api/v1/projects/prj_test?force=true&force=false",
	} {
		stub := &projectDeletionStub{err: errors.New("must not be called")}
		request := httptest.NewRequest(http.MethodDelete, target, nil)
		request.Header.Set("Idempotency-Key", "delete-project-1")
		response := httptest.NewRecorder()
		projectDeletionHandler(stub).ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || stub.projectID != "" {
			t.Fatalf("invalid force response=%d stub=%+v", response.Code, stub)
		}
	}
	stub := &projectDeletionStub{}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/prj_test", strings.NewReader(`{}`))
	request.Header.Set("Idempotency-Key", "delete-project-1")
	response := httptest.NewRecorder()
	projectDeletionHandler(stub).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || stub.projectID != "" {
		t.Fatalf("body response=%d stub=%+v", response.Code, stub)
	}
}
