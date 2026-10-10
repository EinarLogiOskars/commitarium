package database

import (
	"errors"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
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
	_ = run
	if err := service.Delete(t.Context(), "codex"); !errors.Is(err, agent.ErrInUse) {
		t.Fatalf("delete an agent in use: %v", err)
	}
}
