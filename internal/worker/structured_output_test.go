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

func TestResolveStructuredOutput(t *testing.T) {
	tests := []struct {
		name        string
		contract    OutputContract
		raw         string
		event       Event
		disposition Disposition
		publication *ImplementationPublication
		review      *ReviewPublication
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := ResolveStructuredOutput(test.contract, []byte(test.raw))
			if err != nil {
				t.Fatalf("resolve structured output: %v", err)
			}
			if resolved.Event != test.event || resolved.Disposition != test.disposition ||
				!reflect.DeepEqual(resolved.Publication, test.publication) || !reflect.DeepEqual(resolved.Review, test.review) {
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
		{"unknown", `{}`},
	}
	for _, test := range tests {
		if _, err := ResolveStructuredOutput(test.contract, []byte(test.raw)); !errors.Is(err, ErrInvalidStructuredOutput) {
			t.Errorf("contract %q response %q error=%v", test.contract, test.raw, err)
		}
	}
}
