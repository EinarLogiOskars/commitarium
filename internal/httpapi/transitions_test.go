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
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

type recordingWorkflowService struct {
	featureID      string
	state          feature.State
	actor          workflow.Actor
	idempotencyKey string
	result         workflow.Event
	err            error
	calls          int
}

func (s *recordingWorkflowService) TransitionFeature(
	_ context.Context,
	featureID string,
	state feature.State,
	actor workflow.Actor,
	idempotencyKey string,
) (workflow.Event, error) {
	s.calls++
	s.featureID = featureID
	s.state = state
	s.actor = actor
	s.idempotencyKey = idempotencyKey
	return s.result, s.err
}

func TestTransitionFeature(t *testing.T) {
	payload, err := workflow.EncodeFeatureStateChangedPayload(
		feature.StateDraft,
		feature.StatePlanning,
	)
	if err != nil {
		t.Fatalf("encode test payload: %v", err)
	}
	fixedTime := time.Date(2026, time.September, 8, 18, 0, 0, 0, time.UTC)
	features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_test", ProjectID: "prj_test"}}
	workflows := &recordingWorkflowService{result: workflow.Event{
		ID:             "evt_test",
		AggregateID:    "fea_test",
		Type:           workflow.EventTypeFeatureStateChanged,
		OccurredAt:     fixedTime,
		Sequence:       1,
		PayloadVersion: workflow.FeatureStateChangedPayloadVersion,
		Payload:        payload,
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/projects/prj_test/features/fea_test/transitions",
		strings.NewReader(`{"state":"planning","actor_id":"impersonated"}`),
	)
	request.Header.Set("Idempotency-Key", "cmd_test")
	recorder := httptest.NewRecorder()

	New(nil, features, workflows).ServeHTTP(recorder, request)

	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}
	if workflows.featureID != "fea_test" || workflows.state != feature.StatePlanning {
		t.Errorf("unexpected transition target %q/%q", workflows.featureID, workflows.state)
	}
	expectedActor := workflow.Actor{Kind: workflow.ActorKindUser, ID: localUserID}
	if workflows.actor != expectedActor {
		t.Errorf("expected actor %+v, got %+v", expectedActor, workflows.actor)
	}
	if workflows.idempotencyKey != "cmd_test" {
		t.Errorf("expected idempotency key %q, got %q", "cmd_test", workflows.idempotencyKey)
	}

	var body transitionFeatureResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != feature.StatePlanning || body.EventID != "evt_test" || body.Sequence != 1 {
		t.Errorf("unexpected response %+v", body)
	}
}

func TestTransitionFeatureRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		key         string
		featureErr  error
		workflowErr error
		status      int
		code        string
	}{
		{name: "unknown state", body: `{"state":"unknown"}`, key: "cmd", status: 400, code: "invalid_feature_state"},
		{name: "missing key", body: `{"state":"planning"}`, status: 400, code: "idempotency_key_required"},
		{name: "wrong project", body: `{"state":"planning"}`, key: "cmd", featureErr: feature.ErrNotFound, status: 404, code: "feature_not_found"},
		{name: "invalid transition", body: `{"state":"planning"}`, key: "cmd", workflowErr: feature.ErrInvalidTransition, status: 409, code: "invalid_feature_transition"},
		{name: "key conflict", body: `{"state":"planning"}`, key: "cmd", workflowErr: workflow.ErrIdempotencyConflict, status: 409, code: "idempotency_conflict"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			features := &recordingFeatureService{getResult: feature.Feature{ID: "fea_test"}, getErr: test.featureErr}
			workflows := &recordingWorkflowService{err: test.workflowErr}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/prj_test/features/fea_test/transitions", strings.NewReader(test.body))
			request.Header.Set("Idempotency-Key", test.key)
			recorder := httptest.NewRecorder()
			New(nil, features, workflows).ServeHTTP(recorder, request)

			response := recorder.Result()
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("expected status %d, got %d", test.status, response.StatusCode)
			}
			var body errorResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body.Error.Code != test.code {
				t.Errorf("expected code %q, got %q", test.code, body.Error.Code)
			}
			if errors.Is(test.featureErr, feature.ErrNotFound) && workflows.calls != 0 {
				t.Fatal("expected workflow service not to be called")
			}
		})
	}
}
