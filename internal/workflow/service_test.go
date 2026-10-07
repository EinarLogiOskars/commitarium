package workflow

import (
	"context"
	"encoding/json"
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

func TestServiceTransitionsAcceptanceTestAgainstPinnedImplementation(t *testing.T) {
	currentDocument, err := json.Marshal(featureartifact.AcceptanceTests{
		PlanVersion: 1, TestCommitID: "0123456789abcdef0123456789abcdef01234567",
		ImplementationCommitID: "abcdef0123456789abcdef0123456789abcdef01",
		Tests: []featureartifact.AcceptanceTest{{
			ID: "exports-csv", Position: 1, Title: "Exports visible rows",
			Status: featureartifact.AcceptanceTestPending, Note: "",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &recordingWorkflowStore{
		currentArtifact: FeatureArtifact{
			FeatureID: "fea_test", Kind: featureartifact.KindAcceptanceTests,
			Revision: 2, Document: string(currentDocument),
			Actor: Actor{Kind: ActorKindAgent, ID: "reviewer"}, UpdatedAt: time.Now().UTC(),
		},
		artifactResult: FeatureArtifact{FeatureID: "fea_test", Kind: featureartifact.KindAcceptanceTests, Revision: 3, Document: string(currentDocument), Actor: Actor{Kind: ActorKindAgent, ID: "reviewer"}, UpdatedAt: time.Now().UTC()},
		artifactEvent:  validTestEvent(t),
	}
	service := NewService(store)
	_, err = service.TransitionAcceptanceTest(
		t.Context(), "fea_test", 1, "exports-csv", featureartifact.AcceptanceTestRunning,
		"", "abcdef0123456789abcdef0123456789abcdef01",
		Actor{Kind: ActorKindAgent, ID: "reviewer"}, "acceptance-start",
	)
	if err != nil {
		t.Fatalf("start acceptance test: %v", err)
	}
	updated := featureartifact.AcceptanceTests{}
	if err := json.Unmarshal([]byte(store.receivedArtifact.Document), &updated); err != nil {
		t.Fatalf("decode updated artifact: %v", err)
	}
	if store.receivedArtifact.ExpectedRevision != 2 || updated.Tests[0].Status != featureartifact.AcceptanceTestRunning {
		t.Fatalf("mutation=%+v tests=%+v", store.receivedArtifact, updated)
	}
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

func TestServiceAcceptGoal(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 9, 14, 0, 0, 123456789, time.UTC)
	expectedEvent := validTestEvent(t)
	expectedEvent.Type = EventTypeGoalAccepted
	store := &recordingWorkflowStore{acceptanceResult: expectedEvent}
	service := &Service{
		store:      store,
		broker:     newEventBroker(defaultSubscriberBuffer),
		generateID: func() string { return "evt_goal" },
		now:        func() time.Time { return fixedTime },
	}
	actor := Actor{Kind: ActorKindUser, ID: "local-user"}
	events, cancel := service.SubscribeFeatureEvents("fea_test")
	defer cancel()

	actual, err := service.AcceptGoal(
		t.Context(), "fea_test", "ses_lead", "  Ship CSV export.  ", actor, "accept-1",
	)
	if err != nil {
		t.Fatalf("accept goal: %v", err)
	}
	if actual != expectedEvent {
		t.Fatalf("expected event %+v, got %+v", expectedEvent, actual)
	}
	expected := GoalAcceptance{
		EventID: "evt_goal", FeatureID: "fea_test", SessionID: "ses_lead",
		Goal: "Ship CSV export.", Actor: actor, OccurredAt: fixedTime,
		IdempotencyKey: "accept-1",
	}
	if store.receivedAcceptance != expected {
		t.Fatalf("expected acceptance %+v, got %+v", expected, store.receivedAcceptance)
	}
	select {
	case published := <-events:
		if published != expectedEvent {
			t.Fatalf("expected published event %+v, got %+v", expectedEvent, published)
		}
	default:
		t.Fatal("expected accepted-goal event to be published")
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

func TestServicePublishesGoalDraftArtifactUpdate(t *testing.T) {
	fixedTime := time.Date(2026, time.September, 20, 16, 0, 0, 0, time.UTC)
	event := validTestEvent(t)
	event.Type = EventTypeArtifactUpdated
	artifact := FeatureArtifact{
		FeatureID: "fea_test", Kind: featureartifact.KindGoalDraft, Revision: 1,
		Document: `{"goal":"Ship it.","open_questions":[]}`,
		Actor:    Actor{Kind: ActorKindAgent, ID: "ses_lead"}, UpdatedAt: fixedTime,
	}
	store := &recordingWorkflowStore{artifactResult: artifact, artifactEvent: event}
	service := &Service{
		store: store, broker: newEventBroker(defaultSubscriberBuffer),
		generateID: func() string { return "evt_artifact" }, now: func() time.Time { return fixedTime },
	}
	events, cancel := service.SubscribeFeatureEvents("fea_test")
	defer cancel()
	actual, err := service.PutGoalDraft(
		t.Context(), "fea_test", 0,
		featureartifact.GoalDraft{Goal: "Ship it.", OpenQuestions: []string{}},
		artifact.Actor, "goal-1",
	)
	if err != nil || actual != artifact {
		t.Fatalf("artifact=%+v error=%v", actual, err)
	}
	if store.receivedArtifact.ExpectedRevision != 0 || store.receivedArtifact.Kind != featureartifact.KindGoalDraft ||
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

func TestServiceUpsertGoalDraftReusesMatchingDurableRevision(t *testing.T) {
	draft := featureartifact.GoalDraft{Goal: "Ship it.", OpenQuestions: []string{}}
	current := FeatureArtifact{
		FeatureID: "fea_test", Kind: featureartifact.KindGoalDraft, Revision: 2,
		Document: `{"goal":"Ship it.","open_questions":[]}`,
		Actor:    Actor{Kind: ActorKindAgent, ID: "ses_lead"}, UpdatedAt: time.Now().UTC(),
	}
	store := &recordingWorkflowStore{currentArtifact: current}
	service := NewService(store)

	actual, err := service.UpsertGoalDraft(
		t.Context(), "fea_test", draft, current.Actor, "attempt-1:goal-draft",
	)
	if err != nil {
		t.Fatalf("upsert matching goal draft: %v", err)
	}
	if actual != current {
		t.Fatalf("expected current revision %+v, got %+v", current, actual)
	}
	if store.receivedArtifact.FeatureID != "" {
		t.Fatalf("matching retry wrote a new revision: %+v", store.receivedArtifact)
	}
}
