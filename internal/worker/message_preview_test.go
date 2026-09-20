package worker

import "testing"

func TestPreviewProseField(t *testing.T) {
	tests := map[OutputContract]string{
		OutputContractGoalClarification:       "message",
		OutputContractPlanningLead:            "content",
		OutputContractImplementationLead:      "summary",
		OutputContractImplementationReview:    "summary",
		OutputContractImplementationReadiness: "summary",
		OutputContractIntervention:            "response",
		OutputContractToolchainSetup:          "message",
		OutputContract(""):                    "",
	}
	for contract, expected := range tests {
		if actual := PreviewProseField(contract); actual != expected {
			t.Errorf("PreviewProseField(%q) = %q, want %q", contract, actual, expected)
		}
	}
}

func TestExtractMessagePreviewFromTruncatedObject(t *testing.T) {
	tests := []struct {
		name     string
		contract OutputContract
		raw      string
		want     string
	}{
		{name: "partial content", contract: OutputContractPlanningLead,
			raw: `{"action":"respond","content":"I will inspect`, want: "I will inspect"},
		{name: "complete content", contract: OutputContractPlanningLead,
			raw: `{"action":"respond","content":"Done","steps":[]}`, want: "Done"},
		{name: "nested field ignored", contract: OutputContractPlanningLead,
			raw: `{"metadata":{"content":"wrong"},"content":"right`, want: "right"},
		{name: "escaped text", contract: OutputContractGoalClarification,
			raw: `{"action":"ask","message":"Line one\n\"quoted\" and \\ path`, want: "Line one\n\"quoted\" and \\ path"},
		{name: "unicode pair", contract: OutputContractIntervention,
			raw: `{"effect":"guidance_applied","response":"Hi \uD83D\uDE03`, want: "Hi 😃"},
		{name: "incomplete escape withheld", contract: OutputContractImplementationLead,
			raw: `{"action":"blocked","summary":"waiting\`, want: "waiting"},
		{name: "incomplete unicode withheld", contract: OutputContractImplementationLead,
			raw: `{"action":"blocked","summary":"waiting \uD83D`, want: "waiting "},
		{name: "field absent", contract: OutputContractImplementationLead,
			raw: `{"action":"published",`, want: ""},
		{name: "unknown contract", contract: OutputContract("unknown"),
			raw: `{"message":"ignored`, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := ExtractMessagePreview(test.contract, []byte(test.raw)); actual != test.want {
				t.Fatalf("preview = %q, want %q", actual, test.want)
			}
		})
	}
}

func TestExtractMessagePreviewRejectsMalformedPrefix(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`{"content":"bad\q`,
		`{"content":false}`,
		`{"content":"bad\uDE03`,
		`{"other":[1,2},"content":"hidden`,
	} {
		if actual := ExtractMessagePreview(OutputContractPlanningLead, []byte(raw)); actual != "" {
			t.Errorf("ExtractMessagePreview(%q) = %q, want empty", raw, actual)
		}
	}
}

func TestMessagePreviewValidation(t *testing.T) {
	if err := (MessagePreview{StreamID: "item_123", Text: "hello"}).Validate(); err != nil {
		t.Fatalf("validate preview: %v", err)
	}
	for _, preview := range []MessagePreview{
		{Text: "hello"},
		{StreamID: "bad stream", Text: "hello"},
		{StreamID: "item_123"},
	} {
		if err := preview.Validate(); err == nil {
			t.Fatalf("expected invalid preview %+v", preview)
		}
	}
}
