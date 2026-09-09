package workflow

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

type Service struct {
	store      Store
	broker     *eventBroker
	generateID func() string
	now        func() time.Time
}

func (s *Service) AcceptGoal(
	ctx context.Context,
	featureID string,
	sessionID string,
	goal string,
	actor Actor,
	idempotencyKey string,
) (Event, error) {
	event, err := s.store.AcceptGoal(ctx, GoalAcceptance{
		EventID: s.generateID(), FeatureID: featureID, SessionID: sessionID,
		Goal: strings.TrimSpace(goal), Actor: actor,
		OccurredAt: s.now().UTC(), IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return Event{}, fmt.Errorf("accept goal for feature %q: %w", featureID, err)
	}
	if s.broker != nil {
		s.broker.publish(event)
	}
	return event, nil
}

func NewService(store Store) *Service {
	return &Service{
		store:  store,
		broker: newEventBroker(defaultSubscriberBuffer),
		generateID: func() string {
			return "evt_" + rand.Text()
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) TransitionFeature(
	ctx context.Context,
	featureID string,
	state feature.State,
	actor Actor,
	idempotencyKey string,
) (Event, error) {
	event, err := s.store.ApplyFeatureTransition(
		ctx,
		FeatureTransition{
			EventID:        s.generateID(),
			FeatureID:      featureID,
			State:          state,
			Actor:          actor,
			OccurredAt:     s.now().UTC(),
			IdempotencyKey: idempotencyKey,
		},
	)
	if err != nil {
		return Event{}, fmt.Errorf(
			"transition feature %q to %q: %w",
			featureID,
			state,
			err,
		)
	}
	if s.broker != nil {
		s.broker.publish(event)
	}
	return event, nil
}

func (s *Service) EventsForFeature(
	ctx context.Context,
	featureID string,
) ([]Event, error) {
	events, err := s.store.ListEvents(ctx, featureID)
	if err != nil {
		return nil, fmt.Errorf("list events for feature %q: %w", featureID, err)
	}
	return events, nil
}

func (s *Service) SubscribeFeatureEvents(
	featureID string,
) (<-chan Event, func()) {
	return s.broker.subscribe(featureID)
}
