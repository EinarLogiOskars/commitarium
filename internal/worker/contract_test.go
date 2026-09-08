package worker

import (
	"errors"
	"testing"
)

func TestRoleIsValid(t *testing.T) {
	for _, role := range []Role{RoleLead, RoleConsultant, RoleCoder, RoleReviewer} {
		if !role.IsValid() {
			t.Errorf("expected role %q to be valid", role)
		}
	}
	if Role("unknown").IsValid() {
		t.Error("expected unknown role to be invalid")
	}
}

func TestSessionRequestValidate(t *testing.T) {
	valid := SessionRequest{
		SessionID:    "ses_test",
		FeatureID:    "fea_test",
		Role:         RoleCoder,
		Instructions: "Implement the accepted plan.",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}

	tests := map[string]SessionRequest{
		"missing session ID":   {FeatureID: "fea_test", Role: RoleCoder, Instructions: "work"},
		"missing feature ID":   {SessionID: "ses_test", Role: RoleCoder, Instructions: "work"},
		"unknown role":         {SessionID: "ses_test", FeatureID: "fea_test", Role: "unknown", Instructions: "work"},
		"missing instructions": {SessionID: "ses_test", FeatureID: "fea_test", Role: RoleCoder},
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); !errors.Is(err, ErrInvalidSessionRequest) {
				t.Fatalf("expected error %v, got %v", ErrInvalidSessionRequest, err)
			}
		})
	}
}

func TestResumeRequestRequiresProviderSessionID(t *testing.T) {
	request := ResumeRequest{SessionRequest: SessionRequest{
		SessionID:    "ses_test",
		FeatureID:    "fea_test",
		Role:         RoleReviewer,
		Instructions: "Resume the review.",
	}}
	if err := request.Validate(); !errors.Is(err, ErrInvalidSessionRequest) {
		t.Fatalf("expected error %v, got %v", ErrInvalidSessionRequest, err)
	}

	request.ProviderSessionID = "provider_session_test"
	if err := request.Validate(); err != nil {
		t.Fatalf("validate resume request: %v", err)
	}
}

func TestCommandValidate(t *testing.T) {
	tests := []struct {
		name    string
		command Command
		valid   bool
	}{
		{name: "message", command: Command{ID: "cmd_one", Type: CommandMessage, Message: "Please pause after this check."}, valid: true},
		{name: "pause", command: Command{ID: "cmd_two", Type: CommandPause}, valid: true},
		{name: "continue", command: Command{ID: "cmd_three", Type: CommandContinue}, valid: true},
		{name: "stop", command: Command{ID: "cmd_four", Type: CommandStop}, valid: true},
		{name: "missing ID", command: Command{Type: CommandPause}},
		{name: "blank message", command: Command{ID: "cmd_five", Type: CommandMessage}},
		{name: "message on control", command: Command{ID: "cmd_six", Type: CommandPause, Message: "now"}},
		{name: "unknown type", command: Command{ID: "cmd_seven", Type: "unknown"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.command.Validate()
			if test.valid && err != nil {
				t.Fatalf("validate command: %v", err)
			}
			if !test.valid && !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("expected error %v, got %v", ErrInvalidCommand, err)
			}
		})
	}
}

func TestResultValidate(t *testing.T) {
	completed := Result{
		Outcome:           OutcomeCompleted,
		Disposition:       DispositionSucceeded,
		ProviderSessionID: "provider_session_test",
	}
	if err := completed.Validate(); err != nil {
		t.Fatalf("validate completed result: %v", err)
	}
	stopped := Result{
		Outcome:           OutcomeStopped,
		ProviderSessionID: "provider_session_test",
	}
	if err := stopped.Validate(); err != nil {
		t.Fatalf("validate stopped result: %v", err)
	}

	invalid := []Result{
		{Outcome: "unknown", ProviderSessionID: "provider_session_test"},
		{Outcome: OutcomeCompleted, Disposition: DispositionSucceeded},
		{Outcome: OutcomeCompleted, ProviderSessionID: "provider_session_test"},
		{Outcome: OutcomeStopped, Disposition: DispositionSucceeded, ProviderSessionID: "provider_session_test"},
	}
	for _, result := range invalid {
		if err := result.Validate(); !errors.Is(err, ErrInvalidResult) {
			t.Errorf("expected error %v for %+v, got %v", ErrInvalidResult, result, err)
		}
	}
}
