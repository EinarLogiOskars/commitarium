package database

import (
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

func TestFeatureStoreSavesPhaseChoices(t *testing.T) {
	db, store := newTestExecutionStore(t)
	run, _ := createExecutionRecords(t, db, store)
	features := NewFeatureStore(db)

	if choices, err := features.PhaseChoices(t.Context(), run.FeatureID); err != nil || choices != (project.PhaseChoices{}) {
		t.Fatalf("no choices = %+v err=%v", choices, err)
	}
	want := project.PhaseChoices{LeadPlanModel: "claude-opus-5-5", LeadEffort: "medium", ReviewerPlanEffort: "high"}
	if err := features.SavePhaseChoices(t.Context(), run.FeatureID, want); err != nil {
		t.Fatal(err)
	}
	want.LeadPlanModel = "gpt-6-astra"
	if err := features.SavePhaseChoices(t.Context(), run.FeatureID, want); err != nil {
		t.Fatal(err)
	}
	if got, err := features.PhaseChoices(t.Context(), run.FeatureID); err != nil || got != want {
		t.Fatalf("choices = %+v err=%v", got, err)
	}
}
