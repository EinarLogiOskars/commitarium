package database

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestExecutionStoreChangesRunModelsAndMarksStoppedAttempts(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, coder := createExecutionRecords(t, db, store)
	now := time.Date(2026, time.October, 12, 9, 0, 0, 0, time.UTC)
	changed, err := store.ChangeRunModels(t.Context(), run.ID,
		project.AgentModels{Lead: "cheap-model", Reviewer: "review-model"},
		[]execution.ModelChange{{Role: worker.RoleLead, FromModel: run.AgentModels.Lead, ToModel: "cheap-model", Stopped: true, CreatedAt: now}},
		[]string{"att_live"}, now)
	if err != nil {
		t.Fatalf("change models: %v", err)
	}
	if changed.AgentModels.Lead != "cheap-model" {
		t.Fatalf("run models = %+v", changed.AgentModels)
	}
	changes, err := store.ModelChangesForRun(t.Context(), run.ID)
	if err != nil || len(changes) != 1 || changes[0].ToModel != "cheap-model" || !changes[0].Stopped {
		t.Fatalf("model changes = %+v err=%v", changes, err)
	}
	if switched, err := store.TakeModelSwitch(t.Context(), run.ID, "att_live"); err != nil || !switched {
		t.Fatalf("first take = %t err=%v", switched, err)
	}
	if switched, _ := store.TakeModelSwitch(t.Context(), run.ID, "att_live"); switched {
		t.Fatal("a switch mark was taken twice")
	}

	if err := store.RecordAttemptUsage(t.Context(), execution.AttemptUsage{
		SessionID: coder.ID, AttemptID: "att_1", Model: "cheap-model", Effort: "high",
		Usage: execution.TokenUsage{InputTokens: 3}, RecordedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	usage, err := store.FeatureAttemptUsage(t.Context(), run.FeatureID)
	if err != nil || len(usage) != 1 || usage[0].Model != "cheap-model" || usage[0].Effort != "high" {
		t.Fatalf("usage = %+v err=%v", usage, err)
	}
}
