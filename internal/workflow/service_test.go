package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

type recordingWorkflowStore struct {
	receivedTransition FeatureTransition
	transitionResult   Event
	transitionErr      error
	receivedAcceptance GoalAcceptance
	acceptanceResult   Event
	acceptanceErr      error
	receivedAggregate  string
	eventsResult       []Event
	eventsErr          error
	receivedArtifact   FeatureArtifactMutation
	artifactResult     FeatureArtifact
	artifactEvent      Event
	artifactErr        error
	currentArtifact    FeatureArtifact
	currentArtifactErr error
}

func (s *recordingWorkflowStore) PutFeatureArtifact(
	_ context.Context,
	mutation FeatureArtifactMutation,
) (FeatureArtifact, Event, error) {
	s.receivedArtifact = mutation
	return s.artifactResult, s.artifactEvent, s.artifactErr
}

func (s *recordingWorkflowStore) GetFeatureArtifact(
	_ context.Context,
	_ string,
	_ featureartifact.Kind,
) (FeatureArtifact, error) {
	return s.currentArtifact, s.currentArtifactErr
}

func (s *recordingWorkflowStore) AcceptGoal(
	_ context.Context,
	acceptance GoalAcceptance,
) (Event, error) {
	s.receivedAcceptance = acceptance
	return s.acceptanceResult, s.acceptanceErr
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

func TestServicePublishesHandoffBriefArtifactUpdate(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 20, 16, 0, 0, 0, time.UTC)
	event := validTestEvent(t)
	event.Type = EventTypeArtifactUpdated
	artifact := FeatureArtifact{
		FeatureID: "fea_test", Kind: featureartifact.KindHandoffBrief, Revision: 1,
		Document: `{"goal":"Ship it.","areas":[],"considerations":[],"open_questions":[],"base_commit_id":""}`,
		Actor:    Actor{Kind: ActorKindAgent, ID: "ses_lead"}, UpdatedAt: fixedTime,
	}
	store := &recordingWorkflowStore{artifactResult: artifact, artifactEvent: event}
	service := &Service{
		store: store, broker: newEventBroker(defaultSubscriberBuffer),
		generateID: func() string { return "evt_artifact" }, now: func() time.Time { return fixedTime },
	}
	events, cancel := service.SubscribeFeatureEvents("fea_test")
	defer cancel()
	actual, err := service.PutHandoffBrief(
		t.Context(), "fea_test", 0,
		featureartifact.HandoffBrief{Goal: "Ship it.", Areas: []string{}, Considerations: []string{}, OpenQuestions: []string{}},
		artifact.Actor, "goal-1",
	)
	if err != nil || actual != artifact {
		t.Fatalf("artifact=%+v error=%v", actual, err)
	}
	if store.receivedArtifact.ExpectedRevision != 0 || store.receivedArtifact.Kind != featureartifact.KindHandoffBrief ||
		store.receivedArtifact.IdempotencyKey != "goal-1" {
		t.Fatalf("mutation=%+v", store.receivedArtifact)
	}
	select {
	case published := <-events:
		if published != event {
			t.Fatalf("published=%+v", published)
		}
	default:
		t.Fatal("artifact update was not published")
	}
}

func TestServiceUpsertHandoffBriefReusesMatchingDurableRevision(t *testing.T) {
	draft := featureartifact.HandoffBrief{Goal: "Ship it.", Areas: []string{}, Considerations: []string{}, OpenQuestions: []string{}}
	current := FeatureArtifact{
		FeatureID: "fea_test", Kind: featureartifact.KindHandoffBrief, Revision: 2,
		Document: `{"goal":"Ship it.","areas":[],"considerations":[],"open_questions":[],"base_commit_id":""}`,
		Actor:    Actor{Kind: ActorKindAgent, ID: "ses_lead"}, UpdatedAt: time.Now().UTC(),
	}
	store := &recordingWorkflowStore{currentArtifact: current}
	service := NewService(store)

	actual, err := service.UpsertHandoffBrief(
		t.Context(), "fea_test", draft, current.Actor, "attempt-1:handoff-brief",
	)
	if err != nil {
		t.Fatalf("upsert matching handoff brief: %v", err)
	}
	if actual != current {
		t.Fatalf("expected current revision %+v, got %+v", current, actual)
	}
	if store.receivedArtifact.FeatureID != "" {
		t.Fatalf("matching retry wrote a new revision: %+v", store.receivedArtifact)
	}
}

func TestServiceTellsTheWatcherOfEveryEvent(t *testing.T) {
	expected := validTestEvent(t)
	service := NewService(&recordingWorkflowStore{transitionResult: expected})
	heard := 0
	service.OnFeatureEvent(func() { heard++ })
	if _, err := service.TransitionFeature(t.Context(), expected.AggregateID, feature.StatePlanning,
		expected.Actor, expected.IdempotencyKey); err != nil {
		t.Fatalf("transition feature: %v", err)
	}
	if heard != 1 {
		t.Fatalf("heard %d events, want 1", heard)
	}
}
