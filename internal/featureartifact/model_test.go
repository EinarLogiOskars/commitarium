package featureartifact

import (
	"testing"
	"time"
)

func TestImplementationPlanNormalizeAndValidate(t *testing.T) {
	plan, err := (ImplementationPlan{
		Title: "Backend plan", Subtitle: "Durable live checklist",
		Steps: []ImplementationPlanStep{
			{ID: "store", Title: "Persist", Subtitle: "Add storage", DetailsMarkdown: "Create the store.", Verification: []string{"go test ./internal/database"}, CommitSubject: "Add artifact storage"},
			{ID: "api", Title: "Expose", Subtitle: "Add the API", DetailsMarkdown: "Expose the artifact.", Verification: []string{"go test ./internal/httpapi"}, CommitSubject: "Expose feature artifacts"},
		},
	}).NormalizeInitial(3)
	if err != nil {
		t.Fatalf("normalize plan: %v", err)
	}
	if plan.PlanVersion != 3 || plan.Steps[0].Position != 1 || plan.Steps[1].Position != 2 ||
		plan.Steps[0].Status != StepPending || plan.Complete() {
		t.Fatalf("normalized plan = %+v", plan)
	}
	now := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	plan.Steps[0].Status = StepCompleted
	plan.Steps[0].CommitID = "0123456789abcdef0123456789abcdef01234567"
	plan.Steps[0].CompletedAt = &now
	plan.Steps[1].Status = StepInProgress
	if err := plan.Validate(); err != nil {
		t.Fatalf("validate progress: %v", err)
	}
}

func TestImplementationPlanRejectsOutOfOrderCompletion(t *testing.T) {
	plan, err := (ImplementationPlan{
		Title: "Plan", Subtitle: "Ordered",
		Steps: []ImplementationPlanStep{
			{ID: "one", Title: "One", Subtitle: "First", DetailsMarkdown: "First.", Verification: []string{"test one"}, CommitSubject: "Do one"},
			{ID: "two", Title: "Two", Subtitle: "Second", DetailsMarkdown: "Second.", Verification: []string{"test two"}, CommitSubject: "Do two"},
		},
	}).NormalizeInitial(1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan.Steps[1].Status = StepCompleted
	plan.Steps[1].CommitID = "0123456789abcdef0123456789abcdef01234567"
	plan.Steps[1].CompletedAt = &now
	if err := plan.Validate(); err == nil {
		t.Fatal("out-of-order completion was accepted")
	}
}
