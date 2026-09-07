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

type recordingProjectCreator struct {
	calls        int
	receivedName string
	result       project.Project
	err          error
}

type testErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *recordingProjectCreator) Create(
	_ context.Context,
	name string,
) (project.Project, error) {
	c.calls++
	c.receivedName = name
	return c.result, c.err
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

	creator := &recordingProjectCreator{
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
	New(creator).ServeHTTP(recorder, req)

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

	if contentType := res.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if creator.receivedName != "Commitarium" {
		t.Errorf("expected receivedName Commitarium, got %v", creator.receivedName)
	}

	if response.ID != creator.result.ID {
		t.Errorf("expected response ID %v, got %v", creator.result.ID, response.ID)
	}

	if response.Name != creator.result.Name {
		t.Errorf("expected response Name %v, got %v", creator.result.Name, response.Name)
	}

	if response.CreatedAt != creator.result.CreatedAt {
		t.Errorf("expected response CreatedAt %v, got %v", creator.result.CreatedAt, response.CreatedAt)
	}
}

func TestCreateProjectRejectsMalformedJSON(t *testing.T) {
	creator := &recordingProjectCreator{}
	requestBody := strings.NewReader(`{"name":`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)
	request.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	New(creator).ServeHTTP(recorder, request)

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

	if creator.calls != 0 {
		t.Fatalf(
			"expected project creator not to be called, got %d calls",
			creator.calls,
		)
	}
}

func TestCreateProjectRejectsEmptyName(t *testing.T) {
	creator := &recordingProjectCreator{
		err: project.ErrNameRequired,
	}
	requestBody := strings.NewReader(`{"name":""}`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)

	recorder := httptest.NewRecorder()
	New(creator).ServeHTTP(recorder, request)

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

	if creator.calls != 1 {
		t.Fatalf("expected project creator to be called once, got %d", creator.calls)
	}
}

func TestCreateProjectHandlesUnexpectedError(t *testing.T) {
	creator := &recordingProjectCreator{
		err: errors.New("database connection failed"),
	}
	requestBody := strings.NewReader(`{"name":"Commitarium"}`)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects",
		requestBody,
	)

	recorder := httptest.NewRecorder()
	New(creator).ServeHTTP(recorder, request)

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

	if strings.Contains(string(body), creator.err.Error()) {
		t.Fatal("response exposed the internal error")
	}
}
