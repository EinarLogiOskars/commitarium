package database

import (
	"errors"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

// A lead that merges main after its last step records the merge on that
// step, so the published head is still the last step's commit. Only the last
// step moves; an earlier one stays where it was recorded.
func TestTheLastCompletedStepMovesToANewerHead(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	service := workflow.NewService(store)
	lead := workflow.Actor{Kind: workflow.ActorKindAgent, ID: "implementation-lead"}
	step := func(position int, id string) featureartifact.ImplementationPlanStep {
		return featureartifact.ImplementationPlanStep{
			ID: id, Position: position, Title: "Step " + id, Subtitle: "Does " + id,
			DetailsMarkdown: "Details.", Verification: []string{"npm test"}, CommitSubject: "Add " + id,
			Status: featureartifact.StepPending,
		}
	}
	plan := featureartifact.ImplementationPlan{
		PlanVersion: 1, Title: "Checklist", Subtitle: "Two steps",
		Steps: []featureartifact.ImplementationPlanStep{step(1, "first"), step(2, "last")},
	}
	if _, err := service.PutImplementationPlan(t.Context(), created.ID, 0, plan, lead, "plan"); err != nil {
		t.Fatalf("put plan: %v", err)
	}
	first, last, merged := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	for index, change := range []struct {
		id     string
		status featureartifact.StepStatus
		commit string
	}{
		{"first", featureartifact.StepInProgress, ""}, {"first", featureartifact.StepCompleted, first},
		{"last", featureartifact.StepInProgress, ""}, {"last", featureartifact.StepCompleted, last},
		{"last", featureartifact.StepCompleted, merged},
	} {
		if _, err := service.TransitionImplementationPlanStep(
			t.Context(), created.ID, 1, change.id, change.status, change.commit, lead, "step-"+string(rune('a'+index)),
		); err != nil {
			t.Fatalf("%s %s: %v", change.id, change.status, err)
		}
	}
	current, err := service.GetFeatureArtifact(t.Context(), created.ID, featureartifact.KindImplementationPlan)
	if err != nil || !strings.Contains(current.Document, merged) || strings.Contains(current.Document, `"`+last+`"`) {
		t.Fatalf("the last step did not move to the merge: %v %s", err, current.Document)
	}
	if _, err := service.TransitionImplementationPlanStep(
		t.Context(), created.ID, 1, "first", featureartifact.StepCompleted, merged, lead, "move-first",
	); !errors.Is(err, featureartifact.ErrInvalidArtifact) {
		t.Fatalf("an earlier step moved: %v", err)
	}
}
