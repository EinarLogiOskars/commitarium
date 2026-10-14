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
