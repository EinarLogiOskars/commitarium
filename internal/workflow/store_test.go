package workflow

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
)

func TestFeatureTransitionValidate(t *testing.T) {
	transition := validFeatureTransition()

	if err := transition.Validate(); err != nil {
		t.Fatalf("validate transition: %v", err)
	}
}

func TestFeatureTransitionValidateRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*FeatureTransition)
	}{
		{name: "missing event ID", mutate: func(value *FeatureTransition) { value.EventID = "" }},
		{name: "missing feature ID", mutate: func(value *FeatureTransition) { value.FeatureID = "" }},
		{name: "unknown state", mutate: func(value *FeatureTransition) { value.State = "unknown" }},
		{
			name: "unknown actor kind",
			mutate: func(value *FeatureTransition) {
				value.Actor.Kind = "unknown"
			},
		},
		{name: "missing actor ID", mutate: func(value *FeatureTransition) { value.Actor.ID = "" }},
		{
			name: "missing occurrence time",
			mutate: func(value *FeatureTransition) {
				value.OccurredAt = time.Time{}
			},
		},
		{
			name: "missing idempotency key",
			mutate: func(value *FeatureTransition) {
				value.IdempotencyKey = ""
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transition := validFeatureTransition()
			test.mutate(&transition)

			err := transition.Validate()
			if !errors.Is(err, ErrInvalidTransitionRequest) {
				t.Fatalf("expected error %v, got %v", ErrInvalidTransitionRequest, err)
			}
		})
	}
}

func validFeatureTransition() FeatureTransition {
	return FeatureTransition{
		EventID:   "evt_test",
		FeatureID: "fea_test",
		State:     feature.StatePlanning,
		Actor: Actor{
			Kind: ActorKindUser,
			ID:   "usr_test",
		},
		OccurredAt:     time.Date(2026, time.September, 8, 16, 0, 0, 0, time.UTC),
		IdempotencyKey: "cmd_test",
	}
}

func TestGoalAcceptanceValidate(t *testing.T) {
	acceptance := validGoalAcceptance()
	if err := acceptance.Validate(); err != nil {
		t.Fatalf("validate goal acceptance: %v", err)
	}
}

func TestGoalAcceptanceValidateRejectsInvalidRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GoalAcceptance)
	}{
		{name: "missing event ID", mutate: func(value *GoalAcceptance) { value.EventID = "" }},
		{name: "missing feature ID", mutate: func(value *GoalAcceptance) { value.FeatureID = "" }},
		{name: "missing session ID", mutate: func(value *GoalAcceptance) { value.SessionID = "" }},
		{name: "blank goal", mutate: func(value *GoalAcceptance) { value.Goal = " " }},
		{name: "untrimmed goal", mutate: func(value *GoalAcceptance) { value.Goal = " Ship it" }},
		{name: "unknown actor kind", mutate: func(value *GoalAcceptance) { value.Actor.Kind = "unknown" }},
		{name: "missing actor ID", mutate: func(value *GoalAcceptance) { value.Actor.ID = "" }},
		{name: "missing occurrence time", mutate: func(value *GoalAcceptance) { value.OccurredAt = time.Time{} }},
		{name: "missing idempotency key", mutate: func(value *GoalAcceptance) { value.IdempotencyKey = "" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acceptance := validGoalAcceptance()
			test.mutate(&acceptance)
			if err := acceptance.Validate(); !errors.Is(err, ErrInvalidGoalAcceptance) {
				t.Fatalf("expected error %v, got %v", ErrInvalidGoalAcceptance, err)
			}
		})
	}
}

func validGoalAcceptance() GoalAcceptance {
	return GoalAcceptance{
		EventID: "evt_goal", FeatureID: "fea_test", SessionID: "ses_lead",
		Goal: "Ship CSV export.", Actor: Actor{Kind: ActorKindUser, ID: "usr_test"},
		OccurredAt:     time.Date(2026, time.September, 9, 16, 0, 0, 0, time.UTC),
		IdempotencyKey: "accept-1",
	}
}
