//go:build darwin || linux

package codexadapter

import (
	"bufio"
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
	helperModeEnvironment      = "COMMITARIUM_CODEX_ADAPTER_HELPER"
	helperPromptEnvironment    = "COMMITARIUM_CODEX_ADAPTER_PROMPT"
	helperDirectoryEnvironment = "COMMITARIUM_CODEX_ADAPTER_DIRECTORY"
	helperVisibleEnvironment   = "COMMITARIUM_CODEX_ADAPTER_VISIBLE"
)

func TestAdapterStartsCodexAndTranslatesObservableActivity(t *testing.T) {
	adapter := testAdapter(t, "success", "Implement the approved change")
	session, err := adapter.Start(t.Context(), adapter.request("att_codex_start", "Implement the approved change"))
	if err != nil {
		t.Fatalf("start Codex adapter: %v", err)
	}
	if session.ProviderSessionID() != "thr_test" {
		t.Fatalf("provider session ID = %q, want thr_test", session.ProviderSessionID())
	}

	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil {
		t.Fatalf("wait for Codex adapter: %v", err)
	}
	if result.Outcome != worker.OutcomeCompleted ||
		result.Disposition != worker.DispositionSucceeded ||
		result.ProviderSessionID != "thr_test" ||
		result.Summary != "Implemented and verified the change." {
		t.Fatalf("Codex result = %+v", result)
	}
	wantEvents := []worker.Event{
		{Type: worker.EventActivity, Text: "Codex started working."},
		{Type: worker.EventActivity, Text: "Codex started a command."},
		{Type: worker.EventActivity, Text: "Codex finished a command with exit code 0."},
		{Type: worker.EventMessage, Text: "Implemented and verified the change."},
	}
	if observed := <-events; !equalEvents(observed, wantEvents) {
		t.Fatalf("observable events = %+v, want %+v", observed, wantEvents)
	}
}

func TestAdapterResumesExactThreadWithRecoveryBriefing(t *testing.T) {
	adapter := testAdapter(t, "resume", "Inspect durable state before continuing")
	session, err := adapter.Resume(t.Context(), worker.ResumeRequest{
		SessionRequest:    adapter.request("att_codex_resume", "original instructions"),
		ProviderSessionID: "thr_test",
		Recovery: worker.RecoveryContext{
			Briefing: "Inspect durable state before continuing",
		},
	})
	if err != nil {
		t.Fatalf("resume Codex adapter: %v", err)
	}
	go func() {
		for range session.Events() {
		}
	}()
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.ProviderSessionID != "thr_test" {
		t.Fatalf("resumed result=%+v error=%v", result, err)
	}
}

func TestAdapterPublishesStructuredPlanningSubmission(t *testing.T) {
	adapter := testAdapter(t, "structured-plan", "Consider the reviewer's response")
	request := adapter.request("att_codex_plan", "Consider the reviewer's response")
	request.Role = worker.RoleLead
	request.LaunchEnvironment.Role = worker.RoleLead
	request.OutputContract = worker.OutputContractPlanningLead
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start structured planning turn: %v", err)
	}
	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Summary != "Final agreed plan" {
		t.Fatalf("structured planning result=%+v error=%v", result, err)
	}
	if observed := <-events; !equalEvents(observed, []worker.Event{
		{Type: worker.EventActivity, Text: "Codex started working."},
		{Type: worker.EventPlanSubmitted, Text: "Final agreed plan"},
	}) {
		t.Fatalf("structured planning events = %+v", observed)
	}
}

func TestAdapterReturnsStructuredImplementationPublication(t *testing.T) {
	adapter := testAdapter(t, "structured-implementation", "Implement and publish")
	request := adapter.request("att_codex_implementation", "Implement and publish")
	request.Role = worker.RoleLead
	request.LaunchEnvironment.Role = worker.RoleLead
	request.OutputContract = worker.OutputContractImplementationLead
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start structured implementation turn: %v", err)
	}
	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Summary != "Implemented the agreed change and passed tests." ||
		result.Disposition != worker.DispositionSucceeded || result.Publication == nil ||
		result.Publication.CommitID != "0123456789abcdef0123456789abcdef01234567" ||
		result.Publication.PullRequestNumber != 7 {
		t.Fatalf("structured implementation result=%+v error=%v", result, err)
	}
	if observed := <-events; !equalEvents(observed, []worker.Event{
		{Type: worker.EventActivity, Text: "Codex started working."},
		{Type: worker.EventMessage, Text: "Implemented the agreed change and passed tests."},
	}) {
		t.Fatalf("structured implementation events = %+v", observed)
	}
}

func TestAdapterReturnsStructuredImplementationBlocker(t *testing.T) {
	adapter := testAdapter(t, "structured-implementation-blocked", "Implement and publish")
	request := adapter.request("att_codex_implementation_blocked", "Implement and publish")
	request.Role = worker.RoleLead
	request.LaunchEnvironment.Role = worker.RoleLead
	request.OutputContract = worker.OutputContractImplementationLead
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start blocked implementation turn: %v", err)
	}
	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Disposition != worker.DispositionInputRequired ||
		result.Publication != nil {
		t.Fatalf("blocked implementation result=%+v error=%v", result, err)
	}
	if observed := <-events; !equalEvents(observed, []worker.Event{
		{Type: worker.EventActivity, Text: "Codex started working."},
		{Type: worker.EventInputRequired, Text: "Forgejo rejected the push."},
	}) {
		t.Fatalf("blocked implementation events = %+v", observed)
	}
}

func TestAdapterReturnsStructuredImplementationReview(t *testing.T) {
	adapter := testAdapter(t, "structured-review", "Review the exact implementation commit")
	request := adapter.request("att_codex_review", "Review the exact implementation commit")
	request.Role = worker.RoleReviewer
	request.LaunchEnvironment.Role = worker.RoleReviewer
	request.OutputContract = worker.OutputContractImplementationReview
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start structured review turn: %v", err)
	}
	events := collectEvents(session)
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Summary != "The implementation misses the documented failure case." ||
		result.Disposition != worker.DispositionChangesRequested || result.Review == nil ||
		result.Review.CommitID != "0123456789abcdef0123456789abcdef01234567" ||
		result.Review.PullRequestNumber != 7 || result.Review.ReviewID != 11 {
		t.Fatalf("structured review result=%+v error=%v", result, err)
	}
	if observed := <-events; !equalEvents(observed, []worker.Event{
		{Type: worker.EventActivity, Text: "Codex started working."},
		{Type: worker.EventMessage, Text: "The implementation misses the documented failure case."},
	}) {
		t.Fatalf("structured review events = %+v", observed)
	}
}

func TestImplementationReviewResponseFailsClosed(t *testing.T) {
	for _, response := range []string{
		`{"action":"approved","summary":"good","commit_id":"bad","pull_request_number":7,"review_id":11}`,
		`{"action":"blocked","summary":"blocked","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"review_id":0}`,
		`{"action":"changes_requested","summary":" ","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"review_id":11}`,
		`{"action":"approved","summary":"good","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"review_id":11,"extra":true}`,
	} {
		if _, err := decodeImplementationReviewResponse(response); !errors.Is(err, ErrProtocol) {
			t.Errorf("response %q error=%v, want ErrProtocol", response, err)
		}
	}
}

func TestPlanningLeadResponseFailsClosed(t *testing.T) {
	for _, response := range []string{
		`{"action":"unknown","content":"plan"}`,
		`{"action":"submit_plan","content":" "}`,
		`{"action":"respond","content":"reply","extra":true}`,
		`{"action":"respond","content":"reply"} {}`,
	} {
		if _, err := decodePlanningLeadResponse(response); !errors.Is(err, ErrProtocol) {
			t.Errorf("response %q error=%v, want ErrProtocol", response, err)
		}
	}
}

func TestImplementationLeadResponseFailsClosed(t *testing.T) {
	for _, response := range []string{
		`{"action":"published","summary":"done","commit_id":"bad","pull_request_number":7}`,
		`{"action":"blocked","summary":"blocked","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7}`,
		`{"action":"blocked","summary":" ","commit_id":"","pull_request_number":7}`,
		`{"action":"published","summary":"done","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"extra":true}`,
	} {
		if _, err := decodeImplementationLeadResponse(response); !errors.Is(err, ErrProtocol) {
			t.Errorf("response %q error=%v, want ErrProtocol", response, err)
		}
	}
}

func TestAdapterRejectsMismatchedResumedThread(t *testing.T) {
	adapter := testAdapter(t, "resume-mismatch", "recovery briefing")
	session, err := adapter.Resume(t.Context(), worker.ResumeRequest{
		SessionRequest:    adapter.request("att_resume_mismatch", "original instructions"),
		ProviderSessionID: "thr_test",
		Recovery:          worker.RecoveryContext{Briefing: "recovery briefing"},
	})
	if session != nil {
		t.Fatalf("mismatched resume returned session %+v", session)
	}
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("mismatched resume error = %v, want ErrProtocol", err)
	}
}

func TestAdapterSteersAndInterruptsActiveTurn(t *testing.T) {
	adapter := testAdapter(t, "controls", "start work")
	session, err := adapter.Start(t.Context(), adapter.request("att_codex_controls", "start work"))
	if err != nil {
		t.Fatalf("start controlled Codex adapter: %v", err)
	}
	events := collectEvents(session)
	if err := session.Send(t.Context(), worker.Command{
		ID: "cmd_guidance", Type: worker.CommandMessage, Message: "Check the edge case too",
	}); err != nil {
		t.Fatalf("steer Codex turn: %v", err)
	}
	if err := session.Send(t.Context(), worker.Command{
		ID: "cmd_pause", Type: worker.CommandPause,
	}); !errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("pause error = %v, want ErrUnsupportedCommand", err)
	}
	if err := session.Send(t.Context(), worker.Command{
		ID: "cmd_stop", Type: worker.CommandStop,
	}); err != nil {
		t.Fatalf("interrupt Codex turn: %v", err)
	}
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Outcome != worker.OutcomeStopped {
		t.Fatalf("interrupted result=%+v error=%v", result, err)
	}
	if observed := <-events; !equalEvents(observed, []worker.Event{
		{Type: worker.EventActivity, Text: "Codex started working."},
	}) {
		t.Fatalf("control events = %+v", observed)
	}
}

func TestAdapterMapsExplicitTurnFailure(t *testing.T) {
	adapter := testAdapter(t, "failed-turn", "fail deterministically")
	session, err := adapter.Start(t.Context(), adapter.request("att_codex_failed", "fail deterministically"))
	if err != nil {
		t.Fatalf("start failing Codex adapter: %v", err)
	}
	go func() {
		for range session.Events() {
		}
	}()
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil {
		t.Fatalf("wait for explicit failure: %v", err)
	}
	if result.Outcome != worker.OutcomeFailed || result.Disposition != "" {
		t.Fatalf("explicit failure result = %+v", result)
	}
}

func TestAdapterReturnsEarlyIdentityWhenTurnStartFails(t *testing.T) {
	adapter := testAdapter(t, "turn-start-error", "cannot start")
	session, err := adapter.Start(t.Context(), adapter.request("att_turn_start_error", "cannot start"))
	if err == nil || session == nil {
		t.Fatalf("turn-start failure session=%+v error=%v", session, err)
	}
	if session.ProviderSessionID() != "thr_test" {
		t.Fatalf("provider identity after partial launch = %q", session.ProviderSessionID())
	}
}

func TestAdapterReturnsPartialSessionWhenThreadIdentityIsMissing(t *testing.T) {
	adapter := testAdapter(t, "missing-thread-id", "start cautiously")
	session, err := adapter.Start(t.Context(), adapter.request("att_missing_thread", "start cautiously"))
	if err == nil || session == nil {
		t.Fatalf("missing thread identity session=%+v error=%v", session, err)
	}
	if session.ProviderSessionID() != "" {
		t.Fatalf("unexpected provider identity = %q", session.ProviderSessionID())
	}
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("missing thread identity error = %v, want ErrProtocol", err)
	}
}

func TestAdapterFailsClosedOnMalformedProtocol(t *testing.T) {
	adapter := testAdapter(t, "malformed", "observe malformed output")
	session, err := adapter.Start(t.Context(), adapter.request("att_codex_malformed", "observe malformed output"))
	if err != nil {
		t.Fatalf("start malformed Codex adapter: %v", err)
	}
	go func() {
		for range session.Events() {
		}
	}()
	if _, err := session.Wait(timeoutContext(t, 3*time.Second)); !errors.Is(err, ErrProtocol) {
		t.Fatalf("malformed protocol error = %v, want ErrProtocol", err)
	}
}

func TestAdapterForceStopsExactProcessSession(t *testing.T) {
	adapter := testAdapter(t, "force-stop", "keep working")
	providerSession, err := adapter.Start(t.Context(), adapter.request("att_codex_force", "keep working"))
	if err != nil {
		t.Fatalf("start force-stop Codex adapter: %v", err)
	}
	session := providerSession.(worker.ForceStoppableSession)
	go func() {
		for range session.Events() {
		}
	}()
	if err := session.ForceStop(timeoutContext(t, 3*time.Second), "test force stop"); err != nil {
		t.Fatalf("force-stop Codex adapter: %v", err)
	}
	result, err := session.Wait(timeoutContext(t, 3*time.Second))
	if err != nil || result.Outcome != worker.OutcomeStopped {
		t.Fatalf("force-stopped result=%+v error=%v", result, err)
	}
}

func TestNewAdapterValidatesAndUsesSafeDefaults(t *testing.T) {
	if _, err := New(Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing supervisor error = %v", err)
	}
	for _, config := range []Config{
		{Supervisor: processsupervisor.New(), RequestTimeout: -time.Second},
		{Supervisor: processsupervisor.New(), ShutdownTimeout: -time.Second},
		{Supervisor: processsupervisor.New(), EventBuffer: -1},
		{Supervisor: processsupervisor.New(), ApprovalPolicy: "sometimes"},
		{Supervisor: processsupervisor.New(), ApprovalPolicy: "on-request"},
		{Supervisor: processsupervisor.New(), Sandbox: "host-write"},
	} {
		if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("config %+v error = %v, want ErrInvalidConfig", config, err)
		}
	}
	adapter, err := New(Config{Supervisor: processsupervisor.New()})
	if err != nil {
		t.Fatalf("default adapter config: %v", err)
	}
	if adapter.executable != "codex" ||
		strings.Join(adapter.arguments, " ") != "app-server --listen stdio://" ||
		adapter.approvalPolicy != "never" || adapter.sandbox != "read-only" {
		t.Fatalf("unsafe or unexpected defaults: %+v", adapter)
	}
	if _, err := New(Config{
		Supervisor: processsupervisor.New(), Sandbox: "danger-full-access",
	}); err != nil {
		t.Fatalf("container-isolated sandbox config: %v", err)
	}
	request := worker.SessionRequest{
		SessionID: "ses_codex_test", AttemptID: "att_nil_context", FeatureID: "fea_codex_test",
		Role: worker.RoleCoder, Instructions: "test",
	}
	if _, err := adapter.Start(nil, request); err == nil {
		t.Fatal("expected nil start context to fail")
	}
	if _, err := adapter.Start(t.Context(), request); !errors.Is(err, worker.ErrInvalidLaunchEnvironment) {
		t.Fatalf("missing launch environment error = %v", err)
	}
}

func TestCodexAppServerHelper(t *testing.T) {
	mode := os.Getenv(helperModeEnvironment)
	if mode == "" {
		return
	}
	if mode == "force-stop" {
		signal.Ignore(syscall.SIGTERM)
	}
	workingDirectory, err := os.Getwd()
	if err != nil || workingDirectory != os.Getenv(helperDirectoryEnvironment) ||
		os.Getenv(helperVisibleEnvironment) != "assigned" {
		os.Exit(80)
	}
	scanner := bufio.NewScanner(os.Stdin)
	writer := json.NewEncoder(os.Stdout)

	initialize := helperRead(scanner)
	if initialize.Method != "initialize" || initialize.ID != 1 {
		os.Exit(81)
	}
	helperWrite(writer, map[string]any{"id": initialize.ID, "result": map[string]any{"userAgent": "fake"}})
	initialized := helperRead(scanner)
	if initialized.Method != "initialized" || initialized.ID != 0 {
		os.Exit(82)
	}

	threadRequest := helperRead(scanner)
	wantMethod := "thread/start"
	if strings.HasPrefix(mode, "resume") {
		wantMethod = "thread/resume"
	}
	if threadRequest.Method != wantMethod || threadRequest.ID != 2 {
		os.Exit(83)
	}
	var threadParams map[string]any
	if json.Unmarshal(threadRequest.Params, &threadParams) != nil ||
		threadParams["approvalPolicy"] != "never" || threadParams["sandbox"] != "read-only" ||
		threadParams["model"] != "test-model" {
		os.Exit(84)
	}
	if wantMethod == "thread/resume" && threadParams["threadId"] != "thr_test" {
		os.Exit(85)
	}
	threadID := "thr_test"
	if mode == "resume-mismatch" {
		threadID = "thr_other"
	}
	responseThreadID := threadID
	if mode == "missing-thread-id" {
		responseThreadID = ""
	}
	helperWrite(writer, map[string]any{
		"id":     threadRequest.ID,
		"result": map[string]any{"thread": map[string]any{"id": responseThreadID}},
	})
	if mode == "resume-mismatch" || mode == "missing-thread-id" {
		helperWaitForever()
	}
	helpNotify(writer, "thread/started", map[string]any{"thread": map[string]any{"id": threadID}})

	turnRequest := helperRead(scanner)
	if turnRequest.Method != "turn/start" || turnRequest.ID != 3 ||
		!helperTurnPromptMatches(turnRequest.Params, os.Getenv(helperPromptEnvironment)) {
		os.Exit(86)
	}
	if (mode == "structured-plan") != helperTurnHasPlanningSchema(turnRequest.Params) {
		os.Exit(93)
	}
	if strings.HasPrefix(mode, "structured-implementation") !=
		helperTurnHasImplementationSchema(turnRequest.Params) {
		os.Exit(94)
	}
	if (mode == "structured-review") != helperTurnHasReviewSchema(turnRequest.Params) {
		os.Exit(95)
	}
	if mode == "turn-start-error" {
		helperWrite(writer, map[string]any{
			"id":    turnRequest.ID,
			"error": map[string]any{"code": -32000, "message": "controlled turn failure"},
		})
		helperWaitForever()
	}
	helpWriteTurnStarted(writer, turnRequest.ID)

	switch mode {
	case "success", "resume":
		helpNotify(writer, "item/started", map[string]any{
			"threadId": "thr_test", "turnId": "turn_test",
			"item": map[string]any{"id": "item_command", "type": "commandExecution", "status": "inProgress"},
		})
		zero := 0
		helpNotify(writer, "item/completed", map[string]any{
			"threadId": "thr_test", "turnId": "turn_test",
			"item": map[string]any{"id": "item_command", "type": "commandExecution", "status": "completed", "exitCode": zero},
		})
		helpNotify(writer, "item/completed", map[string]any{
			"threadId": "thr_test", "turnId": "turn_test",
			"item": map[string]any{"id": "item_reasoning", "type": "reasoning", "text": "must stay private"},
		})
		helpNotify(writer, "item/completed", map[string]any{
			"threadId": "thr_test", "turnId": "turn_test",
			"item": map[string]any{"id": "item_message", "type": "agentMessage", "text": "Implemented and verified the change."},
		})
		helpNotify(writer, "future/notification", map[string]any{"value": true})
		helpWriteTurnCompleted(writer, "completed")
		helperWaitForever()
	case "structured-plan":
		helpNotify(writer, "item/completed", map[string]any{
			"threadId": "thr_test", "turnId": "turn_test",
			"item": map[string]any{
				"id": "item_message", "type": "agentMessage",
				"text": `{"action":"submit_plan","content":"Final agreed plan"}`,
			},
		})
		helpWriteTurnCompleted(writer, "completed")
		helperWaitForever()
	case "structured-implementation", "structured-implementation-blocked":
		text := `{"action":"published","summary":"Implemented the agreed change and passed tests.","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7}`
		if mode == "structured-implementation-blocked" {
			text = `{"action":"blocked","summary":"Forgejo rejected the push.","commit_id":"","pull_request_number":7}`
		}
		helpNotify(writer, "item/completed", map[string]any{
			"threadId": "thr_test", "turnId": "turn_test",
			"item": map[string]any{
				"id": "item_message", "type": "agentMessage", "text": text,
			},
		})
		helpWriteTurnCompleted(writer, "completed")
		helperWaitForever()
	case "structured-review":
		helpNotify(writer, "item/completed", map[string]any{
			"threadId": "thr_test", "turnId": "turn_test",
			"item": map[string]any{
				"id": "item_message", "type": "agentMessage",
				"text": `{"action":"changes_requested","summary":"The implementation misses the documented failure case.","commit_id":"0123456789abcdef0123456789abcdef01234567","pull_request_number":7,"review_id":11}`,
			},
		})
		helpWriteTurnCompleted(writer, "completed")
		helperWaitForever()
	case "controls":
		steer := helperRead(scanner)
		if steer.Method != "turn/steer" || steer.ID != 4 || !helperSteerMatches(steer.Params) {
			os.Exit(87)
		}
		helperWrite(writer, map[string]any{"id": steer.ID, "result": map[string]any{"turnId": "turn_test"}})
		interrupt := helperRead(scanner)
		if interrupt.Method != "turn/interrupt" || interrupt.ID != 5 {
			os.Exit(88)
		}
		helperWrite(writer, map[string]any{"id": interrupt.ID, "result": map[string]any{}})
		helpWriteTurnCompleted(writer, "interrupted")
		helperWaitForever()
	case "failed-turn":
		helpWriteTurnCompleted(writer, "failed")
		helperWaitForever()
	case "malformed":
		fmt.Fprintln(os.Stdout, "this is not json")
		helperWaitForever()
	case "force-stop":
		helperWaitForever()
	default:
		os.Exit(89)
	}
}

type helperMessage struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func helperRead(scanner *bufio.Scanner) helperMessage {
	if !scanner.Scan() {
		os.Exit(90)
	}
	var message helperMessage
	if json.Unmarshal(scanner.Bytes(), &message) != nil {
		os.Exit(91)
	}
	return message
}

func helperWrite(writer *json.Encoder, value any) {
	if writer.Encode(value) != nil {
		os.Exit(92)
	}
}

func helpNotify(writer *json.Encoder, method string, params any) {
	helperWrite(writer, map[string]any{"method": method, "params": params})
}

func helpWriteTurnStarted(writer *json.Encoder, requestID int64) {
	helperWrite(writer, map[string]any{
		"id":     requestID,
		"result": map[string]any{"turn": map[string]any{"id": "turn_test", "status": "inProgress"}},
	})
	helpNotify(writer, "turn/started", map[string]any{
		"threadId": "thr_test", "turn": map[string]any{"id": "turn_test", "status": "inProgress"},
	})
}

func helpWriteTurnCompleted(writer *json.Encoder, status string) {
	helpNotify(writer, "turn/completed", map[string]any{
		"threadId": "thr_test", "turn": map[string]any{"id": "turn_test", "status": status},
	})
}

func helperTurnPromptMatches(raw json.RawMessage, want string) bool {
	var params struct {
		ThreadID string      `json:"threadId"`
		Input    []textInput `json:"input"`
	}
	return json.Unmarshal(raw, &params) == nil && params.ThreadID == "thr_test" &&
		len(params.Input) == 1 && params.Input[0].Type == "text" && params.Input[0].Text == want
}

func helperTurnHasPlanningSchema(raw json.RawMessage) bool {
	var params struct {
		OutputSchema struct {
			Required []string `json:"required"`
		} `json:"outputSchema"`
	}
	return json.Unmarshal(raw, &params) == nil &&
		slices.Equal(params.OutputSchema.Required, []string{"action", "content"})
}

func helperTurnHasImplementationSchema(raw json.RawMessage) bool {
	var params struct {
		OutputSchema struct {
			Required []string `json:"required"`
		} `json:"outputSchema"`
	}
	return json.Unmarshal(raw, &params) == nil && slices.Equal(
		params.OutputSchema.Required,
		[]string{"action", "summary", "commit_id", "pull_request_number"},
	)
}

func helperTurnHasReviewSchema(raw json.RawMessage) bool {
	var params struct {
		OutputSchema struct {
			Required []string `json:"required"`
		} `json:"outputSchema"`
	}
	return json.Unmarshal(raw, &params) == nil && slices.Equal(
		params.OutputSchema.Required,
		[]string{"action", "summary", "commit_id", "pull_request_number", "review_id"},
	)
}

func helperSteerMatches(raw json.RawMessage) bool {
	var params struct {
		ThreadID            string      `json:"threadId"`
		ExpectedTurnID      string      `json:"expectedTurnId"`
		ClientUserMessageID string      `json:"clientUserMessageId"`
		Input               []textInput `json:"input"`
	}
	return json.Unmarshal(raw, &params) == nil && params.ThreadID == "thr_test" &&
		params.ExpectedTurnID == "turn_test" && params.ClientUserMessageID == "cmd_guidance" &&
		len(params.Input) == 1 && params.Input[0].Text == "Check the edge case too"
}

func helperWaitForever() {
	for {
		time.Sleep(time.Hour)
	}
}

type adapterHarness struct {
	*Adapter
	directory   string
	environment []string
}

func testAdapter(t *testing.T, mode string, prompt string) *adapterHarness {
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
		Supervisor:      processsupervisor.New(),
		Executable:      os.Args[0],
		Arguments:       []string{"-test.run=^TestCodexAppServerHelper$"},
		Model:           "test-model",
		RequestTimeout:  2 * time.Second,
		ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("create test Codex adapter: %v", err)
	}
	return &adapterHarness{Adapter: adapter, directory: directory, environment: environment}
}

func (harness *adapterHarness) request(attemptID string, instructions string) worker.SessionRequest {
	return worker.SessionRequest{
		SessionID: "ses_codex_test", AttemptID: attemptID, FeatureID: "fea_codex_test",
		Role: worker.RoleCoder, Instructions: instructions,
		LaunchEnvironment: worker.LaunchEnvironment{
			AgentProfileID: "profile_codex_test", ProjectID: "prj_codex_test",
			FeatureID: "fea_codex_test", Role: worker.RoleCoder, WorkspaceID: "workspace_codex_test",
			WorkingDirectory: harness.directory,
			Variables:        append([]string(nil), harness.environment...),
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

func equalEvents(left []worker.Event, right []worker.Event) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Type != right[index].Type || left[index].Text != right[index].Text {
			return false
		}
	}
	return true
}

func timeoutContext(t *testing.T, duration time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), duration)
	t.Cleanup(cancel)
	return ctx
}
