package project

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrInvalidPhaseChoices = errors.New("invalid phase choices")

var effortPattern = regexp.MustCompile(`^[a-z]{1,16}$`)

// PhaseChoices are a work order's optional choices by phase (ADR-031): the
// model and effort each role plans with, and the effort it works with after
// the plan. An empty planning model or effort is the role's own; an empty
// role effort is the model's default. The agent never changes, so a planning model is one
// the role's agent offers.
type PhaseChoices struct {
	LeadPlanModel      string `json:"lead_plan_model,omitempty"`
	LeadPlanEffort     string `json:"lead_plan_effort,omitempty"`
	LeadEffort         string `json:"lead_effort,omitempty"`
	ReviewerPlanModel  string `json:"reviewer_plan_model,omitempty"`
	ReviewerPlanEffort string `json:"reviewer_plan_effort,omitempty"`
	ReviewerEffort     string `json:"reviewer_effort,omitempty"`
}

// Normalize trims the choices and checks them.
func (choices PhaseChoices) Normalize() (PhaseChoices, error) {
	fields := []*string{
		&choices.LeadPlanModel, &choices.LeadPlanEffort, &choices.LeadEffort,
		&choices.ReviewerPlanModel, &choices.ReviewerPlanEffort, &choices.ReviewerEffort,
	}
	for _, field := range fields {
		*field = strings.TrimSpace(*field)
	}
	for _, model := range []string{choices.LeadPlanModel, choices.ReviewerPlanModel} {
		if model != "" {
			if err := ValidateExactModelID(model); err != nil {
				return PhaseChoices{}, fmt.Errorf("%w: %w", ErrInvalidPhaseChoices, err)
			}
		}
	}
	for _, effort := range []string{choices.LeadPlanEffort, choices.LeadEffort, choices.ReviewerPlanEffort, choices.ReviewerEffort} {
		if effort != "" && !effortPattern.MatchString(effort) {
			return PhaseChoices{}, fmt.Errorf("%w: effort %q is not a reasoning effort", ErrInvalidPhaseChoices, effort)
		}
	}
	return choices, nil
}

// For returns the model and effort a role's turn runs with: while planning,
// its planning choices where set; otherwise its model and effort.
func (choices PhaseChoices) For(reviewer, planning bool, model string) (string, string) {
	planModel, planEffort, effort := choices.LeadPlanModel, choices.LeadPlanEffort, choices.LeadEffort
	if reviewer {
		planModel, planEffort, effort = choices.ReviewerPlanModel, choices.ReviewerPlanEffort, choices.ReviewerEffort
	}
	if !planning {
		return model, effort
	}
	if planModel != "" {
		model = planModel
	}
	if planEffort != "" {
		effort = planEffort
	}
	return model, effort
}

// WithoutPlanModel drops a role's planning model and effort, as when the user
// switches the role's model: the new model then runs every phase.
func (choices PhaseChoices) WithoutPlanModel(reviewer bool) PhaseChoices {
	if reviewer {
		choices.ReviewerPlanModel, choices.ReviewerPlanEffort = "", ""
	} else {
		choices.LeadPlanModel, choices.LeadPlanEffort = "", ""
	}
	return choices
}

// effortOrder ranks the reasoning efforts providers name, lowest first.
var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// EffortAbove reports whether effort is higher than ceiling. An effort not
// ranked counts as the highest, so it is never let past a ceiling by mistake;
// an empty effort (the model's default) or ceiling is never above.
func EffortAbove(effort, ceiling string) bool {
	if effort == "" || ceiling == "" {
		return false
	}
	rank := func(value string) int {
		for index, known := range effortOrder {
			if known == value {
				return index
			}
		}
		return len(effortOrder)
	}
	return rank(effort) > rank(ceiling)
}

// Efforts lists the choices' efforts.
func (choices PhaseChoices) Efforts() []string {
	return []string{choices.LeadPlanEffort, choices.LeadEffort, choices.ReviewerPlanEffort, choices.ReviewerEffort}
}

// ValidEffortCeiling reports whether value can cap the assistant's efforts.
func ValidEffortCeiling(value string) bool {
	for _, known := range effortOrder[2:] {
		if known == value {
			return true
		}
	}
	return false
}
