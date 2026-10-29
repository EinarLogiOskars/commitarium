package featureartifact

import (
	"errors"
	"slices"
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

// A project to plan is named and described like a new app.
func TestAProjectToPlanNeedsANameAndAGoal(t *testing.T) {
	plan := AssistantAction{Type: AssistantPlanProject, Reason: "A real project", Title: "Shop sync"}
	if _, err := (AssistantTurn{Actions: []AssistantAction{plan}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("a project without a goal = %v", err)
	}
	plan.Description = "Keeps the shop's stock in step with the accounts."
	plan.AppBrief = strings.Repeat("x", MaxAppBriefBytes+1)
	if _, err := (AssistantTurn{Actions: []AssistantAction{plan}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("an unbounded brief = %v", err)
	}
	plan.AppBrief = "Two shops, one accounting system."
	if _, err := (AssistantTurn{Actions: []AssistantAction{plan}}).Normalize(); err != nil {
		t.Fatal(err)
	}
}

// A turn's chat title is optional: trimmed to one line, and refused only
// when far too long.
func TestATurnsChatTitleIsOptional(t *testing.T) {
	turn, err := AssistantTurn{ChatTitle: "  Bank \n CSV   import "}.Normalize()
	if err != nil || turn.ChatTitle != "Bank CSV import" {
		t.Fatalf("turn = %+v err=%v", turn, err)
	}
	if turn, err := (AssistantTurn{}).Normalize(); err != nil || turn.ChatTitle != "" {
		t.Fatalf("turn = %+v err=%v", turn, err)
	}
	if _, err := (AssistantTurn{ChatTitle: strings.Repeat("x", MaxChatTitleBytes+1)}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("an overlong title = %v", err)
	}
}

// A turn asks at most three questions, each with two to four answers and a
// suggested one; what does not fit is left out rather than failing the turn.
func TestATurnsQuestionsAreKeptWithinTheirLimits(t *testing.T) {
	turn, err := AssistantTurn{Questions: []AssistantQuestion{
		{Text: " Light   or dark? ", Options: []string{"Light", " Dark ", "Light", "", "System", "High contrast", "Sepia"}, Suggested: 1},
		{Text: "Only one answer?", Options: []string{"Yes"}},
		{Text: "", Options: []string{"A", "B"}},
		{Text: "Where does it run?", Options: []string{"Commitarium", "Elsewhere"}, Suggested: 7},
		{Text: "Who uses it?", Options: []string{"Me", "The team"}, Suggested: 1},
		{Text: "One too many?", Options: []string{"Yes", "No"}},
	}}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.Questions) != 3 {
		t.Fatalf("questions = %+v", turn.Questions)
	}
	first := turn.Questions[0]
	if first.Text != "Light or dark?" || strings.Join(first.Options, "|") != "Light|Dark|System|High contrast" || first.Suggested != 1 {
		t.Fatalf("first = %+v", first)
	}
	if turn.Questions[1].Text != "Where does it run?" || turn.Questions[1].Suggested != 0 ||
		turn.Questions[2].Text != "Who uses it?" || turn.Questions[2].Suggested != 1 {
		t.Fatalf("questions = %+v", turn.Questions)
	}
	if none, err := (AssistantTurn{Questions: []AssistantQuestion{{Text: "Hm?"}}}).Normalize(); err != nil || none.Questions != nil {
		t.Fatalf("none = %+v, %v", none.Questions, err)
	}
}

// Suggesting plan mode needs only a reason; its goal is optional and
// bounded.
func TestStartPlanningNeedsOnlyAReason(t *testing.T) {
	start := AssistantAction{Type: AssistantStartPlanning}
	if _, err := (AssistantTurn{Actions: []AssistantAction{start}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("plan mode without a reason = %v", err)
	}
	start.Reason = "Several screens and an outside service to settle"
	if _, err := (AssistantTurn{Actions: []AssistantAction{start}}).Normalize(); err != nil {
		t.Fatal(err)
	}
	start.Description = strings.Repeat("x", MaxGoalBytes+1)
	if _, err := (AssistantTurn{Actions: []AssistantAction{start}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("an unbounded goal = %v", err)
	}
	start.Description = "A weekly digest for the team"
	if _, err := (AssistantTurn{Actions: []AssistantAction{start}}).Normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionsLeaveOutCatchAllAnswers(t *testing.T) {
	got := NormalizeQuestions([]AssistantQuestion{{
		Text:      "What is this round for?",
		Options:   []string{"More PR detail", "Something else", "Notifications", "Other…"},
		Suggested: 2,
	}})
	if len(got) != 1 || !slices.Equal(got[0].Options, []string{"More PR detail", "Notifications"}) || got[0].Suggested != 1 {
		t.Fatalf("questions = %+v", got)
	}
}
