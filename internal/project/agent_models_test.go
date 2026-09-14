package project

import (
	"errors"
	"testing"
)

func TestAgentModelsRequireCompleteExactIDs(t *testing.T) {
	if err := (AgentModels{Lead: "gpt-5.6-sol", Reviewer: "claude-sonnet-4-20250514"}).ValidateRequired(); err != nil {
		t.Fatalf("validate exact models: %v", err)
	}
	for _, models := range []AgentModels{
		{Lead: "gpt-5.6-sol"},
		{Lead: "latest", Reviewer: "claude-sonnet-4-20250514"},
		{Lead: "gpt-5.6-sol", Reviewer: "claude-latest"},
	} {
		if err := models.ValidateRequired(); !errors.Is(err, ErrInvalidAgentModels) {
			t.Fatalf("models %#v error = %v", models, err)
		}
	}
}
