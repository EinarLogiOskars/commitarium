//go:build darwin || linux

package claudeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/processsupervisor"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

const (
	testSessionID              = "123e4567-e89b-42d3-a456-426614174000"
	helperModeEnvironment      = "COMMITARIUM_CLAUDE_ADAPTER_HELPER"
	helperPromptEnvironment    = "COMMITARIUM_CLAUDE_ADAPTER_PROMPT"
	helperDirectoryEnvironment = "COMMITARIUM_CLAUDE_ADAPTER_DIRECTORY"
	helperVisibleEnvironment   = "COMMITARIUM_CLAUDE_ADAPTER_VISIBLE"
)

func TestAdapterStartsClaudeAndTranslatesObservableActivity(t *testing.T) {
	adapter := testAdapter(t, "success", "Implement the approved change")
	session, err := adapter.Start(t.Context(), adapter.request("att_claude_start", "Implement the approved change"))
	if err != nil {
		t.Fatalf("start Claude adapter: %v", err)
	}
	if session.ProviderSessionID() != testSessionID {
		t.Fatalf("provider session ID = %q, want %q", session.ProviderSessionID(), testSessionID)
	}
	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil {
		t.Fatalf("wait for Claude adapter: %v", err)
	}
	if result.Outcome != worker.OutcomeCompleted ||
		result.Disposition != worker.DispositionSucceeded ||
		result.ProviderSessionID != testSessionID ||
		result.Summary != "Implemented and verified the change." {
		t.Fatalf("Claude result = %+v", result)
	}
	wantEvents := []worker.Event{
		{Type: worker.EventActivity, Text: "Claude started working."},
		{Type: worker.EventActivity, Text: "Claude started a command or tool."},
		{Type: worker.EventActivity, Text: "Claude finished a command or tool."},
		{Type: worker.EventMessage, Text: "Implemented and verified the change."},
	}
	if observed := <-events; !slices.Equal(observed, wantEvents) {
		t.Fatalf("observable events = %+v, want %+v", observed, wantEvents)
	}
}

func TestAdapterExposesGeneratedIdentityBeforeClaudeInitializes(t *testing.T) {
	adapter := testAdapter(t, "identity-before-init", "Wait before initialization")
	providerSession, err := adapter.Start(t.Context(), adapter.request(
		"att_claude_early_identity",
		"Wait before initialization",
	))
	if err != nil {
		t.Fatalf("start Claude adapter: %v", err)
	}
	if providerSession.ProviderSessionID() != testSessionID {
		t.Fatalf("early provider session ID = %q, want %q", providerSession.ProviderSessionID(), testSessionID)
	}
	session := providerSession.(worker.ForceStoppableSession)
	go func() {
		for range session.Events() {
		}
	}()
	if err := session.ForceStop(timeoutContext(t, 3*time.Second), "end identity test"); err != nil {
		t.Fatalf("force-stop identity test: %v", err)
	}
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Outcome != worker.OutcomeStopped || result.ProviderSessionID != testSessionID {
		t.Fatalf("identity test result=%+v error=%v", result, err)
	}
}

func TestAdapterEmitsResultTextWhenAssistantRecordIsMissing(t *testing.T) {
	adapter := testAdapter(t, "result-only", "Return one answer")
	session, err := adapter.Start(t.Context(), adapter.request("att_claude_result_only", "Return one answer"))
	if err != nil {
		t.Fatalf("start result-only Claude adapter: %v", err)
	}
	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Summary != "Result-only answer." {
		t.Fatalf("result-only result=%+v error=%v", result, err)
	}
	if observed := <-events; !slices.Equal(observed, []worker.Event{
		{Type: worker.EventActivity, Text: "Claude started working."},
		{Type: worker.EventMessage, Text: "Result-only answer."},
	}) {
		t.Fatalf("result-only events = %+v", observed)
	}
}

func TestAdapterResumesExactClaudeSessionWithRecoveryBriefing(t *testing.T) {
	adapter := testAdapter(t, "resume", "Inspect durable state before continuing")
	session, err := adapter.Resume(t.Context(), worker.ResumeRequest{
		SessionRequest:    adapter.request("att_claude_resume", "original instructions"),
		ProviderSessionID: testSessionID,
		Recovery: worker.RecoveryContext{
			Briefing: "Inspect durable state before continuing",
		},
	})
	if err != nil {
		t.Fatalf("resume Claude adapter: %v", err)
	}
	go func() {
		for range session.Events() {
		}
	}()
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.ProviderSessionID != testSessionID {
		t.Fatalf("resumed result=%+v error=%v", result, err)
	}
}

func TestAdapterPublishesStructuredPlanningSubmission(t *testing.T) {
	adapter := testAdapter(t, "structured-plan", "Consider the reviewer's response")
	request := adapter.request("att_claude_plan", "Consider the reviewer's response")
	request.Role = worker.RoleLead
	request.LaunchEnvironment.Role = worker.RoleLead
	request.OutputContract = worker.OutputContractPlanningLead
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start structured Claude planning turn: %v", err)
	}
	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Summary != "Final agreed plan" {
		t.Fatalf("structured planning result=%+v error=%v", result, err)
	}
	if observed := <-events; !slices.Equal(observed, []worker.Event{
		{Type: worker.EventActivity, Text: "Claude started working."},
		{Type: worker.EventPlanSubmitted, Text: "Final agreed plan"},
	}) {
		t.Fatalf("structured planning events = %+v", observed)
	}
}

func TestAdapterReturnsStructuredImplementationReview(t *testing.T) {
	adapter := testAdapter(t, "structured-review", "Review the exact implementation commit")
	request := adapter.request("att_claude_review", "Review the exact implementation commit")
	request.Role = worker.RoleReviewer
	request.LaunchEnvironment.Role = worker.RoleReviewer
	request.OutputContract = worker.OutputContractImplementationReview
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start structured Claude review: %v", err)
	}
	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Disposition != worker.DispositionChangesRequested || result.Review == nil ||
		result.Review.CommitID != "0123456789abcdef0123456789abcdef01234567" ||
		result.Review.PullRequestNumber != 7 || result.Review.ReviewID != 11 {
		t.Fatalf("structured review result=%+v error=%v", result, err)
	}
	if observed := <-events; !slices.Equal(observed, []worker.Event{
		{Type: worker.EventActivity, Text: "Claude started working."},
		{Type: worker.EventMessage, Text: "The implementation misses the documented failure case."},
	}) {
		t.Fatalf("structured review events = %+v", observed)
	}
}

func TestAdapterMapsExplicitTurnFailure(t *testing.T) {
	adapter := testAdapter(t, "failed", "Fail deterministically")
	session, err := adapter.Start(t.Context(), adapter.request("att_claude_failed", "Fail deterministically"))
	if err != nil {
		t.Fatalf("start failing Claude adapter: %v", err)
	}
	go func() {
		for range session.Events() {
		}
	}()
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Outcome != worker.OutcomeFailed || result.Summary != "controlled failure" {
		t.Fatalf("failed result=%+v error=%v", result, err)
	}
}

func TestAdapterFailsClosedOnInvalidClaudeProtocol(t *testing.T) {
	for _, mode := range []string{"malformed", "mismatch", "missing-structured-output"} {
		t.Run(mode, func(t *testing.T) {
			adapter := testAdapter(t, mode, "Observe invalid output")
			request := adapter.request("att_claude_"+strings.ReplaceAll(mode, "-", "_"), "Observe invalid output")
			if mode == "missing-structured-output" {
				request.Role = worker.RoleLead
				request.LaunchEnvironment.Role = worker.RoleLead
				request.OutputContract = worker.OutputContractPlanningLead
			}
			session, err := adapter.Start(t.Context(), request)
			if err != nil {
				t.Fatalf("start invalid Claude adapter: %v", err)
			}
			go func() {
				for range session.Events() {
				}
			}()
			if _, err := session.Wait(timeoutContext(t, 3*time.Second)); !errors.Is(err, ErrProtocol) {
				t.Fatalf("invalid protocol error = %v, want ErrProtocol", err)
			}
		})
	}
}

func TestAdapterSupportsStopAndRejectsLiveSteering(t *testing.T) {
	adapter := testAdapter(t, "wait", "Keep working")
	providerSession, err := adapter.Start(t.Context(), adapter.request("att_claude_stop", "Keep working"))
	if err != nil {
		t.Fatalf("start stoppable Claude adapter: %v", err)
	}
	go func() {
		for range providerSession.Events() {
		}
	}()
	if err := providerSession.Send(t.Context(), worker.Command{
		ID: "cmd_message", Type: worker.CommandMessage, Message: "New guidance",
	}); !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("message error = %v, want ErrUnsupportedCommand", err)
	}
	if err := providerSession.Send(t.Context(), worker.Command{
		ID: "cmd_stop", Type: worker.CommandStop,
	}); err != nil {
		t.Fatalf("stop Claude adapter: %v", err)
	}
	result, err := providerSession.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Outcome != worker.OutcomeStopped {
		t.Fatalf("stopped result=%+v error=%v", result, err)
	}
}

func TestAdapterForceStopsExactClaudeProcess(t *testing.T) {
	adapter := testAdapter(t, "force-stop", "Keep working")
	providerSession, err := adapter.Start(t.Context(), adapter.request("att_claude_force", "Keep working"))
	if err != nil {
		t.Fatalf("start force-stoppable Claude adapter: %v", err)
	}
	session := providerSession.(worker.ForceStoppableSession)
	go func() {
		for range session.Events() {
		}
	}()
	if err := session.ForceStop(timeoutContext(t, 3*time.Second), "test force stop"); err != nil {
		t.Fatalf("force-stop Claude adapter: %v", err)
	}
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Outcome != worker.OutcomeStopped {
		t.Fatalf("force-stopped result=%+v error=%v", result, err)
	}
}

func TestNewAdapterValidatesConfigurationAndSessionIdentity(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing supervisor error = %v", err)
	}
	for _, config := range []Config{
		{Supervisor: processsupervisor.New(), StartTimeout: -time.Second},
		{Supervisor: processsupervisor.New(), ShutdownTimeout: -time.Second},
		{Supervisor: processsupervisor.New(), EventBuffer: -1},
		{Supervisor: processsupervisor.New(), PermissionMode: "unrestricted"},
	} {
		if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("config %+v error = %v, want ErrInvalidConfig", config, err)
		}
	}
	adapter, err := New(Config{Supervisor: processsupervisor.New()})
	if err != nil {
		t.Fatalf("default adapter config: %v", err)
	}
	if adapter.executable != "claude" || adapter.permissionMode != "plan" {
		t.Fatalf("unexpected defaults: %+v", adapter)
	}
	request := worker.SessionRequest{
		SessionID: "ses_claude_test", AttemptID: "att_claude_test", FeatureID: "fea_claude_test",
		Role: worker.RoleReviewer, Instructions: "test",
	}
	if _, err := adapter.Start(nil, request); err == nil {
		t.Fatal("expected nil start context to fail")
	}
	if _, err := adapter.Start(t.Context(), request); !errors.Is(err, worker.ErrInvalidLaunchEnvironment) {
		t.Fatalf("missing launch environment error = %v", err)
	}
	request.LaunchEnvironment = worker.LaunchEnvironment{
		AgentProfileID: "profile_test", ProjectID: "prj_test", FeatureID: request.FeatureID,
		Role: request.Role, WorkspaceID: "workspace_test", WorkingDirectory: t.TempDir(), Variables: []string{},
	}
	if _, err := adapter.Resume(t.Context(), worker.ResumeRequest{
		SessionRequest: request, ProviderSessionID: "not-a-uuid",
	}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("invalid resume identity error = %v, want ErrProtocol", err)
	}
}

func TestClaudeCLIHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnvironment)
	if mode == "" {
		return
	}
	if mode == "force-stop" {
		signal.Ignore(syscall.SIGTERM)
	}
	directory, err := os.Getwd()
	if err != nil || directory != os.Getenv(helperDirectoryEnvironment) ||
		os.Getenv(helperVisibleEnvironment) != "assigned" {
		os.Exit(80)
	}
	arguments := helperArguments()
	wantPrompt := os.Getenv(helperPromptEnvironment)
	if !helperHasPair(arguments, "--output-format", "stream-json") ||
		!helperHasPair(arguments, "--prompt-suggestions", "false") ||
		!helperHasPair(arguments, "--permission-mode", "plan") ||
		!helperHasPair(arguments, "--model", "test-model") ||
		arguments[len(arguments)-1] != wantPrompt {
		os.Exit(81)
	}
	if strings.HasPrefix(mode, "resume") {
		if !helperHasPair(arguments, "--resume", testSessionID) || slices.Contains(arguments, "--session-id") {
			os.Exit(82)
		}
	} else if !helperHasPair(arguments, "--session-id", testSessionID) || slices.Contains(arguments, "--resume") {
		os.Exit(83)
	}
	schemaPresent := slices.Contains(arguments, "--json-schema")
	if (mode == "structured-plan" || mode == "structured-review" || mode == "missing-structured-output") != schemaPresent {
		os.Exit(84)
	}
	if mode == "identity-before-init" {
		helperWaitForever()
	}

	writer := json.NewEncoder(os.Stdout)
	sessionID := testSessionID
	if mode == "mismatch" {
		sessionID = "223e4567-e89b-42d3-a456-426614174000"
	}
	helperWrite(writer, map[string]any{
		"type": "system", "subtype": "init", "session_id": sessionID, "cwd": directory,
	})
	if mode == "malformed" || mode == "mismatch" {
		if mode == "malformed" {
			fmt.Fprintln(os.Stdout, "not-json")
		}
		helperWaitForever()
	}

	switch mode {
	case "success", "resume":
		helperWrite(writer, map[string]any{
			"type": "assistant", "session_id": sessionID,
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "must stay private"},
				map[string]any{"type": "tool_use", "id": "tool_test", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
			}},
		})
		helperWrite(writer, map[string]any{
			"type": "user", "session_id": sessionID,
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "tool_test", "content": "ok", "is_error": false},
			}},
		})
		helperWrite(writer, map[string]any{
			"type": "assistant", "session_id": sessionID,
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "Implemented and verified the change."},
			}},
		})
		helperWrite(writer, map[string]any{
			"type": "result", "subtype": "success", "session_id": sessionID,
			"is_error": false, "result": "Implemented and verified the change.",
		})
	case "result-only":
		helperWrite(writer, map[string]any{
			"type": "result", "subtype": "success", "session_id": sessionID,
			"is_error": false, "result": "Result-only answer.",
		})
	case "structured-plan":
		helperWrite(writer, map[string]any{
			"type": "assistant", "session_id": sessionID,
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": `{"action":"submit_plan","content":"Final agreed plan"}`},
			}},
		})
		helperWrite(writer, map[string]any{
			"type": "result", "subtype": "success", "session_id": sessionID,
			"is_error":          false,
			"structured_output": map[string]any{"action": "submit_plan", "content": "Final agreed plan"},
		})
	case "structured-review":
		helperWrite(writer, map[string]any{
			"type": "result", "subtype": "success", "session_id": sessionID,
			"is_error": false,
			"structured_output": map[string]any{
				"action": "changes_requested", "summary": "The implementation misses the documented failure case.",
				"commit_id": "0123456789abcdef0123456789abcdef01234567", "pull_request_number": 7, "review_id": 11,
			},
		})
	case "failed":
		helperWrite(writer, map[string]any{
			"type": "result", "subtype": "error_during_execution", "session_id": sessionID,
			"is_error": true, "result": "controlled failure",
		})
	case "missing-structured-output":
		helperWrite(writer, map[string]any{
			"type": "result", "subtype": "success", "session_id": sessionID,
			"is_error": false, "result": "plain result",
		})
	case "wait", "force-stop":
		helperWaitForever()
	default:
		os.Exit(85)
	}
	os.Exit(0)
}

type adapterHarness struct {
	*Adapter
	directory   string
	environment []string
}

func testAdapter(t *testing.T, mode, prompt string) *adapterHarness {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve test workspace: %v", err)
	}
	environment := append(os.Environ(),
		helperModeEnvironment+"="+mode,
		helperPromptEnvironment+"="+prompt,
		helperDirectoryEnvironment+"="+directory,
		helperVisibleEnvironment+"=assigned",
	)
	adapter, err := New(Config{
		Supervisor: processsupervisor.New(), Executable: os.Args[0],
		Arguments: []string{"-test.run=^TestClaudeCLIHelper$", "--"},
		Model:     "test-model", PermissionMode: "plan",
		StartTimeout: 2 * time.Second, ShutdownTimeout: time.Second,
		GenerateSessionID: func() (string, error) { return testSessionID, nil },
	})
	if err != nil {
		t.Fatalf("create test Claude adapter: %v", err)
	}
	return &adapterHarness{Adapter: adapter, directory: directory, environment: environment}
}

func (harness *adapterHarness) request(attemptID, instructions string) worker.SessionRequest {
	return worker.SessionRequest{
		SessionID: "ses_claude_test", AttemptID: attemptID, FeatureID: "fea_claude_test",
		Role: worker.RoleReviewer, Instructions: instructions,
		LaunchEnvironment: worker.LaunchEnvironment{
			AgentProfileID: "profile_claude_test", ProjectID: "prj_claude_test",
			FeatureID: "fea_claude_test", Role: worker.RoleReviewer,
			WorkspaceID: "workspace_claude_test", WorkingDirectory: harness.directory,
			Variables: append([]string(nil), harness.environment...),
		},
	}
}

func collectEvents(session worker.Session) <-chan []worker.Event {
	collected := make(chan []worker.Event, 1)
	go func() {
		var events []worker.Event
		for event := range session.Events() {
			events = append(events, event)
		}
		collected <- events
	}()
	return collected
}

func timeoutContext(t *testing.T, duration time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), duration)
	t.Cleanup(cancel)
	return ctx
}

func helperArguments() []string {
	for index, argument := range os.Args {
		if argument == "--" {
			return os.Args[index+1:]
		}
	}
	os.Exit(86)
	return nil
}

func helperHasPair(arguments []string, name, value string) bool {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name && arguments[index+1] == value {
			return true
		}
	}
	return false
}

func helperWrite(writer *json.Encoder, value any) {
	if writer.Encode(value) != nil {
		os.Exit(87)
	}
}

func helperWaitForever() {
	for {
		time.Sleep(time.Hour)
	}
}
