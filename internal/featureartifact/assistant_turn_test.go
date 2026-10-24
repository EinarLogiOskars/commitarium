package featureartifact

import (
	"errors"
	"strings"
	"testing"
)

func TestSetUpPreviewNeedsOnlyAReason(t *testing.T) {
	turn, err := AssistantTurn{Actions: []AssistantAction{{
		Type: AssistantSetUpPreview, Reason: " The app does not open ", Guidance: " The api exits. ",
	}}}.Normalize()
	if err != nil || turn.Actions[0].Reason != "The app does not open" || turn.Actions[0].Guidance != "The api exits." {
		t.Fatalf("normalized = %+v, %v", turn, err)
	}
	if _, err := (AssistantTurn{Actions: []AssistantAction{{Type: AssistantSetUpPreview}}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("a set_up_preview without a reason = %v", err)
	}
	long := AssistantAction{Type: AssistantSetUpPreview, Reason: "Fails", Guidance: strings.Repeat("x", MaxGoalBytes+1)}
	if _, err := (AssistantTurn{Actions: []AssistantAction{long}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("unbounded guidance = %v", err)
	}
}

// A new app carries the brief the assistant gathered and what it assumed,
// trimmed and bounded.
func TestANewAppCarriesItsBrief(t *testing.T) {
	app := AssistantAction{Type: AssistantCreateApp, Reason: "Settled", Title: "VSK Kerfi", Description: "VAT returns.",
		AppBrief: " For a sole trader. ", Assumptions: []string{" Filed every two months ", " "}}
	turn, err := AssistantTurn{Actions: []AssistantAction{app}}.Normalize()
	if err != nil || turn.Actions[0].AppBrief != "For a sole trader." ||
		strings.Join(turn.Actions[0].Assumptions, "|") != "Filed every two months" {
		t.Fatalf("normalized = %+v, %v", turn, err)
	}
	app.AppBrief = strings.Repeat("x", MaxAppBriefBytes+1)
	if _, err := (AssistantTurn{Actions: []AssistantAction{app}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("an unbounded brief = %v", err)
	}
}

// Research names what it looks up and asks its question.
func TestResearchNeedsATitleAndAQuestion(t *testing.T) {
	research := AssistantAction{Type: AssistantResearch, Reason: "A fact", Title: "VAT rates"}
	if _, err := (AssistantTurn{Actions: []AssistantAction{research}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("research without a question = %v", err)
	}
	research.Description = "Which VAT rates apply in Iceland?"
	if _, err := (AssistantTurn{Actions: []AssistantAction{research}}).Normalize(); err != nil {
		t.Fatal(err)
	}
}
