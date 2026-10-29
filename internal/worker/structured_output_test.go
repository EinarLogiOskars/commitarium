package worker

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

func TestOutputJSONSchemaCoversStructuredContracts(t *testing.T) {
	tests := []struct {
		contract OutputContract
		required []string
	}{
		{OutputContractGoalClarification, []string{"action", "message", "goal", "open_questions"}},
		{OutputContractPlanningLead, []string{"action", "content", "plan_title", "plan_subtitle", "steps"}},
		{OutputContractImplementationLead, []string{"action", "summary", "commit_id", "pull_request_number", "system_packages", "environment_reason", "finding_answers"}},
		{OutputContractAcceptanceTests, []string{"action", "summary", "test_commit_id", "tests"}},
		{OutputContractImplementationReview, []string{"action", "summary", "commit_id", "pull_request_number", "review_id"}},
		{OutputContractImplementationReadiness, []string{"action", "summary"}},
		{OutputContractIntervention, []string{"effect", "response"}},
		{OutputContractToolchainSetup, []string{"action", "message", "tools", "services", "run"}},
		{OutputContractSessionOpening, []string{"title", "message"}},
	}
	for _, test := range tests {
		t.Run(string(test.contract), func(t *testing.T) {
			schema, ok := OutputJSONSchema(test.contract).(map[string]any)
			if !ok || !reflect.DeepEqual(schema["required"], test.required) || schema["additionalProperties"] != false {
				t.Fatalf("schema for %q = %#v", test.contract, schema)
			}
		})
	}
	if OutputJSONSchema("") != nil || OutputJSONSchema("unknown") != nil {
		t.Fatal("unstructured or unknown contracts unexpectedly returned schemas")
	}
}

func TestToolchainOutputSchemaUsesStrictToolEntries(t *testing.T) {
	schema := OutputJSONSchema(OutputContractToolchainSetup).(map[string]any)
	properties := schema["properties"].(map[string]any)
	tools := properties["tools"].(map[string]any)
	items := tools["items"].(map[string]any)
	if tools["type"] != "array" || items["additionalProperties"] != false ||
		!reflect.DeepEqual(items["required"], []string{"name", "version"}) {
		t.Fatalf("toolchain tools schema = %#v", tools)
	}
}

func TestResolveStructuredOutput(t *testing.T) {
	tests := []struct {
		name        string
		contract    OutputContract
		raw         string
		event       Event
		disposition Disposition
		publication *ImplementationPublication
		review      *ReviewPublication
		effect      InterventionEffect
		proposal    *ToolchainProposal
		goalDraft   *featureartifact.GoalDraft
		plan        *featureartifact.ImplementationPlan
		acceptance  *featureartifact.AcceptanceTests
		environment *EnvironmentRequest
	}{
		{
			name: "planning response", contract: OutputContractPlanningLead,
			raw:   `{"action":"respond","content":"One detail remains.","plan_title":"","plan_subtitle":"","steps":[]}`,
			event: Event{Type: EventMessage, Text: "One detail remains."}, disposition: DispositionSucceeded,
		},
		{
			name: "plan submission", contract: OutputContractPlanningLead,
			raw:   `{"action":"submit_plan","content":"Final agreed plan","plan_title":"Backend plan","plan_subtitle":"Ship it safely","steps":[{"id":"store","title":"Persist state","subtitle":"Add durable storage","details_markdown":"Create the migration and store.","verification":["go test ./internal/database"],"commit_subject":"Add artifact persistence"}]}`,
			event: Event{Type: EventPlanSubmitted, Text: "Final agreed plan"}, disposition: DispositionSucceeded,
			plan: &featureartifact.ImplementationPlan{Title: "Backend plan", Subtitle: "Ship it safely", Steps: []featureartifact.ImplementationPlanStep{{ID: "store", Position: 1, Title: "Persist state", Subtitle: "Add durable storage", DetailsMarkdown: "Create the migration and store.", Verification: []string{"go test ./internal/database"}, CommitSubject: "Add artifact persistence", Status: featureartifact.StepPending}}},
		},
		{
			name: "goal proposal", contract: OutputContractGoalClarification,
			raw:   `{"action":"propose","message":"This is ready.","goal":"Build the feature.","open_questions":[]}`,
			event: Event{Type: EventMessage, Text: "This is ready."}, disposition: DispositionSucceeded,
			goalDraft: &featureartifact.GoalDraft{Goal: "Build the feature.", OpenQuestions: []string{}},
		},
		{
			name: "published implementation", contract: OutputContractImplementationLead,
			raw:   `{"action":"published","summary":"Done","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7}`,
			event: Event{Type: EventMessage, Text: "Done"}, disposition: DispositionSucceeded,
			publication: &ImplementationPublication{CommitID: "0123456789abcdef0123456789abcdef01234567", PullRequestNumber: 7},
		},
		{
			name: "review response that changes nothing", contract: OutputContractImplementationLead,
			raw:   `{"action":"published","summary":"F1 out of scope","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"system_packages":[],"environment_reason":"","finding_answers":[{"finding":"F1","answer":"out_of_scope"}]}`,
			event: Event{Type: EventMessage, Text: "F1 out of scope"}, disposition: DispositionSucceeded,
			publication: &ImplementationPublication{CommitID: "0123456789abcdef0123456789abcdef01234567", PullRequestNumber: 7,
				FindingAnswers: []FindingAnswer{{Finding: "F1", Answer: FindingOutOfScope}}},
		},
		{
			name: "implementation blocker", contract: OutputContractImplementationLead,
			raw:   `{"action":"blocked","summary":"Cannot push","commit_id":"","pull_request_number":7}`,
			event: Event{Type: EventInputRequired, Text: "Cannot push"}, disposition: DispositionInputRequired,
		},
		{
			name: "authored acceptance tests", contract: OutputContractAcceptanceTests,
			raw:   `{"action":"authored","summary":"Two private tests are committed.","test_commit_id":"0123456789abcdef0123456789abcdef01234567","tests":[{"id":"exports-csv","title":"Exports visible rows"},{"id":"rejects-empty","title":"Rejects an empty selection"}]}`,
			event: Event{Type: EventMessage, Text: "Two private tests are committed."}, disposition: DispositionSucceeded,
			acceptance: &featureartifact.AcceptanceTests{TestCommitID: "0123456789abcdef0123456789abcdef01234567", Tests: []featureartifact.AcceptanceTest{
				{ID: "exports-csv", Position: 1, Title: "Exports visible rows", Status: featureartifact.AcceptanceTestPending},
				{ID: "rejects-empty", Position: 2, Title: "Rejects an empty selection", Status: featureartifact.AcceptanceTestPending},
			}},
		},
		{
			name: "implementation environment request", contract: OutputContractImplementationLead,
			raw:   `{"action":"environment_required","summary":"libvips is required","commit_id":"","pull_request_number":0,"system_packages":["libvips-dev"],"environment_reason":"The repository builds image bindings against libvips."}`,
			event: Event{Type: EventInputRequired, Text: "libvips is required"}, disposition: DispositionInputRequired,
			environment: &EnvironmentRequest{SystemPackages: []string{"libvips-dev"}, Reason: "The repository builds image bindings against libvips."},
		},
		{
			name: "changes requested", contract: OutputContractImplementationReview,
			raw:   `{"action":"changes_requested","summary":"Fix the race","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"review_id":11}`,
			event: Event{Type: EventMessage, Text: "Fix the race"}, disposition: DispositionChangesRequested,
			review: &ReviewPublication{CommitID: "0123456789abcdef0123456789abcdef01234567", PullRequestNumber: 7, ReviewID: 11},
		},
		{
			name: "merge readiness", contract: OutputContractImplementationReadiness,
			raw:   `{"action":"ready_to_merge","summary":"Ready"}`,
			event: Event{Type: EventMessage, Text: "Ready"}, disposition: DispositionSucceeded,
		},
		{
			name: "intervention guidance", contract: OutputContractIntervention,
			raw:         `{"effect":"guidance_applied","response":"I will preserve that preference."}`,
			event:       Event{Type: EventMessage, Text: "I will preserve that preference."},
			disposition: DispositionSucceeded, effect: InterventionEffectGuidanceApplied,
		},
		{
			name: "toolchain question", contract: OutputContractToolchainSetup,
			raw:   `{"action":"ask","message":"Do you need a browser UI?","tools":[],"services":[]}`,
			event: Event{Type: EventInputRequired, Text: "Do you need a browser UI?"}, disposition: DispositionInputRequired,
		},
		{
			name: "toolchain proposal", contract: OutputContractToolchainSetup,
			raw:   `{"action":"propose","message":"Use Python.","tools":[{"name":"python","version":"3.14.7"}],"services":[],"run":{"open":{"service":"web","port":8000}}}`,
			event: Event{Type: EventMessage, Text: "Use Python."}, disposition: DispositionSucceeded,
			proposal: &ToolchainProposal{Tools: map[string]string{"python": "3.14.7"}, Services: []string{},
				Run: &ToolchainRunConfig{Open: ToolchainRunOpen{Service: "web", Port: 8000}}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := ResolveStructuredOutput(test.contract, []byte(test.raw))
			if err != nil {
				t.Fatalf("resolve structured output: %v", err)
			}
			if resolved.Event != test.event || resolved.Disposition != test.disposition ||
				!reflect.DeepEqual(resolved.Publication, test.publication) ||
				!reflect.DeepEqual(resolved.Review, test.review) || resolved.InterventionEffect != test.effect ||
				!reflect.DeepEqual(resolved.ToolchainProposal, test.proposal) ||
				!reflect.DeepEqual(resolved.GoalDraft, test.goalDraft) ||
				!reflect.DeepEqual(resolved.ImplementationPlan, test.plan) ||
				!reflect.DeepEqual(resolved.AcceptanceTests, test.acceptance) ||
				!reflect.DeepEqual(resolved.EnvironmentRequest, test.environment) {
				t.Fatalf("resolved output = %+v", resolved)
			}
		})
	}
}

func TestResolveStructuredOutputFailsClosed(t *testing.T) {
	tests := []struct {
		contract OutputContract
		raw      string
	}{
		{OutputContractPlanningLead, `{"action":"unknown","content":"plan"}`},
		{OutputContractPlanningLead, `{"action":"respond","content":"reply","extra":true}`},
		{OutputContractImplementationLead, `{"action":"published","summary":"done","commit_id":"bad","pull_request_number":7}`},
		{OutputContractImplementationLead, `{"action":"blocked","summary":"blocked","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7}`},
		{OutputContractAcceptanceTests, `{"action":"authored","summary":"done","test_commit_id":"bad","tests":[{"id":"one","title":"One"}]}`},
		{OutputContractImplementationReview, `{"action":"approved","summary":"good","commit_id":"bad","pull_request_number":7,"review_id":11}`},
		{OutputContractImplementationReview, `{"action":"blocked","summary":"blocked","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"review_id":0}`},
		{OutputContractImplementationReadiness, `{"action":"unknown","summary":"decision"}`},
		{OutputContractImplementationReadiness, `{"action":"blocked","summary":"Cannot inspect"} {}`},
		{OutputContractIntervention, `{"effect":"unknown","response":"Noted"}`},
		{OutputContractIntervention, `{"effect":"replanning_required","response":""}`},
		{OutputContractToolchainSetup, `{"action":"ask","message":"Question","tools":[{"name":"python","version":"3.14.7"}],"services":[]}`},
		{OutputContractToolchainSetup, `{"action":"propose","message":"Use Python","tools":[],"services":[]}`},
		{OutputContractToolchainSetup, `{"action":"propose","message":"Use Python","tools":[{"name":"python","version":""}],"services":[]}`},
		{OutputContractToolchainSetup, `{"action":"propose","message":"Use Python","tools":[{"name":"python","version":"3.14.7"},{"name":"python","version":"3.13.7"}],"services":[]}`},
		{"unknown", `{}`},
	}
	for _, test := range tests {
		if _, err := ResolveStructuredOutput(test.contract, []byte(test.raw)); !errors.Is(err, ErrInvalidStructuredOutput) {
			t.Errorf("contract %q response %q error=%v", test.contract, test.raw, err)
		}
	}
}

func TestResolveStructuredEnvironmentRequestExplainsInvalidField(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "pull request",
			raw:  `{"action":"environment_required","summary":"Docker is required","commit_id":"","pull_request_number":8,"system_packages":["docker.io"],"environment_reason":"Docker is unavailable."}`,
			want: "cannot claim a pull request",
		},
		{
			name: "package",
			raw:  `{"action":"environment_required","summary":"Docker is required","commit_id":"","pull_request_number":0,"system_packages":["bad package"],"environment_reason":"Docker is unavailable."}`,
			want: `environment package "bad package" is invalid`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveStructuredOutput(OutputContractImplementationLead, []byte(test.raw))
			if !errors.Is(err, ErrInvalidStructuredOutput) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want invalid structured output containing %q", err, test.want)
			}
		})
	}
}

func TestResolveImplementationLeadExplainsEachRejectedField(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "pull request",
			raw:  `{"action":"published","summary":"Done","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":0,"system_packages":[],"environment_reason":""}`,
			want: "missing its pull_request_number",
		},
		{
			name: "packages",
			raw:  `{"action":"published","summary":"Done","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"system_packages":["libvips-dev"],"environment_reason":""}`,
			want: "system_packages must be empty unless action is environment_required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveStructuredOutput(OutputContractImplementationLead, []byte(test.raw))
			if !errors.Is(err, ErrInvalidStructuredOutput) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want invalid structured output containing %q", err, test.want)
			}
		})
	}
}

// Unused environment fields filled with a placeholder or a stray note do not
// reject finished work: placeholders are empty and a note joins the summary.
func TestResolveImplementationLeadForgivesUnusedEnvironmentFields(t *testing.T) {
	published, err := ResolveStructuredOutput(OutputContractImplementationLead, []byte(
		`{"action":"published","summary":"Polished the mobile layout.","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"system_packages":["none"],"environment_reason":"Live browser inspection was unavailable, so layouts were checked in tests."}`,
	))
	if err != nil {
		t.Fatalf("resolve published lead result: %v", err)
	}
	want := "Polished the mobile layout.\n\nNote: Live browser inspection was unavailable, so layouts were checked in tests."
	if published.Event.Text != want || published.Publication == nil || published.EnvironmentRequest != nil {
		t.Fatalf("published = %+v", published)
	}
	for _, placeholder := range []string{"", "none", "N/A", "na", "-", "null"} {
		raw := `{"action":"blocked","summary":"Needs a decision.","commit_id":"` + placeholder +
			`","pull_request_number":7,"system_packages":[],"environment_reason":"` + placeholder + `"}`
		blocked, err := ResolveStructuredOutput(OutputContractImplementationLead, []byte(raw))
		if err != nil || blocked.Event.Text != "Needs a decision." || blocked.Disposition != DispositionInputRequired {
			t.Fatalf("placeholder %q: blocked=%+v err=%v", placeholder, blocked, err)
		}
	}
	review, err := ResolveStructuredOutput(OutputContractImplementationReview, []byte(
		`{"action":"blocked","summary":"Cannot reach the PR.","commit_id":"n/a","pull_request_number":7,"review_id":0}`,
	))
	if err != nil || review.Disposition != DispositionInputRequired {
		t.Fatalf("blocked review with placeholder commit: %+v err=%v", review, err)
	}
	_, err = ResolveStructuredOutput(OutputContractImplementationReview, []byte(
		`{"action":"approved","summary":"Good.","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":0,"review_id":3}`,
	))
	if !errors.Is(err, ErrInvalidStructuredOutput) || !strings.Contains(err.Error(), "missing its pull_request_number") {
		t.Fatalf("review without pull request: %v", err)
	}
}

func TestResolveWorkOrderBrief(t *testing.T) {
	ask, err := ResolveStructuredOutput(OutputContractWorkOrderBrief, []byte(
		`{"action":"ask","message":"Should overdue items be highlighted?","goal":"","areas":[],"considerations":[],"open_questions":[]}`,
	))
	if err != nil || ask.Disposition != DispositionInputRequired || ask.HandoffBrief != nil {
		t.Fatalf("ask=%+v err=%v", ask, err)
	}
	proposed, err := ResolveStructuredOutput(OutputContractWorkOrderBrief, []byte(
		`{"action":"propose","message":"Here is the brief.","goal":"Add due dates.","areas":["backend/app/models"," "],"considerations":["Keep rows valid."],"open_questions":[]}`,
	))
	if err != nil || proposed.Disposition != DispositionSucceeded || proposed.HandoffBrief == nil ||
		proposed.HandoffBrief.Goal != "Add due dates." || len(proposed.HandoffBrief.Areas) != 1 {
		t.Fatalf("proposed=%+v err=%v", proposed, err)
	}
	if _, err := ResolveStructuredOutput(OutputContractWorkOrderBrief, []byte(
		`{"action":"propose","message":"Brief.","goal":"","areas":[],"considerations":[],"open_questions":[]}`,
	)); err == nil {
		t.Fatal("a brief without a goal was accepted")
	}
}

func TestResolveSessionOpening(t *testing.T) {
	opened, err := ResolveStructuredOutput(OutputContractSessionOpening, []byte(
		`{"title":"  Fix   the Icelandic\ncopy ","message":" Sure, which page first? "}`,
	))
	if err != nil || opened.SessionTitle != "Fix the Icelandic copy" ||
		opened.Event != (Event{Type: EventMessage, Text: "Sure, which page first?"}) ||
		opened.Disposition != DispositionSucceeded {
		t.Fatalf("session opening = %+v err=%v", opened, err)
	}
	long, err := ResolveStructuredOutput(OutputContractSessionOpening, []byte(
		`{"title":"`+strings.Repeat("a", 200)+`","message":"ok"}`,
	))
	if err != nil || len([]rune(long.SessionTitle)) != MaxSessionTitleRunes ||
		!strings.HasSuffix(long.SessionTitle, "…") {
		t.Fatalf("long title = %q err=%v", long.SessionTitle, err)
	}
	untitled, err := ResolveStructuredOutput(OutputContractSessionOpening, []byte(`{"title":" ","message":"hi"}`))
	if err != nil || untitled.SessionTitle != "" || untitled.Event.Text != "hi" {
		t.Fatalf("untitled opening = %+v err=%v", untitled, err)
	}
	if _, err := ResolveStructuredOutput(OutputContractSessionOpening, []byte(`{"title":"Tweaks","message":""}`)); !errors.Is(err, ErrInvalidStructuredOutput) {
		t.Fatalf("opening without a message: %v", err)
	}
	if field := PreviewProseField(OutputContractSessionOpening); field != "message" {
		t.Fatalf("preview field = %q", field)
	}
}

func TestResolveVisionPlan(t *testing.T) {
	asked, err := ResolveStructuredOutput(OutputContractVisionPlan, []byte(
		`{"action":"ask","message":"Who uses it?","statement":"","confidence":"low","guesses":[],"priorities":[]}`,
	))
	if err != nil || asked.Disposition != DispositionInputRequired || asked.VisionPlan != nil {
		t.Fatalf("question = %+v err=%v", asked, err)
	}
	proposed, err := ResolveStructuredOutput(OutputContractVisionPlan, []byte(`{
		"action":"propose","message":"Here is the plan.",
		"statement":"A calorie tracker for one person.","confidence":"medium","guesses":["Metric units"],
		"priorities":[{"level":0,"items":[
			{"title":"Log meals","description":"","items":[
				{"title":"Add a meal","description":"Name and calories.","items":[]},
				{"title":"Add a meal","description":"Again.","items":[{"title":"Quick add","description":""}]}
			]}
		]}]}`))
	if err != nil || proposed.VisionPlan == nil || proposed.Disposition != DispositionSucceeded {
		t.Fatalf("proposal = %+v err=%v", proposed, err)
	}
	items := proposed.VisionPlan.Priorities[0].Items
	if items[0].ID != "log-meals" || items[0].Items[0].ID != "add-a-meal" || items[0].Items[1].ID != "add-a-meal-2" ||
		items[0].Items[1].Items[0].ID != "quick-add" {
		t.Fatalf("item IDs = %+v", items)
	}
	if _, err := ResolveStructuredOutput(OutputContractVisionPlan, []byte(
		`{"action":"propose","message":"x","statement":"","confidence":"high","guesses":[],"priorities":[]}`,
	)); !errors.Is(err, ErrInvalidStructuredOutput) {
		t.Fatalf("a vision without a statement: %v", err)
	}
}

func TestResolveAssistantTurn(t *testing.T) {
	resolved, err := ResolveStructuredOutput(OutputContractAssistantTurn, []byte(`{
		"message": "Two work orders would finish P0.",
		"actions": [{"type":"create_work_order","reason":"Small form change","title":"Log a meal","description":"Form.",
			"vision_item_id":"log-a-meal","steps":"quick","independent_tests":false,"lead_agent":"","lead_model":"gpt-6-luna",
			"reviewer_agent":"","reviewer_model":"","work_order_id":"","when":""}],
		"notes": "Prefers quick changes for copy edits."}`))
	if err != nil || resolved.AssistantTurn == nil || len(resolved.AssistantTurn.Actions) != 1 ||
		resolved.Event.Text != "Two work orders would finish P0." || resolved.AssistantTurn.Notes == "" {
		t.Fatalf("resolved = %+v err=%v", resolved, err)
	}
	if _, err := ResolveStructuredOutput(OutputContractAssistantTurn, []byte(`{"message":"x","actions":[{"type":"change_models",
		"reason":"r","title":"","description":"","vision_item_id":"","steps":"","independent_tests":false,"lead_agent":"",
		"lead_model":"","reviewer_agent":"","reviewer_model":"","work_order_id":"fea_1","when":"now"}],"notes":""}`)); err == nil {
		t.Fatal("a model change without a model was accepted")
	}
	if OutputJSONSchema(OutputContractAssistantTurn) == nil {
		t.Fatal("no schema")
	}
}

// While a vision is planned, every turn carries the plan, which resolves to
// an update_plan ahead of the turn's own actions; other turns have no plan.
func TestResolveAssistantPlanningTurn(t *testing.T) {
	resolved, err := ResolveStructuredOutput(OutputContractAssistantPlanningTurn, []byte(`{
		"message": "Who uses it?",
		"actions": [{"type":"research","reason":"r","title":"GitHub's API","description":"Rate limits?","vision_item_id":"",
			"steps":"","lead_agent":"","lead_model":"","reviewer_agent":"","reviewer_model":"","work_order_id":"","when":""}],
		"notes": "",
		"plan": {"goal":"A dashboard of open pull requests.","decisions":[" Dark theme "],"open_questions":["Who uses it?",""],
			"items":[{"priority":0,"title":"List pull requests","description":"Open ones, newest first."}],
			"stack_presets":[],"frameworks":""}}`))
	if err != nil || resolved.AssistantTurn == nil || len(resolved.AssistantTurn.Actions) != 2 {
		t.Fatalf("resolved = %+v err=%v", resolved, err)
	}
	plan := resolved.AssistantTurn.Actions[0]
	if plan.Type != featureartifact.AssistantUpdatePlan || plan.Description != "A dashboard of open pull requests." ||
		len(plan.Decisions) != 1 || plan.Decisions[0] != "Dark theme" || len(plan.OpenQuestions) != 1 ||
		len(plan.PlanItems) != 1 || resolved.AssistantTurn.Actions[1].Type != featureartifact.AssistantResearch {
		t.Fatalf("actions = %+v", resolved.AssistantTurn.Actions)
	}
	if _, err := ResolveStructuredOutput(OutputContractAssistantPlanningTurn,
		[]byte(`{"message":"x","actions":[],"notes":""}`)); !errors.Is(err, ErrInvalidStructuredOutput) {
		t.Fatalf("a planning turn without a plan: %v", err)
	}
	required := func(contract OutputContract) []string {
		return OutputJSONSchema(contract).(map[string]any)["required"].([]string)
	}
	if !slices.Contains(required(OutputContractAssistantPlanningTurn), "plan") ||
		slices.Contains(required(OutputContractAssistantTurn), "plan") {
		t.Fatal("only the planning turn requires the plan")
	}
	plain, err := ResolveStructuredOutput(OutputContractAssistantTurn, []byte(`{"message":"x","actions":[],"notes":""}`))
	if err != nil || len(plain.AssistantTurn.Actions) != 0 {
		t.Fatalf("plain turn = %+v err=%v", plain, err)
	}
}

// A planning turn's plan may carry the written plan and slices as sub-items
// (PLAN_MODE_PLAN.md PM6); the schema asks for both.
func TestResolveAssistantPlanningTurnWithTheWrittenPlan(t *testing.T) {
	resolved, err := ResolveStructuredOutput(OutputContractAssistantPlanningTurn, []byte(`{
		"message": "Here is the first draft.", "actions": [], "notes": "", "chat_title": "", "questions": [],
		"plan": {"goal":"","step":"review","covered":[],"decisions":[],"open_questions":[],"stack_presets":[],"frameworks":"",
			"items":[{"priority":2,"title":"Sharing","description":"Share a list.","items":[{"title":" Share links ","description":"A link per list."}]}],
			"document":" # Sharing\n\n- [ ] 1. Share links "}}`))
	if err != nil || resolved.AssistantTurn == nil {
		t.Fatalf("resolved = %+v err=%v", resolved, err)
	}
	plan := resolved.AssistantTurn.Actions[0]
	if plan.PlanDocument != "# Sharing\n\n- [ ] 1. Share links" || len(plan.PlanItems) != 1 ||
		len(plan.PlanItems[0].Items) != 1 || plan.PlanItems[0].Items[0].Title != "Share links" {
		t.Fatalf("plan = %+v", plan)
	}
	properties := OutputJSONSchema(OutputContractAssistantPlanningTurn).(map[string]any)["properties"].(map[string]any)
	planSchema := properties["plan"].(map[string]any)
	item := planSchema["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	if !slices.Contains(planSchema["required"].([]string), "document") || !slices.Contains(item["required"].([]string), "items") {
		t.Fatal("the plan's schema does not ask for the written plan and slices")
	}
}

// A turn's questions resolve with it, trimmed to their limits; a question
// that does not fit is left out rather than failing the turn.
func TestResolveAssistantTurnQuestions(t *testing.T) {
	resolved, err := ResolveStructuredOutput(OutputContractAssistantPlanningTurn, []byte(`{
		"message": "Got it: a dashboard for your team.", "actions": [], "notes": "", "chat_title": "",
		"questions": [
			{"text":"Light or dark?","options":["Light","Dark","Follow the system"],"suggested":1},
			{"text":"Only one?","options":["Yes"],"suggested":0}
		],
		"plan": {"goal":"","step":"look","covered":["purpose","look"],"decisions":[],"open_questions":[],"items":[],"stack_presets":[],"frameworks":""}}`))
	if err != nil || resolved.AssistantTurn == nil || len(resolved.AssistantTurn.Questions) != 1 {
		t.Fatalf("resolved = %+v err=%v", resolved, err)
	}
	if plan := resolved.AssistantTurn.Actions[0]; plan.PlanStep != "look" || strings.Join(plan.PlanCovered, ",") != "purpose,look" {
		t.Fatalf("plan = %+v", plan)
	}
	if question := resolved.AssistantTurn.Questions[0]; question.Text != "Light or dark?" || len(question.Options) != 3 ||
		question.Suggested != 1 {
		t.Fatalf("question = %+v", question)
	}
	if !slices.Contains(OutputJSONSchema(OutputContractAssistantTurn).(map[string]any)["required"].([]string), "questions") {
		t.Fatal("the turn's schema does not ask for questions")
	}
}

// A turn outside planning may suggest plan mode; a planning turn may not.
func TestOnlyATurnOutsidePlanningSuggestsPlanMode(t *testing.T) {
	types := func(contract OutputContract) []string {
		schema := OutputJSONSchema(contract).(map[string]any)
		actions := schema["properties"].(map[string]any)["actions"].(map[string]any)
		action := actions["items"].(map[string]any)["properties"].(map[string]any)
		return action["type"].(map[string]any)["enum"].([]string)
	}
	if !slices.Contains(types(OutputContractAssistantTurn), "start_planning") ||
		slices.Contains(types(OutputContractAssistantPlanningTurn), "start_planning") {
		t.Fatal("start_planning belongs only to turns outside planning")
	}
	resolved, err := ResolveStructuredOutput(OutputContractAssistantTurn, []byte(`{"message":"Let's plan it.","actions":[
		{"type":"start_planning","reason":"Several screens to settle","title":"","description":"A team dashboard"}],"notes":""}`))
	if err != nil || len(resolved.AssistantTurn.Actions) != 1 ||
		resolved.AssistantTurn.Actions[0].Type != featureartifact.AssistantStartPlanning {
		t.Fatalf("resolved = %+v err=%v", resolved, err)
	}
}
