package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type recordingFeatureService struct {
	createCalls         int
	receivedProjectID   string
	receivedTitle       string
	receivedDescription string
	createResult        feature.Feature
	createErr           error

	receivedGetProjectID string
	receivedFeatureID    string
	getResult            feature.Feature
	getErr               error
}

func (s *recordingFeatureService) Create(
	_ context.Context,
	projectID string,
	title string,
	description string,
) (feature.Feature, error) {
	s.createCalls++
	s.receivedProjectID = projectID
	s.receivedTitle = title
	s.receivedDescription = description
	return s.createResult, s.createErr
}

func (s *recordingFeatureService) GetByID(
	_ context.Context,
	projectID string,
	id string,
) (feature.Feature, error) {
	s.receivedGetProjectID = projectID
	s.receivedFeatureID = id
	return s.getResult, s.getErr
}

func TestCreateFeature(t *testing.T) {
	createdAt := time.Date(
		2026,
		time.September,
		8,
		13,
		30,
		0,
		123456789,
		time.UTC,
	)
	expected := feature.Feature{
		ID:          "fea_test",
		ProjectID:   "prj_test",
		Title:       "Persist workflow events",
		Description: "Store each transition atomically.",
		State:       feature.StateDraft,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
	features := &recordingFeatureService{createResult: expected}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/prj_test/features",
		strings.NewReader(`{
			"title":"Persist workflow events",
			"description":"Store each transition atomically."
		}`),
	)

	recorder := httptest.NewRecorder()
	New(nil, features, nil, nil, nil).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		t.Fatalf(
			"expected status code %d, got %d",
			http.StatusCreated,
			response.StatusCode,
		)
	}

	expectedLocation := "/api/v1/projects/prj_test/features/fea_test"
	if location := response.Header.Get("Location"); location != expectedLocation {
		t.Errorf("expected Location header %q, got %q", expectedLocation, location)
	}

	if contentType := response.Header.Get("Content-Type"); contentType != "application/json" {
		t.Errorf("expected application/json, got %q", contentType)
	}

	if features.createCalls != 1 {
		t.Errorf("expected feature service to be called once, got %d", features.createCalls)
	}
	if features.receivedProjectID != expected.ProjectID {
		t.Errorf(
			"expected project ID %q, got %q",
			expected.ProjectID,
			features.receivedProjectID,
		)
	}
	if features.receivedTitle != expected.Title {
		t.Errorf("expected title %q, got %q", expected.Title, features.receivedTitle)
	}
	if features.receivedDescription != expected.Description {
		t.Errorf(
			"expected description %q, got %q",
			expected.Description,
			features.receivedDescription,
		)
	}

	var body featureResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode feature response: %v", err)
	}

	assertFeatureResponse(t, body, expected)
}

func TestCreateFeatureRejectsMalformedJSON(t *testing.T) {
	features := &recordingFeatureService{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/prj_test/features",
		strings.NewReader(`{"title":`),
	)

	recorder := httptest.NewRecorder()
	New(nil, features, nil, nil, nil).ServeHTTP(recorder, request)

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

	if body.Error.Code != "invalid_json" {
		t.Errorf("expected error code %q, got %q", "invalid_json", body.Error.Code)
	}
	if features.createCalls != 0 {
		t.Errorf("expected feature service not to be called, got %d calls", features.createCalls)
	}
}

func TestCreateFeatureMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name            string
		serviceErr      error
		expectedStatus  int
		expectedCode    string
		expectedMessage string
	}{
		{
			name:            "missing title",
			serviceErr:      feature.ErrTitleRequired,
			expectedStatus:  http.StatusBadRequest,
			expectedCode:    "feature_title_required",
			expectedMessage: "feature title is required",
		},
		{
			name:            "unknown project",
			serviceErr:      project.ErrNotFound,
			expectedStatus:  http.StatusNotFound,
			expectedCode:    "project_not_found",
			expectedMessage: "project not found",
		},
		{
			name:            "unexpected error",
			serviceErr:      errors.New("database connection failed"),
			expectedStatus:  http.StatusInternalServerError,
			expectedCode:    "internal_error",
			expectedMessage: "internal server error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			features := &recordingFeatureService{createErr: test.serviceErr}
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/projects/prj_test/features",
				strings.NewReader(`{"title":"Test feature"}`),
			)

			recorder := httptest.NewRecorder()
			New(nil, features, nil, nil, nil).ServeHTTP(recorder, request)

			response := recorder.Result()
			defer response.Body.Close()

			if response.StatusCode != test.expectedStatus {
				t.Fatalf(
					"expected status code %d, got %d",
					test.expectedStatus,
					response.StatusCode,
				)
			}

			var body errorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}

			if body.Error.Code != test.expectedCode {
				t.Errorf("expected error code %q, got %q", test.expectedCode, body.Error.Code)
			}
			if body.Error.Message != test.expectedMessage {
				t.Errorf(
					"expected error message %q, got %q",
					test.expectedMessage,
					body.Error.Message,
				)
			}
			if body.Error.Message == test.serviceErr.Error() && test.expectedStatus == http.StatusInternalServerError {
				t.Fatal("response exposed the internal error")
			}
		})
	}
}

func TestGetFeatureByID(t *testing.T) {
	createdAt := time.Date(2026, time.September, 8, 14, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(15 * time.Minute)
	expected := feature.Feature{
		ID:          "fea_test",
		ProjectID:   "prj_test",
		Title:       "Persist workflow events",
		Description: "Store each transition atomically.",
		State:       feature.StateDraft,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
	features := &recordingFeatureService{getResult: expected}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/projects/prj_test/features/fea_test",
		nil,
	)

	recorder := httptest.NewRecorder()
	New(nil, features, nil, nil, nil).ServeHTTP(recorder, request)

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
	if features.receivedGetProjectID != expected.ProjectID {
		t.Errorf(
			"expected project ID %q, got %q",
			expected.ProjectID,
			features.receivedGetProjectID,
		)
	}
	if features.receivedFeatureID != expected.ID {
		t.Errorf(
			"expected feature ID %q, got %q",
			expected.ID,
			features.receivedFeatureID,
		)
	}

	var body featureResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode feature response: %v", err)
	}

	assertFeatureResponse(t, body, expected)
}

func TestGetFeatureByIDMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name           string
		serviceErr     error
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "missing feature",
			serviceErr:     feature.ErrNotFound,
			expectedStatus: http.StatusNotFound,
			expectedCode:   "feature_not_found",
		},
		{
			name:           "unexpected error",
			serviceErr:     errors.New("database connection failed"),
			expectedStatus: http.StatusInternalServerError,
			expectedCode:   "internal_error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			features := &recordingFeatureService{getErr: test.serviceErr}
			request := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/projects/prj_test/features/fea_test",
				nil,
			)

			recorder := httptest.NewRecorder()
			New(nil, features, nil, nil, nil).ServeHTTP(recorder, request)

			response := recorder.Result()
			defer response.Body.Close()

			if response.StatusCode != test.expectedStatus {
				t.Fatalf(
					"expected status code %d, got %d",
					test.expectedStatus,
					response.StatusCode,
				)
			}

			var body errorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}

			if body.Error.Code != test.expectedCode {
				t.Errorf("expected error code %q, got %q", test.expectedCode, body.Error.Code)
			}
			if body.Error.Message == test.serviceErr.Error() && test.expectedStatus == http.StatusInternalServerError {
				t.Fatal("response exposed the internal error")
			}
		})
	}
}

func assertFeatureResponse(
	t *testing.T,
	actual featureResponse,
	expected feature.Feature,
) {
	t.Helper()

	if actual.ID != expected.ID {
		t.Errorf("expected ID %q, got %q", expected.ID, actual.ID)
	}
	if actual.ProjectID != expected.ProjectID {
		t.Errorf("expected project ID %q, got %q", expected.ProjectID, actual.ProjectID)
	}
	if actual.Title != expected.Title {
		t.Errorf("expected title %q, got %q", expected.Title, actual.Title)
	}
	if actual.Description != expected.Description {
		t.Errorf("expected description %q, got %q", expected.Description, actual.Description)
	}
	if actual.State != expected.State {
		t.Errorf("expected state %q, got %q", expected.State, actual.State)
	}
	if actual.CreatedAt != expected.CreatedAt {
		t.Errorf("expected creation time %v, got %v", expected.CreatedAt, actual.CreatedAt)
	}
	if actual.UpdatedAt != expected.UpdatedAt {
		t.Errorf("expected update time %v, got %v", expected.UpdatedAt, actual.UpdatedAt)
	}
}
