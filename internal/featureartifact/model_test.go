package featureartifact

import (
	"strings"
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

func TestAcceptanceTestsNormalizeAndValidate(t *testing.T) {
	tests, err := (AcceptanceTests{
		TestCommitID: "0123456789abcdef0123456789abcdef01234567",
		Tests: []AcceptanceTest{
			{ID: "creates-report", Title: "Creates the requested report", Status: AcceptanceTestFailed, Note: "provider value"},
			{ID: "rejects-empty", Title: "Rejects an empty request"},
		},
	}).NormalizeInitial(2)
	if err != nil {
		t.Fatalf("normalize acceptance tests: %v", err)
	}
	if tests.PlanVersion != 2 || tests.ImplementationCommitID != "" ||
		tests.Tests[0].Position != 1 || tests.Tests[1].Position != 2 ||
		tests.Tests[0].Status != AcceptanceTestPending || tests.Tests[0].Note != "" {
		t.Fatalf("normalized acceptance tests = %+v", tests)
	}
	tests.ImplementationCommitID = "abcdef0123456789abcdef0123456789abcdef01"
	tests.Tests[0].Status = AcceptanceTestRunning
	tests.Tests[1].Status = AcceptanceTestNotApplicable
	tests.Tests[1].Note = "The accepted goal excludes empty requests."
	if err := tests.Validate(); err != nil {
		t.Fatalf("validate acceptance progress: %v", err)
	}
}

func TestAcceptanceTestsRejectExecutedStatusWithoutImplementation(t *testing.T) {
	tests, err := (AcceptanceTests{
		TestCommitID: "0123456789abcdef0123456789abcdef01234567",
		Tests:        []AcceptanceTest{{ID: "behavior", Title: "Exercises the behavior"}},
	}).NormalizeInitial(1)
	if err != nil {
		t.Fatal(err)
	}
	tests.Tests[0].Status = AcceptanceTestPassed
	if err := tests.Validate(); err == nil {
		t.Fatal("executed status without an implementation commit was accepted")
	}
}

func TestNormalizeInitialRepairsAgentChosenIDs(t *testing.T) {
	tests, err := (AcceptanceTests{
		TestCommitID: strings.Repeat("a", 40),
		Tests: []AcceptanceTest{
			{ID: "Docker Compose: start", Title: "Starts"},
			{ID: "docker-compose-start", Title: "Duplicate after cleanup"},
			{ID: "!!!", Title: "Nothing usable"},
			{ID: "readme.docker_section", Title: "Already valid"},
		},
	}).NormalizeInitial(1)
	if err != nil {
		t.Fatalf("normalize acceptance tests: %v", err)
	}
	want := []string{"docker-compose-start", "docker-compose-start-2", "test-3", "readme.docker_section"}
	for index, test := range tests.Tests {
		if test.ID != want[index] {
			t.Fatalf("test %d ID = %q, want %q", index+1, test.ID, want[index])
		}
	}
	plan, err := (ImplementationPlan{
		Title: "Plan", Subtitle: "Sub",
		Steps: []ImplementationPlanStep{{
			ID: "Update README (Docker)", Title: "Update", Subtitle: "Docs",
			DetailsMarkdown: "Edit README.", Verification: []string{"Read it"}, CommitSubject: "docs: update readme",
		}},
	}).NormalizeInitial(1)
	if err != nil || plan.Steps[0].ID != "update-readme-docker" {
		t.Fatalf("normalize plan: step=%+v err=%v", plan.Steps, err)
	}
}
