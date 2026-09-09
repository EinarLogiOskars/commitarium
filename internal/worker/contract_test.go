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
		AttemptID:    "att_test",
		FeatureID:    "fea_test",
		Role:         RoleCoder,
		Instructions: "Implement the accepted plan.",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}

	tests := map[string]SessionRequest{
		"missing session ID":   {AttemptID: "att_test", FeatureID: "fea_test", Role: RoleCoder, Instructions: "work"},
		"missing attempt ID":   {SessionID: "ses_test", FeatureID: "fea_test", Role: RoleCoder, Instructions: "work"},
		"missing feature ID":   {SessionID: "ses_test", AttemptID: "att_test", Role: RoleCoder, Instructions: "work"},
		"unknown role":         {SessionID: "ses_test", AttemptID: "att_test", FeatureID: "fea_test", Role: "unknown", Instructions: "work"},
		"missing instructions": {SessionID: "ses_test", AttemptID: "att_test", FeatureID: "fea_test", Role: RoleCoder},
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
		AttemptID:    "att_test",
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

func TestLaunchEnvironmentValidationAndCopying(t *testing.T) {
	environment := validLaunchEnvironment(t)
	request := SessionRequest{
		SessionID: "ses_test", AttemptID: "att_test", FeatureID: "fea_test",
		Role: RoleCoder, Instructions: "Implement the plan.", LaunchEnvironment: environment,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("validate request with launch environment: %v", err)
	}

	clone := request.Clone()
	clone.LaunchEnvironment.Variables[0] = "PATH=/changed"
	if request.LaunchEnvironment.Variables[0] != "PATH=/usr/bin:/bin" || request.Equal(clone) {
		t.Fatalf("request clone shared or ignored environment variables: original=%+v clone=%+v", request, clone)
	}
	empty := environment
	empty.Variables = []string{}
	if cloned := empty.Clone(); cloned.Variables == nil {
		t.Fatal("an explicit empty environment became nil and would inherit worker variables")
	}

	invalid := []LaunchEnvironment{
		withLaunchProfile(environment, ""),
		withLaunchDirectory(environment, "relative/workspace"),
		withLaunchVariables(environment, nil),
		withLaunchVariables(environment, []string{"NOT AN ENVIRONMENT ENTRY"}),
		withLaunchVariables(environment, []string{"PATH=/one", "PATH=/two"}),
	}
	for _, candidate := range invalid {
		if err := candidate.Validate(); !errors.Is(err, ErrInvalidLaunchEnvironment) {
			t.Errorf("environment %+v error = %v, want ErrInvalidLaunchEnvironment", candidate, err)
		}
	}

	mismatchedFeature := request
	mismatchedFeature.LaunchEnvironment.FeatureID = "fea_other"
	if err := mismatchedFeature.Validate(); !errors.Is(err, ErrInvalidSessionRequest) {
		t.Fatalf("mismatched feature error = %v", err)
	}
	mismatchedRole := request
	mismatchedRole.LaunchEnvironment.Role = RoleReviewer
	if err := mismatchedRole.Validate(); !errors.Is(err, ErrInvalidSessionRequest) {
		t.Fatalf("mismatched role error = %v", err)
	}
}

func validLaunchEnvironment(t *testing.T) LaunchEnvironment {
	t.Helper()
	return LaunchEnvironment{
		AgentProfileID: "profile_test", ProjectID: "prj_test", FeatureID: "fea_test",
		Role: RoleCoder, WorkspaceID: "workspace_test",
		WorkingDirectory: t.TempDir(),
		Variables:        []string{"PATH=/usr/bin:/bin", "CODEX_HOME=/var/lib/commitarium/provider"},
	}
}

func withLaunchProfile(environment LaunchEnvironment, profileID string) LaunchEnvironment {
	environment.AgentProfileID = profileID
	return environment
}

func withLaunchDirectory(environment LaunchEnvironment, directory string) LaunchEnvironment {
	environment.WorkingDirectory = directory
	return environment
}

func withLaunchVariables(environment LaunchEnvironment, variables []string) LaunchEnvironment {
	environment.Variables = variables
	return environment
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
	failed := Result{
		Outcome:           OutcomeFailed,
		ProviderSessionID: "provider_session_test",
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("validate failed result: %v", err)
	}

	invalid := []Result{
		{Outcome: "unknown", ProviderSessionID: "provider_session_test"},
		{Outcome: OutcomeCompleted, Disposition: DispositionSucceeded},
		{Outcome: OutcomeCompleted, ProviderSessionID: "provider_session_test"},
		{Outcome: OutcomeStopped, Disposition: DispositionSucceeded, ProviderSessionID: "provider_session_test"},
		{Outcome: OutcomeFailed, Disposition: DispositionSucceeded, ProviderSessionID: "provider_session_test"},
	}
	for _, result := range invalid {
		if err := result.Validate(); !errors.Is(err, ErrInvalidResult) {
			t.Errorf("expected error %v for %+v, got %v", ErrInvalidResult, result, err)
		}
	}
}
