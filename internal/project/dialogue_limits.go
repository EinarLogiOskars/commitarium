package project

import (
	"errors"
	"fmt"
)

const DefaultDialogueRoundLimit = 6

type DialogueLimits struct {
	PlanningRounds             int
	ImplementationReviewRounds int
}

var ErrInvalidDialogueLimits = errors.New("invalid project dialogue limits")

func DefaultDialogueLimits() DialogueLimits {
	return DialogueLimits{
		PlanningRounds:             DefaultDialogueRoundLimit,
		ImplementationReviewRounds: DefaultDialogueRoundLimit,
	}
}

func (limits DialogueLimits) Validate() error {
	switch {
	case limits.PlanningRounds < 0:
		return fmt.Errorf("%w: planning rounds cannot be negative", ErrInvalidDialogueLimits)
	case limits.ImplementationReviewRounds < 0:
		return fmt.Errorf("%w: implementation review rounds cannot be negative", ErrInvalidDialogueLimits)
	default:
		return nil
	}
}
