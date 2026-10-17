package project

import (
	"errors"
	"testing"
)

func TestPhaseChoicesPickEachPhasesModelAndEffort(t *testing.T) {
	choices := PhaseChoices{LeadPlanModel: "opus", LeadEffort: "medium", ReviewerPlanEffort: "high"}
	for _, test := range []struct {
		reviewer, planning bool
		model, effort      string
	}{
		{false, true, "opus", "medium"},
		{false, false, "sonnet", "medium"},
		{true, true, "sonnet", "high"},
		{true, false, "sonnet", ""},
	} {
		model, effort := choices.For(test.reviewer, test.planning, "sonnet")
		if model != test.model || effort != test.effort {
			t.Errorf("For(%t, %t) = %s, %s; want %s, %s", test.reviewer, test.planning, model, effort, test.model, test.effort)
		}
	}
	if cleared := choices.WithoutPlanModel(false); cleared.LeadPlanModel != "" || cleared.LeadEffort != "medium" {
		t.Fatalf("cleared = %+v", cleared)
	}
}

func TestPhaseChoicesNormalize(t *testing.T) {
	normalized, err := PhaseChoices{LeadPlanModel: " gpt-6-astra ", LeadEffort: "xhigh"}.Normalize()
	if err != nil || normalized.LeadPlanModel != "gpt-6-astra" {
		t.Fatalf("normalized = %+v err=%v", normalized, err)
	}
	if _, err := (PhaseChoices{LeadEffort: "Max!"}).Normalize(); !errors.Is(err, ErrInvalidPhaseChoices) {
		t.Fatalf("bad effort err = %v", err)
	}
	if _, err := (PhaseChoices{ReviewerPlanModel: "bad model"}).Normalize(); !errors.Is(err, ErrInvalidPhaseChoices) {
		t.Fatalf("bad model err = %v", err)
	}
}

func TestEffortAbove(t *testing.T) {
	for _, test := range []struct {
		effort, ceiling string
		above           bool
	}{
		{"medium", "high", false}, {"high", "high", false}, {"xhigh", "high", true},
		{"", "low", false}, {"max", "", false}, {"turbo", "max", true}, {"minimal", "low", false},
	} {
		if got := EffortAbove(test.effort, test.ceiling); got != test.above {
			t.Errorf("EffortAbove(%q, %q) = %t", test.effort, test.ceiling, got)
		}
	}
	if !ValidEffortCeiling("xhigh") || ValidEffortCeiling("none") || ValidEffortCeiling("") {
		t.Fatal("effort ceilings")
	}
}
