package project

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	ErrInvalidAgentModels = errors.New("invalid agent models")
	exactModelIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// AgentModels stores the exact provider model ID selected for each durable
// workflow role. Empty pairs are accepted only for records created before
// explicit model selection was introduced.
type AgentModels struct {
	Lead     string
	Reviewer string
}

func (models AgentModels) Validate() error {
	lead := strings.TrimSpace(models.Lead)
	reviewer := strings.TrimSpace(models.Reviewer)
	if lead == "" && reviewer == "" {
		return nil
	}
	if lead == "" || reviewer == "" {
		return ErrInvalidAgentModels
	}
	if err := ValidateExactModelID(lead); err != nil {
		return err
	}
	return ValidateExactModelID(reviewer)
}

func (models AgentModels) ValidateRequired() error {
	if strings.TrimSpace(models.Lead) == "" || strings.TrimSpace(models.Reviewer) == "" {
		return ErrInvalidAgentModels
	}
	return models.Validate()
}

func (models AgentModels) Normalize() (AgentModels, error) {
	normalized := AgentModels{
		Lead:     strings.TrimSpace(models.Lead),
		Reviewer: strings.TrimSpace(models.Reviewer),
	}
	if err := normalized.Validate(); err != nil {
		return AgentModels{}, err
	}
	return normalized, nil
}

func ValidateExactModelID(value string) error {
	modelID := strings.TrimSpace(value)
	if !exactModelIDPattern.MatchString(modelID) {
		return fmt.Errorf("%w: model ID %q is not a safe explicit identifier", ErrInvalidAgentModels, value)
	}
	lower := strings.ToLower(modelID)
	switch lower {
	case "default", "latest", "best", "sonnet", "opus", "haiku", "fable", "opusplan":
		return fmt.Errorf("%w: model ID %q is a floating alias", ErrInvalidAgentModels, value)
	}
	if strings.HasSuffix(lower, "-latest") {
		return fmt.Errorf("%w: model ID %q is a floating alias", ErrInvalidAgentModels, value)
	}
	return nil
}
