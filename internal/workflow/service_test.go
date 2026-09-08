package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

type recordingWorkflowStore struct {
	receivedTransition FeatureTransition
	transitionResult   Event
	transitionErr      error
	receivedAggregate  string
	eventsResult       []Event
	eventsErr          error
}

func (s *recordingWorkflowStore) ApplyFeatureTransition(
	_ context.Context,
	transition FeatureTransition,
) (Event, error) {
	s.receivedTransition = transition
	return s.transitionResult, s.transitionErr
}

func (s *recordingWorkflowStore) ListEvents(
	_ context.Context,
	aggregateID string,
) ([]Event, error) {
	s.receivedAggregate = aggregateID
	return s.eventsResult, s.eventsErr
}

func TestServiceTransitionFeature(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 8, 17, 0, 0, 123456789, time.UTC)
	expectedEvent := validTestEvent(t)
	store := &recordingWorkflowStore{transitionResult: expectedEvent}
	service := &Service{
		store: store,
		generateID: func() string {
			return "evt_generated"
		},
		now: func() time.Time {
			return fixedTime
		},
	}
	actor := Actor{Kind: ActorKindAgent, ID: "agt_coder"}

	actual, err := service.TransitionFeature(
		t.Context(),
		"fea_test",
		feature.StatePlanning,
		actor,
		"cmd_test",
	)
	if err != nil {
		t.Fatalf("transition feature: %v", err)
	}
	if actual != expectedEvent {
		t.Errorf("expected event %+v, got %+v", expectedEvent, actual)
	}

	expectedTransition := FeatureTransition{
		EventID:        "evt_generated",
		FeatureID:      "fea_test",
		State:          feature.StatePlanning,
		Actor:          actor,
		OccurredAt:     fixedTime,
		IdempotencyKey: "cmd_test",
	}
	if store.receivedTransition != expectedTransition {
		t.Errorf(
			"expected transition %+v, got %+v",
			expectedTransition,
			store.receivedTransition,
		)
	}
}

func TestServiceTransitionFeatureWrapsStoreError(t *testing.T) {
	storeErr := errors.New("storage failed")
	service := NewService(&recordingWorkflowStore{transitionErr: storeErr})

	_, err := service.TransitionFeature(
		t.Context(),
		"fea_test",
		feature.StatePlanning,
		Actor{Kind: ActorKindUser, ID: "usr_test"},
		"cmd_test",
	)
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected error %v, got %v", storeErr, err)
	}
}

func TestServiceEventsForFeature(t *testing.T) {
	expected := []Event{validTestEvent(t)}
	store := &recordingWorkflowStore{eventsResult: expected}
	service := NewService(store)

	actual, err := service.EventsForFeature(t.Context(), "fea_test")
	if err != nil {
		t.Fatalf("list feature events: %v", err)
	}
	if store.receivedAggregate != "fea_test" {
		t.Errorf("expected feature ID %q, got %q", "fea_test", store.receivedAggregate)
	}
	if len(actual) != 1 || actual[0] != expected[0] {
		t.Errorf("expected events %+v, got %+v", expected, actual)
	}
}

func TestServicePublishesSuccessfulTransition(t *testing.T) {
	expected := validTestEvent(t)
	service := NewService(&recordingWorkflowStore{transitionResult: expected})
	events, cancel := service.SubscribeFeatureEvents(expected.AggregateID)
	defer cancel()

	if _, err := service.TransitionFeature(
		t.Context(),
		expected.AggregateID,
		feature.StatePlanning,
		expected.Actor,
		expected.IdempotencyKey,
	); err != nil {
		t.Fatalf("transition feature: %v", err)
	}

	select {
	case actual := <-events:
		if actual != expected {
			t.Errorf("expected event %+v, got %+v", expected, actual)
		}
	default:
		t.Fatal("expected transition event to be published")
	}
}
