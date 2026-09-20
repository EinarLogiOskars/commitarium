package workflow

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
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

func (s *Service) GetFeatureArtifact(
	ctx context.Context,
	featureID string,
	kind featureartifact.Kind,
) (FeatureArtifact, error) {
	store, ok := s.store.(ArtifactStore)
	if !ok {
		return FeatureArtifact{}, ErrArtifactNotFound
	}
	return store.GetFeatureArtifact(ctx, featureID, kind)
}

func (s *Service) PutGoalDraft(
	ctx context.Context,
	featureID string,
	expectedRevision int,
	draft featureartifact.GoalDraft,
	actor Actor,
	idempotencyKey string,
) (FeatureArtifact, error) {
	if err := draft.Validate(); err != nil {
		return FeatureArtifact{}, err
	}
	return s.putFeatureArtifact(ctx, featureID, featureartifact.KindGoalDraft, expectedRevision, draft, actor, idempotencyKey)
}

func (s *Service) UpsertGoalDraft(
	ctx context.Context,
	featureID string,
	draft featureartifact.GoalDraft,
	actor Actor,
	idempotencyKey string,
) (FeatureArtifact, error) {
	expected := 0
	current, err := s.GetFeatureArtifact(ctx, featureID, featureartifact.KindGoalDraft)
	if err == nil {
		matches, compareErr := artifactDocumentMatches(current, draft)
		if compareErr != nil {
			return FeatureArtifact{}, compareErr
		}
		if matches {
			return current, nil
		}
		expected = current.Revision
	} else if !errors.Is(err, ErrArtifactNotFound) {
		return FeatureArtifact{}, err
	}
	return s.PutGoalDraft(ctx, featureID, expected, draft, actor, idempotencyKey)
}

func (s *Service) PutImplementationPlan(
	ctx context.Context,
	featureID string,
	expectedRevision int,
	plan featureartifact.ImplementationPlan,
	actor Actor,
	idempotencyKey string,
) (FeatureArtifact, error) {
	if err := plan.Validate(); err != nil {
		return FeatureArtifact{}, err
	}
	return s.putFeatureArtifact(ctx, featureID, featureartifact.KindImplementationPlan, expectedRevision, plan, actor, idempotencyKey)
}

func (s *Service) UpsertImplementationPlan(
	ctx context.Context,
	featureID string,
	plan featureartifact.ImplementationPlan,
	actor Actor,
	idempotencyKey string,
) (FeatureArtifact, error) {
	expected := 0
	current, err := s.GetFeatureArtifact(ctx, featureID, featureartifact.KindImplementationPlan)
	if err == nil {
		matches, compareErr := artifactDocumentMatches(current, plan)
		if compareErr != nil {
			return FeatureArtifact{}, compareErr
		}
		if matches {
			return current, nil
		}
		expected = current.Revision
	} else if !errors.Is(err, ErrArtifactNotFound) {
		return FeatureArtifact{}, err
	}
	return s.PutImplementationPlan(ctx, featureID, expected, plan, actor, idempotencyKey)
}

func artifactDocumentMatches(current FeatureArtifact, document any) (bool, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return false, fmt.Errorf("encode feature artifact for comparison: %w", err)
	}
	return current.Document == string(encoded), nil
}

func (s *Service) TransitionImplementationPlanStep(
	ctx context.Context,
	featureID string,
	planVersion int,
	stepID string,
	status featureartifact.StepStatus,
	commitID string,
	actor Actor,
	idempotencyKey string,
) (FeatureArtifact, error) {
	current, err := s.GetFeatureArtifact(ctx, featureID, featureartifact.KindImplementationPlan)
	if err != nil {
		return FeatureArtifact{}, err
	}
	plan := featureartifact.ImplementationPlan{}
	if err := json.Unmarshal([]byte(current.Document), &plan); err != nil {
		return FeatureArtifact{}, fmt.Errorf("decode implementation plan: %w", err)
	}
	if plan.PlanVersion != planVersion {
		return FeatureArtifact{}, ErrArtifactConflict
	}
	stepIndex := -1
	for index := range plan.Steps {
		if plan.Steps[index].ID == stepID {
			stepIndex = index
			break
		}
	}
	if stepIndex < 0 {
		return FeatureArtifact{}, featureartifact.ErrInvalidArtifact
	}
	step := &plan.Steps[stepIndex]
	commitID = strings.TrimSpace(commitID)
	switch status {
	case featureartifact.StepInProgress:
		if step.Status == featureartifact.StepInProgress {
			return current, nil
		}
		if step.Status != featureartifact.StepPending || commitID != "" {
			return FeatureArtifact{}, featureartifact.ErrInvalidArtifact
		}
		for index := 0; index < stepIndex; index++ {
			if plan.Steps[index].Status != featureartifact.StepCompleted {
				return FeatureArtifact{}, featureartifact.ErrInvalidArtifact
			}
		}
		for index := range plan.Steps {
			if plan.Steps[index].Status == featureartifact.StepInProgress {
				return FeatureArtifact{}, featureartifact.ErrInvalidArtifact
			}
		}
		step.Status = featureartifact.StepInProgress
	case featureartifact.StepCompleted:
		if step.Status == featureartifact.StepCompleted && step.CommitID == commitID {
			return current, nil
		}
		if step.Status != featureartifact.StepInProgress || commitID == "" {
			return FeatureArtifact{}, featureartifact.ErrInvalidArtifact
		}
		now := s.now().UTC()
		step.Status = featureartifact.StepCompleted
		step.CommitID = commitID
		step.CompletedAt = &now
	default:
		return FeatureArtifact{}, featureartifact.ErrInvalidArtifact
	}
	if err := plan.Validate(); err != nil {
		return FeatureArtifact{}, err
	}
	return s.PutImplementationPlan(ctx, featureID, current.Revision, plan, actor, idempotencyKey)
}

func (s *Service) putFeatureArtifact(
	ctx context.Context,
	featureID string,
	kind featureartifact.Kind,
	expectedRevision int,
	document any,
	actor Actor,
	idempotencyKey string,
) (FeatureArtifact, error) {
	store, ok := s.store.(ArtifactStore)
	if !ok {
		return FeatureArtifact{}, ErrArtifactNotFound
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return FeatureArtifact{}, fmt.Errorf("encode feature artifact: %w", err)
	}
	artifact, event, err := store.PutFeatureArtifact(ctx, FeatureArtifactMutation{
		EventID: s.generateID(), FeatureID: featureID, Kind: kind,
		ExpectedRevision: expectedRevision, Document: string(encoded),
		DocumentDigest: DigestArtifactDocument(string(encoded)), Actor: actor,
		OccurredAt: s.now().UTC(), IdempotencyKey: strings.TrimSpace(idempotencyKey),
	})
	if err != nil {
		return FeatureArtifact{}, fmt.Errorf("update %s artifact for feature %q: %w", kind, featureID, err)
	}
	if s.broker != nil {
		s.broker.publish(event)
	}
	return artifact, nil
}
