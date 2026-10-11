package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

func TestAgentStoreSeedsCreatesRenamesAndRemoves(t *testing.T) {
	db, _ := newTestExecutionStore(t)
	service := agent.NewService(NewAgentStore(db))

	seeded, err := service.List(t.Context())
	if err != nil || len(seeded) != 2 || seeded[0].Provider == seeded[1].Provider {
		t.Fatalf("seeded agents=%+v err=%v", seeded, err)
	}
	first, err := service.Create(t.Context(), "Claude Max", project.AgentProviderClaude)
	if err != nil || first.ID != "claude-max" {
		t.Fatalf("create: agent=%+v err=%v", first, err)
	}
	second, err := service.Create(t.Context(), "Claude Max", project.AgentProviderClaude)
	if err != nil || second.ID != "claude-max-2" {
		t.Fatalf("create a second account with the same name: agent=%+v err=%v", second, err)
	}
	renamed, err := service.Rename(t.Context(), second.ID, "Claude Max (work)")
	if err != nil || renamed.Name != "Claude Max (work)" || renamed.ID != second.ID {
		t.Fatalf("rename: agent=%+v err=%v", renamed, err)
	}
	if err := service.Delete(t.Context(), second.ID); err != nil {
		t.Fatalf("delete an unused agent: %v", err)
	}
	if _, err := service.Get(t.Context(), second.ID); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("deleted agent is still there: %v", err)
	}
	// createExecutionRecords' project defaults point at the codex agent.
	run, _ := createExecutionRecords(t, db, NewExecutionStore(db))
	if err := service.Delete(t.Context(), "codex"); !errors.Is(err, agent.ErrInUse) {
		t.Fatalf("delete an agent in use: %v", err)
	}
	// A work order that has not started yet keeps its chosen agent too.
	var projectID string
	if err := db.QueryRowContext(t.Context(), `SELECT project_id FROM features WHERE id = ?`, run.FeatureID).Scan(&projectID); err != nil {
		t.Fatalf("find project: %v", err)
	}
	now := time.Now().UTC()
	if err := NewFeatureStore(db).Create(t.Context(), feature.Feature{
		ID: "fea_agent_draft", ProjectID: projectID, Title: "Draft", State: feature.StateDraft,
		DialogueLimits: project.DefaultDialogueLimits(),
		AgentProviders: project.AgentProviders{
			Lead: project.AgentProviderClaude, Reviewer: project.AgentProviderCodex,
			LeadAgent: first.ID, ReviewerAgent: "codex",
		},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create draft work order: %v", err)
	}
	if err := service.Delete(t.Context(), first.ID); !errors.Is(err, agent.ErrInUse) {
		t.Fatalf("delete an agent chosen by a draft work order: %v", err)
	}
}
