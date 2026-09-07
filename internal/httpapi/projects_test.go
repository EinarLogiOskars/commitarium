package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type recordingProjectService struct {
	calls        int
	receivedName string
	result       project.Project
	err          error

	receivedID    string
	getByIDResult project.Project
	getByIDErr    error
}

type testErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *recordingProjectService) Create(
	_ context.Context,
	name string,
) (project.Project, error) {
	s.calls++
	s.receivedName = name
	return s.result, s.err
}

func (s *recordingProjectService) GetByID(
	_ context.Context,
	id string,
) (project.Project, error) {
	s.receivedID = id
	return s.getByIDResult, s.getByIDErr
}

func TestCreateProject(t *testing.T) {
	fixedTime := time.Date(
		2026,
		time.September,
		6,
		12,
		0,
		0,
		0,
		time.UTC,
	)

	service := &recordingProjectService{
		result: project.Project{
			ID:        "prj_test",
			Name:      "Commitarium",
			CreatedAt: fixedTime,
		},
	}

	body := strings.NewReader(`{"name":"Commitarium"}`)

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		body,
	)
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	New(service).ServeHTTP(recorder, req)

	var response struct {
		ID        string    `json:"id"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}

	res := recorder.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("expected status code %d, got %d", http.StatusCreated, res.StatusCode)
	}

	expectedLocation := "/api/v1/projects/prj_test"

	if location := res.Header.Get("Location"); location != expectedLocation {
		t.Errorf(
			"expected Location header %q, got %q",
			expectedLocation,
			location,
		)
	}

	if contentType := res.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if service.receivedName != "Commitarium" {
		t.Errorf("expected receivedName Commitarium, got %v", service.receivedName)
	}

	if response.ID != service.result.ID {
		t.Errorf("expected response ID %v, got %v", service.result.ID, response.ID)
	}

	if response.Name != service.result.Name {
		t.Errorf("expected response Name %v, got %v", service.result.Name, response.Name)
	}

	if response.CreatedAt != service.result.CreatedAt {
		t.Errorf("expected response CreatedAt %v, got %v", service.result.CreatedAt, response.CreatedAt)
	}
}

func TestCreateProjectRejectsMalformedJSON(t *testing.T) {
	service := &recordingProjectService{}
	requestBody := strings.NewReader(`{"name":`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	New(service).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusBadRequest,
			response.StatusCode,
		)
	}

	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected application/json, got %q", contentType)
	}

	var responseBody testErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&responseBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if responseBody.Error.Code != "invalid_json" {
		t.Fatalf(
			"expected error code %q, got %q",
			"invalid_json",
			responseBody.Error.Code,
		)
	}

	if responseBody.Error.Message != "request body must contain valid JSON" {
		t.Errorf(
			"expected error message %q, got %q",
			"request body must contain valid JSON",
			responseBody.Error.Message,
		)
	}

	if service.calls != 0 {
		t.Fatalf(
			"expected project service not to be called, got %d calls",
			service.calls,
		)
	}
}

func TestCreateProjectRejectsEmptyName(t *testing.T) {
	service := &recordingProjectService{
		err: project.ErrNameRequired,
	}
	requestBody := strings.NewReader(`{"name":""}`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)

	recorder := httptest.NewRecorder()
	New(service).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusBadRequest,
			response.StatusCode,
		)
	}

	var body errorResponse

	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if body.Error.Code != "project_name_required" {
		t.Errorf(
			"expected error code %q, got %q",
			"project_name_required",
			body.Error.Code,
		)
	}

	if body.Error.Message != "project name is required" {
		t.Errorf(
			"expected error message %q, got %q",
			"project name is required",
			body.Error.Message,
		)
	}

	if service.calls != 1 {
		t.Fatalf("expected project service to be called once, got %d", service.calls)
	}
}

func TestCreateProjectHandlesUnexpectedError(t *testing.T) {
	service := &recordingProjectService{
		err: errors.New("database connection failed"),
	}
	requestBody := strings.NewReader(`{"name":"Commitarium"}`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)

	recorder := httptest.NewRecorder()
	New(service).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusInternalServerError,
			response.StatusCode,
		)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	var decodedBody errorResponse

	if err := json.Unmarshal(body, &decodedBody); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if decodedBody.Error.Code != "internal_error" {
		t.Errorf(
			"expected error code %q, got %q",
			"internal_error",
			decodedBody.Error.Code,
		)
	}

	if decodedBody.Error.Message != "internal server error" {
		t.Errorf(
			"expected error message %q, got %q",
			"internal server error",
			decodedBody.Error.Message,
		)
	}

	if strings.Contains(string(body), service.err.Error()) {
		t.Fatal("response exposed the internal error")
	}
}

func TestGetProjectByID(t *testing.T) {
	expected := project.Project{
		ID:        "prj_test",
		Name:      "Commitarium",
		CreatedAt: time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC),
	}

	projects := &recordingProjectService{
		getByIDResult: expected,
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_test",
		nil,
	)

	recorder := httptest.NewRecorder()
	New(projects).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusOK,
			response.StatusCode,
		)
	}

	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	var body projectResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode project response: %v", err)
	}

	if projects.receivedID != expected.ID {
		t.Errorf(
			"expected service to receive ID %q, got %q",
			expected.ID,
			projects.receivedID,
		)
	}

	if body.ID != expected.ID {
		t.Errorf("expected ID %q, got %q", expected.ID, body.ID)
	}

	if body.Name != expected.Name {
		t.Errorf("expected name %q, got %q", expected.Name, body.Name)
	}

	if body.CreatedAt != expected.CreatedAt {
		t.Errorf(
			"expected creation time %v, got %v",
			expected.CreatedAt,
			body.CreatedAt,
		)
	}
}

func TestGetProjectByIDReturnsNotFound(t *testing.T) {
	service := &recordingProjectService{
		getByIDErr: project.ErrNotFound,
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_missing",
		nil,
	)

	recorder := httptest.NewRecorder()
	New(service).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusNotFound {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusNotFound,
			response.StatusCode,
		)
	}

	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	var body errorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if body.Error.Code != "project_not_found" {
		t.Errorf(
			"expected error code %q, got %q",
			"project_not_found",
			body.Error.Code,
		)
	}

	if body.Error.Message != "project not found" {
		t.Errorf(
			"expected error message %q, got %q",
			"project not found",
			body.Error.Message,
		)
	}

	if service.receivedID != "prj_missing" {
		t.Errorf(
			"expected service to receive ID %q, got %q",
			"prj_missing",
			service.receivedID,
		)
	}
}
