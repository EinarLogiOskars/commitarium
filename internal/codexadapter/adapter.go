// Package codexadapter translates Commitarium's provider-neutral worker
// contract to the local Codex app-server JSON protocol.
package codexadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/processsupervisor"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

const (
	defaultExecutable      = "codex"
	defaultRequestTimeout  = 10 * time.Second
	defaultShutdownTimeout = 5 * time.Second
	defaultEventBuffer     = 64
)

var ErrInvalidConfig = errors.New("invalid Codex adapter configuration")

// Config describes an already materialized provider process environment. The
// future runtime wiring will resolve its directory and environment from a
// profile plus workspace revision; this adapter deliberately does not read
// coordinator secrets or choose project mounts itself.
type Config struct {
	Supervisor      *processsupervisor.Supervisor
	Executable      string
	Arguments       []string
	Directory       string
	Environment     []string
	Model           string
	ApprovalPolicy  string
	Sandbox         string
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
	EventBuffer     int
}

type Adapter struct {
	supervisor      *processsupervisor.Supervisor
	executable      string
	arguments       []string
	directory       string
	environment     []string
	model           string
	approvalPolicy  string
	sandbox         string
	requestTimeout  time.Duration
	shutdownTimeout time.Duration
	eventBuffer     int
}

var _ worker.Adapter = (*Adapter)(nil)

func New(config Config) (*Adapter, error) {
	if config.Supervisor == nil {
		return nil, fmt.Errorf("%w: process supervisor is required", ErrInvalidConfig)
	}
	if config.RequestTimeout < 0 || config.ShutdownTimeout < 0 {
		return nil, fmt.Errorf("%w: timeouts cannot be negative", ErrInvalidConfig)
	}
	if config.EventBuffer < 0 {
		return nil, fmt.Errorf("%w: event buffer cannot be negative", ErrInvalidConfig)
	}
	if config.ApprovalPolicy != "" && config.ApprovalPolicy != "never" {
		return nil, fmt.Errorf(
			"%w: approval policy %q requires approval forwarding, which is not implemented yet",
			ErrInvalidConfig,
			config.ApprovalPolicy,
		)
	}
	if config.Sandbox != "" && !validSandbox(config.Sandbox) {
		return nil, fmt.Errorf("%w: sandbox %q is not supported", ErrInvalidConfig, config.Sandbox)
	}

	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = defaultExecutable
	}
	arguments := append([]string(nil), config.Arguments...)
	if config.Arguments == nil {
		arguments = []string{"app-server", "--listen", "stdio://"}
	}
	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	shutdownTimeout := config.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = defaultShutdownTimeout
	}
	eventBuffer := config.EventBuffer
	if eventBuffer == 0 {
		eventBuffer = defaultEventBuffer
	}
	approvalPolicy := config.ApprovalPolicy
	if approvalPolicy == "" {
		approvalPolicy = "never"
	}
	sandbox := config.Sandbox
	if sandbox == "" {
		sandbox = "read-only"
	}
	return &Adapter{
		supervisor:      config.Supervisor,
		executable:      executable,
		arguments:       arguments,
		directory:       config.Directory,
		environment:     append([]string(nil), config.Environment...),
		model:           config.Model,
		approvalPolicy:  approvalPolicy,
		sandbox:         sandbox,
		requestTimeout:  requestTimeout,
		shutdownTimeout: shutdownTimeout,
		eventBuffer:     eventBuffer,
	}, nil
}

func (adapter *Adapter) Start(
	ctx context.Context,
	request worker.SessionRequest,
) (worker.Session, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return adapter.launch(ctx, request, "", request.Instructions)
}

func (adapter *Adapter) Resume(
	ctx context.Context,
	request worker.ResumeRequest,
) (worker.Session, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	prompt := request.Recovery.Briefing
	if strings.TrimSpace(prompt) == "" {
		prompt = request.Instructions
	}
	return adapter.launch(ctx, request.SessionRequest, request.ProviderSessionID, prompt)
}

type initializeParams struct {
	ClientInfo clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

type threadParams struct {
	ThreadID       string `json:"threadId,omitempty"`
	Model          string `json:"model,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	ApprovalPolicy string `json:"approvalPolicy"`
	Sandbox        string `json:"sandbox"`
	ServiceName    string `json:"serviceName,omitempty"`
}

type threadResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type textInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type turnStartParams struct {
	ThreadID string      `json:"threadId"`
	Input    []textInput `json:"input"`
}

type turnResponse struct {
	Turn struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turn"`
}

func (adapter *Adapter) launch(
	ctx context.Context,
	request worker.SessionRequest,
	resumeThreadID string,
	prompt string,
) (worker.Session, error) {
	operationCtx, cancel := context.WithTimeout(ctx, adapter.requestTimeout)
	defer cancel()
	process, err := adapter.supervisor.Start(operationCtx, processsupervisor.StartRequest{
		AttemptID:   request.AttemptID,
		Executable:  adapter.executable,
		Arguments:   append([]string(nil), adapter.arguments...),
		Directory:   adapter.directory,
		Environment: append([]string(nil), adapter.environment...),
	})
	if err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	client := newProtocolClient(process)
	cleanupWithoutSession := func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), adapter.shutdownTimeout)
		defer stopCancel()
		if _, stopErr := process.Terminate(stopCtx); stopErr != nil {
			forceCtx, forceCancel := context.WithTimeout(context.Background(), adapter.shutdownTimeout)
			defer forceCancel()
			_, _ = process.ForceStop(forceCtx)
		}
	}

	var initialized map[string]any
	if err := client.request(operationCtx, "initialize", initializeParams{ClientInfo: clientInfo{
		Name: "commitarium", Title: "Commitarium worker", Version: "0.1.0",
	}}, &initialized); err != nil {
		cleanupWithoutSession()
		return nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err := client.notify(operationCtx, "initialized", struct{}{}); err != nil {
		cleanupWithoutSession()
		return nil, fmt.Errorf("acknowledge Codex app-server initialization: %w", err)
	}

	method := "thread/start"
	params := threadParams{
		Model:          adapter.model,
		CWD:            adapter.directory,
		ApprovalPolicy: adapter.approvalPolicy,
		Sandbox:        adapter.sandbox,
		ServiceName:    "commitarium",
	}
	if resumeThreadID != "" {
		method = "thread/resume"
		params.ThreadID = resumeThreadID
	}
	var threadStarted threadResponse
	if err := client.request(operationCtx, method, params, &threadStarted); err != nil {
		var rejected *protocolError
		if !errors.As(err, &rejected) {
			partial := newSession(client, process, "", adapter.shutdownTimeout, adapter.eventBuffer)
			partial.stopAfterFailedLaunch()
			return partial, fmt.Errorf("%s Codex thread: %w", strings.TrimPrefix(method, "thread/"), err)
		}
		cleanupWithoutSession()
		return nil, fmt.Errorf("%s Codex thread: %w", strings.TrimPrefix(method, "thread/"), err)
	}
	threadID := strings.TrimSpace(threadStarted.Thread.ID)
	if threadID == "" {
		partial := newSession(client, process, "", adapter.shutdownTimeout, adapter.eventBuffer)
		partial.stopAfterFailedLaunch()
		return partial, fmt.Errorf("%w: %s response omitted the thread ID", ErrProtocol, method)
	}
	if resumeThreadID != "" && threadID != resumeThreadID {
		cleanupWithoutSession()
		return nil, fmt.Errorf(
			"%w: resumed thread %q does not match requested thread %q",
			ErrProtocol,
			threadID,
			resumeThreadID,
		)
	}

	providerSession := newSession(
		client,
		process,
		threadID,
		adapter.shutdownTimeout,
		adapter.eventBuffer,
	)
	var turnStarted turnResponse
	if err := client.request(operationCtx, "turn/start", turnStartParams{
		ThreadID: threadID,
		Input:    []textInput{{Type: "text", Text: prompt}},
	}, &turnStarted); err != nil {
		providerSession.stopAfterFailedLaunch()
		return providerSession, fmt.Errorf("start Codex turn: %w", err)
	}
	turnID := strings.TrimSpace(turnStarted.Turn.ID)
	if turnID == "" || turnStarted.Turn.Status != "inProgress" {
		providerSession.stopAfterFailedLaunch()
		return providerSession, fmt.Errorf(
			"%w: turn/start returned ID %q with status %q",
			ErrProtocol,
			turnID,
			turnStarted.Turn.Status,
		)
	}
	providerSession.start(turnID)
	return providerSession, nil
}

func validSandbox(value string) bool {
	return value == "read-only" || value == "workspace-write"
}
