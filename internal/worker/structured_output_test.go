package worker

import (
	"errors"
	"reflect"
	"testing"
)

func TestOutputJSONSchemaCoversStructuredContracts(t *testing.T) {
	tests := []struct {
		contract OutputContract
		required []string
	}{
		{OutputContractPlanningLead, []string{"action", "content"}},
		{OutputContractImplementationLead, []string{"action", "summary", "commit_id", "pull_request_number"}},
		{OutputContractImplementationReview, []string{"action", "summary", "commit_id", "pull_request_number", "review_id"}},
		{OutputContractImplementationReadiness, []string{"action", "summary"}},
		{OutputContractIntervention, []string{"effect", "response"}},
		{OutputContractToolchainSetup, []string{"action", "message", "tools", "services"}},
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
	}{
		{
			name: "planning response", contract: OutputContractPlanningLead,
			raw:   `{"action":"respond","content":"One detail remains."}`,
			event: Event{Type: EventMessage, Text: "One detail remains."}, disposition: DispositionSucceeded,
		},
		{
			name: "plan submission", contract: OutputContractPlanningLead,
			raw:   `{"action":"submit_plan","content":"Final agreed plan"}`,
			event: Event{Type: EventPlanSubmitted, Text: "Final agreed plan"}, disposition: DispositionSucceeded,
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
			raw:   `{"action":"propose","message":"Use Python.","tools":[{"name":"python","version":"3.14.7"}],"services":[]}`,
			event: Event{Type: EventMessage, Text: "Use Python."}, disposition: DispositionSucceeded,
			proposal: &ToolchainProposal{Tools: map[string]string{"python": "3.14.7"}, Services: []string{}},
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
				!reflect.DeepEqual(resolved.ToolchainProposal, test.proposal) {
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
