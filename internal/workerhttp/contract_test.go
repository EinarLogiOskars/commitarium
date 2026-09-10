package workerhttp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHealthAndCapabilitiesValidate(t *testing.T) {
	health := HealthResponse{Status: HealthStatusOK, ProtocolVersion: ProtocolVersion}
	if err := health.Validate(); err != nil {
		t.Fatalf("validate health response: %v", err)
	}
	for _, response := range []HealthResponse{
		{Status: "unknown", ProtocolVersion: ProtocolVersion},
		{Status: HealthStatusOK, ProtocolVersion: "v2"},
	} {
		if err := response.Validate(); !errors.Is(err, ErrInvalidContract) {
			t.Errorf("expected error %v for %+v, got %v", ErrInvalidContract, response, err)
		}
	}

	capabilities := CapabilitiesResponse{
		ProtocolVersion: ProtocolVersion,
		Provider:        ProviderCodex,
		Capabilities: []Capability{
			CapabilityStart,
			CapabilityResume,
			CapabilityEventReplay,
		},
		MaxConcurrentAttempts: 1,
	}
	if err := capabilities.Validate(); err != nil {
		t.Fatalf("validate capabilities response: %v", err)
	}

	invalid := []CapabilitiesResponse{
		{ProtocolVersion: "v2", Provider: ProviderCodex, Capabilities: []Capability{CapabilityStart}, MaxConcurrentAttempts: 1},
		{ProtocolVersion: ProtocolVersion, Provider: "unknown", Capabilities: []Capability{CapabilityStart}, MaxConcurrentAttempts: 1},
		{ProtocolVersion: ProtocolVersion, Provider: ProviderCodex, MaxConcurrentAttempts: 1},
		{ProtocolVersion: ProtocolVersion, Provider: ProviderCodex, Capabilities: []Capability{"unknown"}, MaxConcurrentAttempts: 1},
		{ProtocolVersion: ProtocolVersion, Provider: ProviderCodex, Capabilities: []Capability{CapabilityStart, CapabilityStart}, MaxConcurrentAttempts: 1},
		{ProtocolVersion: ProtocolVersion, Provider: ProviderCodex, Capabilities: []Capability{CapabilityStart}},
	}
	for _, response := range invalid {
		if err := response.Validate(); !errors.Is(err, ErrInvalidContract) {
			t.Errorf("expected error %v for %+v, got %v", ErrInvalidContract, response, err)
		}
	}
}

func TestPutAttemptRequestValidate(t *testing.T) {
	identity := validMutationIdentity()
	start := PutAttemptRequest{
		Mode:         AttemptModeStart,
		Assignment:   validAssignment(),
		Instructions: "Implement the accepted plan.",
	}
	if err := start.Validate(identity); err != nil {
		t.Fatalf("validate start request: %v", err)
	}

	resume := start
	resume.Mode = AttemptModeResume
	resume.ProviderSessionID = "provider_session_test"
	resume.Instructions = "Reconcile durable state before continuing."
	if err := resume.Validate(identity); err != nil {
		t.Fatalf("validate resume request: %v", err)
	}

	tests := []struct {
		name     string
		identity MutationIdentity
		request  PutAttemptRequest
	}{
		{name: "missing session ID", identity: MutationIdentity{AttemptReference: AttemptReference{AttemptID: "att_test"}, IdempotencyKey: "idem_test"}, request: start},
		{name: "unsafe attempt ID", identity: MutationIdentity{AttemptReference: AttemptReference{SessionID: "ses_test", AttemptID: "att/test"}, IdempotencyKey: "idem_test"}, request: start},
		{name: "missing idempotency key", identity: MutationIdentity{AttemptReference: AttemptReference{SessionID: "ses_test", AttemptID: "att_test"}}, request: start},
		{name: "unknown mode", identity: identity, request: withAttemptMode(start, "unknown")},
		{name: "invalid assignment", identity: identity, request: withAssignment(start, Assignment{})},
		{name: "missing instructions", identity: identity, request: withInstructions(start, " ")},
		{name: "unknown output contract", identity: identity, request: func() PutAttemptRequest { invalid := start; invalid.OutputContract = "unknown"; return invalid }()},
		{name: "planning output for reviewer", identity: identity, request: func() PutAttemptRequest {
			invalid := start
			invalid.OutputContract = OutputContractPlanningLead
			invalid.Assignment.Role = RoleReviewer
			return invalid
		}()},
		{name: "instructions too large", identity: identity, request: withInstructions(start, strings.Repeat("x", MaxInstructionsBytes+1))},
		{name: "start with provider session", identity: identity, request: withProviderSessionID(start, "provider_session_test")},
		{name: "resume without provider session", identity: identity, request: withAttemptMode(start, AttemptModeResume)},
		{name: "provider session too large", identity: identity, request: withProviderSessionID(withAttemptMode(start, AttemptModeResume), strings.Repeat("x", maxProviderSessionIDBytes+1))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(test.identity); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
			}
		})
	}
}

func TestAttemptStateTransitionsAndFencing(t *testing.T) {
	allowed := map[AttemptState][]AttemptState{
		AttemptStateStarting: {
			AttemptStateRunning,
			AttemptStateStopRequested,
			AttemptStateTerminal,
			AttemptStateIndeterminate,
		},
		AttemptStateRunning: {
			AttemptStatePauseRequested,
			AttemptStateStopRequested,
			AttemptStateTerminal,
			AttemptStateIndeterminate,
		},
		AttemptStatePauseRequested: {
			AttemptStatePaused,
			AttemptStateRunning,
			AttemptStateStopRequested,
			AttemptStateTerminal,
			AttemptStateIndeterminate,
		},
		AttemptStatePaused: {
			AttemptStateRunning,
			AttemptStateStopRequested,
			AttemptStateTerminal,
			AttemptStateIndeterminate,
		},
		AttemptStateStopRequested: {AttemptStateTerminal, AttemptStateIndeterminate},
		AttemptStateIndeterminate: {AttemptStateTerminal},
		AttemptStateTerminal:      {},
	}

	states := []AttemptState{
		AttemptStateStarting,
		AttemptStateRunning,
		AttemptStatePauseRequested,
		AttemptStatePaused,
		AttemptStateStopRequested,
		AttemptStateTerminal,
		AttemptStateIndeterminate,
	}
	for _, current := range states {
		for _, next := range states {
			want := containsState(allowed[current], next)
			if got := current.CanTransitionTo(next); got != want {
				t.Errorf("CanTransitionTo(%q, %q) = %t, want %t", current, next, got, want)
			}
		}
	}

	for _, state := range states {
		want := state != AttemptStateTerminal
		if got := state.MayStillBeActive(); got != want {
			t.Errorf("MayStillBeActive(%q) = %t, want %t", state, got, want)
		}
	}
	if AttemptState("unknown").MayStillBeActive() {
		t.Error("unknown state must not be treated as a valid active state")
	}
}

func TestAttemptValidate(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	running := Attempt{
		AttemptReference:    validAttemptReference(),
		Mode:                AttemptModeStart,
		Assignment:          validAssignment(),
		ProviderSessionID:   "provider_session_test",
		State:               AttemptStateRunning,
		LatestEventSequence: 3,
		StartedAt:           now,
		UpdatedAt:           now.Add(time.Minute),
	}
	if err := running.Validate(); err != nil {
		t.Fatalf("validate running attempt: %v", err)
	}

	starting := running
	starting.State = AttemptStateStarting
	starting.ProviderSessionID = ""
	if err := starting.Validate(); err != nil {
		t.Fatalf("validate starting attempt before provider identity: %v", err)
	}

	indeterminate := starting
	indeterminate.State = AttemptStateIndeterminate
	if err := indeterminate.Validate(); err != nil {
		t.Fatalf("validate indeterminate pre-session attempt: %v", err)
	}

	endedAt := now.Add(2 * time.Minute)
	terminal := running
	terminal.State = AttemptStateTerminal
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &TerminalResult{
		Outcome:     OutcomeCompleted,
		Disposition: DispositionSucceeded,
		Summary:     "Implementation completed.",
	}
	if err := terminal.Validate(); err != nil {
		t.Fatalf("validate terminal attempt: %v", err)
	}

	failedBeforeStart := starting
	failedBeforeStart.State = AttemptStateTerminal
	failedBeforeStart.UpdatedAt = endedAt
	failedBeforeStart.EndedAt = &endedAt
	failedBeforeStart.Result = &TerminalResult{
		Outcome: OutcomeFailed,
		Summary: "provider process failed before creating a session",
		Error:   &ProtocolError{Code: ErrorProfileUnavailable, Message: "authentication is unavailable"},
	}
	if err := failedBeforeStart.Validate(); err != nil {
		t.Fatalf("validate failed attempt before provider identity: %v", err)
	}

	resuming := starting
	resuming.Mode = AttemptModeResume
	if err := resuming.Validate(); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("expected resume without provider identity to fail with %v, got %v", ErrInvalidContract, err)
	}

	localTime := time.Date(2026, 9, 8, 12, 0, 0, 0, time.FixedZone("local", 3600))
	tests := []struct {
		name    string
		attempt Attempt
	}{
		{name: "missing reference", attempt: withAttemptReference(running, AttemptReference{})},
		{name: "unknown mode", attempt: withStoredAttemptMode(running, "unknown")},
		{name: "invalid assignment", attempt: withStoredAssignment(running, Assignment{})},
		{name: "unknown state", attempt: withAttemptState(running, "unknown")},
		{name: "negative event sequence", attempt: withEventSequence(running, -1)},
		{name: "running without provider session", attempt: withStoredProviderSessionID(running, "")},
		{name: "nonterminal with result", attempt: withResult(running, terminal.Result)},
		{name: "nonterminal with end time", attempt: withEndTime(running, &endedAt)},
		{name: "completed without provider session", attempt: withStoredProviderSessionID(terminal, "")},
		{name: "terminal without result", attempt: withResult(terminal, nil)},
		{name: "terminal without end time", attempt: withEndTime(terminal, nil)},
		{name: "non UTC start time", attempt: withStartTime(running, localTime)},
		{name: "non UTC update time", attempt: withUpdateTime(running, localTime)},
		{name: "non UTC end time", attempt: withEndTime(terminal, &localTime)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.attempt.Validate(); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
			}
		})
	}
}

func TestTerminalResultValidate(t *testing.T) {
	valid := []TerminalResult{
		{Outcome: OutcomeCompleted, Disposition: DispositionSucceeded, Summary: "done"},
		{Outcome: OutcomeCompleted, Disposition: DispositionSucceeded, Summary: "published", Publication: &ImplementationPublication{CommitID: "0123456789abcdef0123456789abcdef01234567", PullRequestNumber: 7}},
		{Outcome: OutcomeCompleted, Disposition: DispositionChangesRequested, Summary: "changes requested"},
		{Outcome: OutcomeStopped, Summary: "stopped safely"},
		{Outcome: OutcomeFailed, Summary: "provider unavailable", Error: &ProtocolError{Code: ErrorProfileUnavailable, Message: "profile is unavailable", Retryable: true}},
	}
	for _, result := range valid {
		if err := result.Validate(); err != nil {
			t.Errorf("validate result %+v: %v", result, err)
		}
	}

	invalid := []TerminalResult{
		{Outcome: "unknown", Summary: "bad"},
		{Outcome: OutcomeCompleted, Summary: "missing disposition"},
		{Outcome: OutcomeCompleted, Disposition: DispositionSucceeded, Summary: "bad", Error: &ProtocolError{Code: ErrorInternal, Message: "unexpected", Retryable: true}},
		{Outcome: OutcomeStopped, Disposition: DispositionSucceeded, Summary: "bad"},
		{Outcome: OutcomeFailed, Disposition: DispositionSucceeded, Summary: "bad", Error: &ProtocolError{Code: ErrorInternal, Message: "unexpected", Retryable: true}},
		{Outcome: OutcomeFailed, Summary: "missing error"},
		{Outcome: OutcomeFailed, Summary: "bad error", Error: &ProtocolError{Code: "unknown", Message: "unexpected"}},
		{Outcome: OutcomeCompleted, Disposition: DispositionSucceeded, Summary: "bad publication", Publication: &ImplementationPublication{CommitID: "bad", PullRequestNumber: 7}},
		{Outcome: OutcomeCompleted, Disposition: DispositionInputRequired, Summary: "blocked", Publication: &ImplementationPublication{CommitID: "0123456789abcdef0123456789abcdef01234567", PullRequestNumber: 7}},
	}
	for _, result := range invalid {
		if err := result.Validate(); !errors.Is(err, ErrInvalidContract) {
			t.Errorf("expected error %v for %+v, got %v", ErrInvalidContract, result, err)
		}
	}
}

func TestCommandsValidate(t *testing.T) {
	identity := validMutationIdentity()
	tests := []struct {
		name    string
		request CommandRequest
		valid   bool
	}{
		{name: "message", request: CommandRequest{Type: CommandMessage, Message: "Please inspect the failing test."}, valid: true},
		{name: "pause", request: CommandRequest{Type: CommandPause}, valid: true},
		{name: "continue", request: CommandRequest{Type: CommandContinue}, valid: true},
		{name: "stop", request: CommandRequest{Type: CommandStop}, valid: true},
		{name: "unknown", request: CommandRequest{Type: "unknown"}},
		{name: "blank message", request: CommandRequest{Type: CommandMessage}},
		{name: "message on control", request: CommandRequest{Type: CommandPause, Message: "now"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate(identity)
			if test.valid && err != nil {
				t.Fatalf("validate command: %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
			}
		})
	}

	forceStop := ForceStopRequest{Reason: "cooperative stop deadline expired"}
	if err := forceStop.Validate(identity); err != nil {
		t.Fatalf("validate force stop: %v", err)
	}
	if err := (ForceStopRequest{}).Validate(identity); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
	}
}

func TestEventValidate(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	valid := Event{
		AttemptReference: validAttemptReference(),
		Sequence:         1,
		Type:             EventActivity,
		Text:             "running tests",
		OccurredAt:       now,
		Redaction:        RedactionMetadata{},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate event: %v", err)
	}

	redacted := valid
	redacted.Sequence = 2
	redacted.Text = "token=[REDACTED]"
	redacted.Redaction = RedactionMetadata{Count: 1, Categories: []RedactionCategory{RedactionCredential}}
	if err := redacted.Validate(); err != nil {
		t.Fatalf("validate redacted event: %v", err)
	}

	assessment := valid
	assessment.Type = EventRecoveryAssessment
	assessment.RecoveryAssessment = &RecoveryAssessment{Consistent: true}
	if err := assessment.Validate(); err != nil {
		t.Fatalf("validate recovery assessment: %v", err)
	}

	truncated := valid
	truncated.Truncation = &TruncationMetadata{OriginalBytes: 100_000, RetainedBytes: 60_000}
	if err := truncated.Validate(); err != nil {
		t.Fatalf("validate truncated event: %v", err)
	}

	localTime := time.Date(2026, 9, 8, 12, 0, 0, 0, time.FixedZone("local", 3600))
	tests := []struct {
		name  string
		event Event
	}{
		{name: "missing reference", event: withEventReference(valid, AttemptReference{})},
		{name: "nonpositive sequence", event: withSequence(valid, 0)},
		{name: "unknown type", event: withEventType(valid, "unknown")},
		{name: "blank text", event: withEventText(valid, " ")},
		{name: "oversized text", event: withEventText(valid, strings.Repeat("x", MaxEventTextBytes+1))},
		{name: "missing occurrence time", event: withOccurredAt(valid, time.Time{})},
		{name: "non UTC occurrence time", event: withOccurredAt(valid, localTime)},
		{name: "negative redaction count", event: withRedaction(valid, RedactionMetadata{Count: -1})},
		{name: "categories without count", event: withRedaction(valid, RedactionMetadata{Categories: []RedactionCategory{RedactionCredential}})},
		{name: "count without categories", event: withRedaction(valid, RedactionMetadata{Count: 1})},
		{name: "unknown redaction category", event: withRedaction(valid, RedactionMetadata{Count: 1, Categories: []RedactionCategory{"unknown"}})},
		{name: "duplicate redaction category", event: withRedaction(valid, RedactionMetadata{Count: 2, Categories: []RedactionCategory{RedactionCredential, RedactionCredential}})},
		{name: "invalid truncation", event: withTruncation(valid, &TruncationMetadata{OriginalBytes: 10, RetainedBytes: 10})},
		{name: "missing recovery assessment", event: withEventType(valid, EventRecoveryAssessment)},
		{name: "assessment on wrong event", event: withRecoveryAssessment(valid, &RecoveryAssessment{Consistent: true})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.event.Validate(); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("expected error %v, got %v", ErrInvalidContract, err)
			}
		})
	}
}

func TestJSONContract(t *testing.T) {
	request := PutAttemptRequest{
		Mode:              AttemptModeResume,
		Assignment:        validAssignment(),
		Instructions:      "Reconcile durable state.",
		OutputContract:    OutputContractPlanningLead,
		ProviderSessionID: "provider_session_test",
	}
	request.Assignment.Role = RoleLead
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	want := `{"mode":"resume","assignment":{"agent_profile_id":"apr_test","project_id":"prj_test","feature_id":"fea_test","role":"lead","workspace_id":"wsp_test"},"instructions":"Reconcile durable state.","output_contract":"planning_lead","provider_session_id":"provider_session_test"}`
	if string(encoded) != want {
		t.Fatalf("request JSON\n got: %s\nwant: %s", encoded, want)
	}

	var decoded PutAttemptRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if err := decoded.Validate(validMutationIdentity()); err != nil {
		t.Fatalf("validate round-tripped request: %v", err)
	}

	now := time.Date(2026, 9, 8, 12, 0, 0, 123_000_000, time.UTC)
	event := Event{
		AttemptReference: validAttemptReference(),
		Sequence:         7,
		Type:             EventRecoveryAssessment,
		Text:             "durable state is consistent",
		OccurredAt:       now,
		Redaction: RedactionMetadata{
			Count:      1,
			Categories: []RedactionCategory{RedactionInternalPath},
		},
		RecoveryAssessment: &RecoveryAssessment{
			Consistent:         true,
			RequiresUserReview: false,
		},
	}
	encoded, err = json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	want = `{"session_id":"ses_test","attempt_id":"att_test","sequence":7,"type":"recovery_assessment","text":"durable state is consistent","occurred_at":"2026-09-08T12:00:00.123Z","redaction":{"count":1,"categories":["internal_path"]},"recovery_assessment":{"consistent":true,"requires_user_review":false}}`
	if string(encoded) != want {
		t.Fatalf("event JSON\n got: %s\nwant: %s", encoded, want)
	}

	endedAt := now.Add(time.Minute)
	attempt := Attempt{
		AttemptReference:    validAttemptReference(),
		Mode:                AttemptModeStart,
		Assignment:          validAssignment(),
		ProviderSessionID:   "provider_session_test",
		State:               AttemptStateTerminal,
		LatestEventSequence: 7,
		StartedAt:           now,
		UpdatedAt:           endedAt,
		EndedAt:             &endedAt,
		Result: &TerminalResult{
			Outcome:     OutcomeCompleted,
			Disposition: DispositionSucceeded,
			Summary:     "done",
		},
	}
	encoded, err = json.Marshal(attempt)
	if err != nil {
		t.Fatalf("marshal attempt: %v", err)
	}
	want = `{"session_id":"ses_test","attempt_id":"att_test","mode":"start","assignment":{"agent_profile_id":"apr_test","project_id":"prj_test","feature_id":"fea_test","role":"coder","workspace_id":"wsp_test"},"provider_session_id":"provider_session_test","state":"terminal","latest_event_sequence":7,"started_at":"2026-09-08T12:00:00.123Z","updated_at":"2026-09-08T12:01:00.123Z","ended_at":"2026-09-08T12:01:00.123Z","result":{"outcome":"completed","disposition":"succeeded","summary":"done"}}`
	if string(encoded) != want {
		t.Fatalf("attempt JSON\n got: %s\nwant: %s", encoded, want)
	}
	var decodedAttempt Attempt
	if err := json.Unmarshal(encoded, &decodedAttempt); err != nil {
		t.Fatalf("unmarshal attempt: %v", err)
	}
	if err := decodedAttempt.Validate(); err != nil {
		t.Fatalf("validate round-tripped attempt: %v", err)
	}
}

func TestProtocolErrorValidateAndJSON(t *testing.T) {
	response := ErrorResponse{Error: ProtocolError{
		Code:      ErrorStaleAttempt,
		Message:   "the attempt is no longer current",
		Retryable: false,
	}}
	if err := response.Validate(); err != nil {
		t.Fatalf("validate error response: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	want := `{"error":{"code":"stale_attempt","message":"the attempt is no longer current","retryable":false}}`
	if string(encoded) != want {
		t.Fatalf("error JSON\n got: %s\nwant: %s", encoded, want)
	}

	invalid := []ProtocolError{
		{Code: "unknown", Message: "bad"},
		{Code: ErrorInternal},
		{Code: ErrorInternal, Message: strings.Repeat("x", maxErrorMessageBytes+1)},
	}
	for _, protocolError := range invalid {
		if err := protocolError.Validate(); !errors.Is(err, ErrInvalidContract) {
			t.Errorf("expected error %v for %+v, got %v", ErrInvalidContract, protocolError, err)
		}
	}
}

func validAttemptReference() AttemptReference {
	return AttemptReference{SessionID: "ses_test", AttemptID: "att_test"}
}

func validMutationIdentity() MutationIdentity {
	return MutationIdentity{
		AttemptReference: validAttemptReference(),
		IdempotencyKey:   "idem_test",
	}
}

func validAssignment() Assignment {
	return Assignment{
		AgentProfileID: "apr_test",
		ProjectID:      "prj_test",
		FeatureID:      "fea_test",
		Role:           RoleCoder,
		WorkspaceID:    "wsp_test",
	}
}

func containsState(states []AttemptState, target AttemptState) bool {
	for _, state := range states {
		if state == target {
			return true
		}
	}
	return false
}

func withAttemptMode(request PutAttemptRequest, mode AttemptMode) PutAttemptRequest {
	request.Mode = mode
	return request
}

func withAssignment(request PutAttemptRequest, assignment Assignment) PutAttemptRequest {
	request.Assignment = assignment
	return request
}

func withInstructions(request PutAttemptRequest, instructions string) PutAttemptRequest {
	request.Instructions = instructions
	return request
}

func withProviderSessionID(request PutAttemptRequest, providerSessionID string) PutAttemptRequest {
	request.ProviderSessionID = providerSessionID
	return request
}

func withAttemptReference(attempt Attempt, reference AttemptReference) Attempt {
	attempt.AttemptReference = reference
	return attempt
}

func withStoredAttemptMode(attempt Attempt, mode AttemptMode) Attempt {
	attempt.Mode = mode
	return attempt
}

func withStoredAssignment(attempt Attempt, assignment Assignment) Attempt {
	attempt.Assignment = assignment
	return attempt
}

func withAttemptState(attempt Attempt, state AttemptState) Attempt {
	attempt.State = state
	return attempt
}

func withEventSequence(attempt Attempt, sequence int64) Attempt {
	attempt.LatestEventSequence = sequence
	return attempt
}

func withStoredProviderSessionID(attempt Attempt, providerSessionID string) Attempt {
	attempt.ProviderSessionID = providerSessionID
	return attempt
}

func withResult(attempt Attempt, result *TerminalResult) Attempt {
	attempt.Result = result
	return attempt
}

func withEndTime(attempt Attempt, endedAt *time.Time) Attempt {
	attempt.EndedAt = endedAt
	return attempt
}

func withStartTime(attempt Attempt, startedAt time.Time) Attempt {
	attempt.StartedAt = startedAt
	return attempt
}

func withUpdateTime(attempt Attempt, updatedAt time.Time) Attempt {
	attempt.UpdatedAt = updatedAt
	return attempt
}

func withEventReference(event Event, reference AttemptReference) Event {
	event.AttemptReference = reference
	return event
}

func withSequence(event Event, sequence int64) Event {
	event.Sequence = sequence
	return event
}

func withEventType(event Event, eventType EventType) Event {
	event.Type = eventType
	return event
}

func withEventText(event Event, text string) Event {
	event.Text = text
	return event
}

func withOccurredAt(event Event, occurredAt time.Time) Event {
	event.OccurredAt = occurredAt
	return event
}

func withRedaction(event Event, metadata RedactionMetadata) Event {
	event.Redaction = metadata
	return event
}

func withTruncation(event Event, metadata *TruncationMetadata) Event {
	event.Truncation = metadata
	return event
}

func withRecoveryAssessment(event Event, assessment *RecoveryAssessment) Event {
	event.RecoveryAssessment = assessment
	return event
}
