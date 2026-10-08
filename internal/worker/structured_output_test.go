package worker

import (
	"errors"
	"reflect"
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
		{OutputContractImplementationLead, []string{"action", "summary", "commit_id", "pull_request_number", "system_packages", "environment_reason"}},
		{OutputContractAcceptanceTests, []string{"action", "summary", "test_commit_id", "tests"}},
		{OutputContractImplementationReview, []string{"action", "summary", "commit_id", "pull_request_number", "review_id"}},
		{OutputContractImplementationReadiness, []string{"action", "summary"}},
		{OutputContractIntervention, []string{"effect", "response"}},
		{OutputContractToolchainSetup, []string{"action", "message", "tools", "services", "run"}},
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
			raw:   `{"action":"propose","message":"Use Python.","tools":[{"name":"python","version":"3.14.7"}],"services":[],"run":{"setup":[],"processes":[{"name":"web","command":"python -m http.server 8000 --bind 0.0.0.0","port":8000,"open":true}]}}`,
			event: Event{Type: EventMessage, Text: "Use Python."}, disposition: DispositionSucceeded,
			proposal: &ToolchainProposal{Tools: map[string]string{"python": "3.14.7"}, Services: []string{},
				Run: &ToolchainRunConfig{Setup: []string{}, Processes: []ToolchainRunProcess{{Name: "web", Command: "python -m http.server 8000 --bind 0.0.0.0", Port: intPointer(8000), Open: true}}}},
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

func intPointer(value int) *int { return &value }

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
