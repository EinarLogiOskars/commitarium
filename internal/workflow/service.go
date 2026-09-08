package workflow

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

type Service struct {
	store      Store
	generateID func() string
	now        func() time.Time
}

func NewService(store Store) *Service {
	return &Service{
		store: store,
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
