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

func TestADeployNeedsOnlyAReason(t *testing.T) {
	turn, err := AssistantTurn{Actions: []AssistantAction{{Type: AssistantDeployApp, Reason: " Asked to ship it "}}}.Normalize()
	if err != nil || turn.Actions[0].Reason != "Asked to ship it" {
		t.Fatalf("normalized = %+v, %v", turn, err)
	}
}

// An apps page card names what it does; anything else is refused.
func TestAppsPageCardsNameWhatTheyDo(t *testing.T) {
	for _, action := range []AssistantAction{
		{Type: AssistantSetAppsPage, Reason: "Asked", How: "company"},
		{Type: AssistantSetAppsPage, Reason: "Asked", How: "off"},
		{Type: AssistantListOnAppsPage, Reason: "Asked", How: "hide"},
	} {
		if _, err := (AssistantTurn{Actions: []AssistantAction{action}}).Normalize(); err != nil {
			t.Fatalf("%+v: %v", action, err)
		}
	}
	for _, action := range []AssistantAction{
		{Type: AssistantSetAppsPage, Reason: "Asked"},
		{Type: AssistantSetAppsPage, Reason: "Asked", How: "list"},
		{Type: AssistantListOnAppsPage, Reason: "Asked", How: "off"},
	} {
		if _, err := (AssistantTurn{Actions: []AssistantAction{action}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("%+v accepted: %v", action, err)
		}
	}
}

// A deploy may propose the app's address: a name under a domain, the bare
// domain, and extra addresses, kept in lower case.
func TestADeployMayProposeItsAddress(t *testing.T) {
	site := AssistantAction{Type: AssistantDeployApp, Reason: "Asked", Domain: " EinarLogi.is ", Name: "",
		Aliases: []AppAddress{{Domain: "einarlogi.com", Name: ""}, {Domain: "einarlogi.is", Name: " En "}}}
	turn, err := AssistantTurn{Actions: []AssistantAction{site}}.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := turn.Actions[0]; got.Domain != "einarlogi.is" || got.Aliases[1].Name != "en" {
		t.Fatalf("normalized = %+v", got)
	}
	for _, action := range []AssistantAction{
		{Type: AssistantDeployApp, Reason: "Asked", Name: "notes"},
		{Type: AssistantDeployApp, Reason: "Asked", Domain: "example.com", Name: "a.b"},
		{Type: AssistantDeployApp, Reason: "Asked", Domain: "example.com", Aliases: []AppAddress{{Name: "x"}}},
	} {
		if _, err := (AssistantTurn{Actions: []AssistantAction{action}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("%+v accepted: %v", action, err)
		}
	}
}

// The server's setup cards say what they change; anything else is refused.
func TestServerCardsNameWhatTheyChange(t *testing.T) {
	for _, action := range []AssistantAction{
		{Type: AssistantAddDomain, Reason: "Asked", Domain: "commitarium.com"},
		{Type: AssistantAddDomain, Reason: "Asked", Domain: "commitarium.com", How: "each_name"},
		{Type: AssistantInstallAuthentik, Reason: "Asked"},
		{Type: AssistantInstallAuthentik, Reason: "Asked", Domain: "commitarium.com"},
		{Type: AssistantSetAppAccess, Reason: "Asked", How: "company", App: "notes.example.com", Groups: []string{"sales"}},
		{Type: AssistantSetAppAccess, Reason: "Asked", How: "private"},
		{Type: AssistantSetHomelab, Reason: "Asked", How: "off"},
		{Type: AssistantSetAppsPage, Reason: "Asked", How: "public", Domain: "commitarium.com"},
	} {
		if _, err := (AssistantTurn{Actions: []AssistantAction{action}}).Normalize(); err != nil {
			t.Fatalf("%+v: %v", action, err)
		}
	}
	for _, action := range []AssistantAction{
		{Type: AssistantAddDomain, Reason: "Asked"},
		{Type: AssistantAddDomain, Reason: "Asked", Domain: "*.example.com"},
		{Type: AssistantAddDomain, Reason: "Asked", Domain: "example.com", How: "proxied"},
		{Type: AssistantInstallAuthentik, Reason: "Asked", Domain: "https://example.com"},
		{Type: AssistantSetAppAccess, Reason: "Asked", How: "off"},
		{Type: AssistantSetAppAccess, Reason: "Asked", How: "public", Groups: []string{"sales"}},
		{Type: AssistantSetHomelab, Reason: "Asked"},
		{Type: AssistantSetHomelab, Reason: "Asked", How: "private"},
	} {
		if _, err := (AssistantTurn{Actions: []AssistantAction{action}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
			t.Fatalf("%+v accepted: %v", action, err)
		}
	}
	if !ServerCard(AssistantAddDomain) || ServerCard(AssistantDeployApp) || !AnyChat(AssistantSetAppsPage) ||
		AnyChat(AssistantListOnAppsPage) {
		t.Fatal("the server's cards are not told apart")
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

// Only a helper may be asked to test the app.
func TestOnlyDelegateTestsTheApp(t *testing.T) {
	delegate := AssistantAction{Type: AssistantDelegate, Reason: "Asked for", Title: "Try sign-in",
		Description: "Sign in as the admin and add a meal.", TestApp: true}
	turn, err := (AssistantTurn{Actions: []AssistantAction{delegate}}).Normalize()
	if err != nil || !turn.Actions[0].TestApp {
		t.Fatalf("delegate with test_app = %+v, %v", turn, err)
	}
	research := AssistantAction{Type: AssistantResearch, Reason: "A fact", Title: "VAT rates",
		Description: "Which VAT rates apply in Iceland?", TestApp: true}
	if _, err := (AssistantTurn{Actions: []AssistantAction{research}}).Normalize(); !errors.Is(err, ErrInvalidArtifact) {
		t.Fatalf("research with test_app = %v", err)
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
	// A question about the user may suggest nothing.
	asked, err := AssistantTurn{Questions: []AssistantQuestion{
		{Text: "How many people?", Options: []string{"Just me", "A handful"}, Suggested: -1},
	}}.Normalize()
	if err != nil || asked.Questions[0].Suggested != -1 {
		t.Fatalf("asked = %+v, %v", asked.Questions, err)
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
