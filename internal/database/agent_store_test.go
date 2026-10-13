package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// A project and a draft work order created without agents (ADR-027) read
// back without any, not as the historical Codex default, and hold no agent.
func TestProjectsAndWorkOrdersWithoutAgentsStayWithout(t *testing.T) {
	db, _ := newTestExecutionStore(t)
	now := time.Now().UTC()
	projects, features := NewProjectStore(db), NewFeatureStore(db)
	if err := projects.Create(t.Context(), project.Project{
		ID: "prj_plain", Name: "Plain", RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		DialogueLimits: project.DefaultDialogueLimits(), CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := features.Create(t.Context(), feature.Feature{
		ID: "fea_plain", ProjectID: "prj_plain", Title: "Draft", State: feature.StateDraft,
		DialogueLimits: project.DefaultDialogueLimits(), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create work order: %v", err)
	}
	stored, err := projects.GetByID(t.Context(), "prj_plain")
	if err != nil || !stored.AgentProviders.IsZero() {
		t.Fatalf("project agents = %+v err=%v", stored.AgentProviders, err)
	}
	draft, err := features.GetByID(t.Context(), "fea_plain")
	if err != nil || !draft.AgentProviders.IsZero() {
		t.Fatalf("work order agents = %+v err=%v", draft.AgentProviders, err)
	}
	service := agent.NewService(NewAgentStore(db))
	codex, err := service.Create(t.Context(), "Codex", project.AgentProviderCodex)
	if err != nil || codex.ID != "codex" {
		t.Fatalf("create agent: %+v err=%v", codex, err)
	}
	if err := service.Delete(t.Context(), codex.ID); err != nil {
		t.Fatalf("an agent no project chose could not be removed: %v", err)
	}
}

func TestAgentStoreStartsEmptyCreatesRenamesAndRemoves(t *testing.T) {
	db, _ := newTestExecutionStore(t)
	service := agent.NewService(NewAgentStore(db))

	// A new install has no agents (ADR-027); the user adds each one.
	seeded, err := service.List(t.Context())
	if err != nil || len(seeded) != 0 {
		t.Fatalf("a new install has agents=%+v err=%v", seeded, err)
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
	// A work order that has not started chooses its agents at Start, so it
	// does not keep an agent from being removed.
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
	if err := service.Delete(t.Context(), first.ID); err != nil {
		t.Fatalf("delete an agent named by a draft work order: %v", err)
	}
}
