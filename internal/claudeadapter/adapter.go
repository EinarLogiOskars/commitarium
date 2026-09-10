// Package claudeadapter translates Commitarium's provider-neutral worker
// contract to Claude Code's non-interactive stream-JSON CLI protocol.
package claudeadapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/processsupervisor"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

const (
	defaultExecutable      = "claude"
	defaultPermissionMode  = "plan"
	defaultStartTimeout    = 10 * time.Second
	defaultShutdownTimeout = 5 * time.Second
	defaultEventBuffer     = 64
)

var (
	ErrInvalidConfig = errors.New("invalid Claude adapter configuration")
	ErrProtocol      = errors.New("invalid Claude Code stream protocol")
	uuidPattern      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type Config struct {
	Supervisor        *processsupervisor.Supervisor
	Executable        string
	Arguments         []string
	Model             string
	PermissionMode    string
	StartTimeout      time.Duration
	ShutdownTimeout   time.Duration
	EventBuffer       int
	GenerateSessionID func() (string, error)
}

type Adapter struct {
	supervisor        *processsupervisor.Supervisor
	executable        string
	arguments         []string
	model             string
	permissionMode    string
	startTimeout      time.Duration
	shutdownTimeout   time.Duration
	eventBuffer       int
	generateSessionID func() (string, error)
}

var _ worker.Adapter = (*Adapter)(nil)

func New(config Config) (*Adapter, error) {
	if config.Supervisor == nil {
		return nil, fmt.Errorf("%w: process supervisor is required", ErrInvalidConfig)
	}
	if config.StartTimeout < 0 || config.ShutdownTimeout < 0 {
		return nil, fmt.Errorf("%w: timeouts cannot be negative", ErrInvalidConfig)
	}
	if config.EventBuffer < 0 {
		return nil, fmt.Errorf("%w: event buffer cannot be negative", ErrInvalidConfig)
	}
	permissionMode := strings.TrimSpace(config.PermissionMode)
	if permissionMode == "" {
		permissionMode = defaultPermissionMode
	}
	if !validPermissionMode(permissionMode) {
		return nil, fmt.Errorf("%w: permission mode %q is unsupported", ErrInvalidConfig, permissionMode)
	}
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = defaultExecutable
	}
	startTimeout := config.StartTimeout
	if startTimeout == 0 {
		startTimeout = defaultStartTimeout
	}
	shutdownTimeout := config.ShutdownTimeout
	if shutdownTimeout == 0 {
		shutdownTimeout = defaultShutdownTimeout
	}
	eventBuffer := config.EventBuffer
	if eventBuffer == 0 {
		eventBuffer = defaultEventBuffer
	}
	generateSessionID := config.GenerateSessionID
	if generateSessionID == nil {
		generateSessionID = newSessionID
	}
	return &Adapter{
		supervisor: config.Supervisor, executable: executable,
		arguments: append([]string(nil), config.Arguments...),
		model:     strings.TrimSpace(config.Model), permissionMode: permissionMode,
		startTimeout: startTimeout, shutdownTimeout: shutdownTimeout,
		eventBuffer: eventBuffer, generateSessionID: generateSessionID,
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
	if request.LaunchEnvironment.IsZero() {
		return nil, fmt.Errorf("%w: launch environment is required", worker.ErrInvalidLaunchEnvironment)
	}
	sessionID, err := adapter.generateSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate Claude session ID: %w", err)
	}
	if !uuidPattern.MatchString(sessionID) {
		return nil, fmt.Errorf("%w: generated session ID is not a UUID", ErrInvalidConfig)
	}
	return adapter.launch(ctx, request, sessionID, false, request.Instructions)
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
	if request.LaunchEnvironment.IsZero() {
		return nil, fmt.Errorf("%w: launch environment is required", worker.ErrInvalidLaunchEnvironment)
	}
	if !uuidPattern.MatchString(request.ProviderSessionID) {
		return nil, fmt.Errorf("%w: provider session ID is not a UUID", ErrProtocol)
	}
	prompt := strings.TrimSpace(request.Recovery.Briefing)
	if prompt == "" {
		prompt = request.Instructions
	}
	return adapter.launch(ctx, request.SessionRequest, request.ProviderSessionID, true, prompt)
}

func (adapter *Adapter) launch(
	ctx context.Context,
	request worker.SessionRequest,
	providerSessionID string,
	resume bool,
	prompt string,
) (worker.Session, error) {
	arguments, err := adapter.commandArguments(request.OutputContract, providerSessionID, resume, prompt)
	if err != nil {
		return nil, err
	}
	startContext, cancel := context.WithTimeout(ctx, adapter.startTimeout)
	defer cancel()
	process, err := adapter.supervisor.Start(startContext, processsupervisor.StartRequest{
		AttemptID:   request.AttemptID,
		Executable:  adapter.executable,
		Arguments:   arguments,
		Directory:   request.LaunchEnvironment.WorkingDirectory,
		Environment: request.LaunchEnvironment.Clone().Variables,
	})
	if err != nil {
		return nil, fmt.Errorf("start Claude Code: %w", err)
	}
	providerSession := newSession(
		process,
		providerSessionID,
		request.LaunchEnvironment.WorkingDirectory,
		request.OutputContract,
		adapter.shutdownTimeout,
		adapter.eventBuffer,
	)
	providerSession.start()
	return providerSession, nil
}

func (adapter *Adapter) commandArguments(
	contract worker.OutputContract,
	providerSessionID string,
	resume bool,
	prompt string,
) ([]string, error) {
	arguments := append([]string(nil), adapter.arguments...)
	arguments = append(arguments,
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--no-chrome",
		"--prompt-suggestions", "false",
		"--permission-mode", adapter.permissionMode,
	)
	if resume {
		arguments = append(arguments, "--resume", providerSessionID)
	} else {
		arguments = append(arguments, "--session-id", providerSessionID)
	}
	if adapter.model != "" {
		arguments = append(arguments, "--model", adapter.model)
	}
	if schema := worker.OutputJSONSchema(contract); schema != nil {
		encoded, err := json.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("encode Claude output schema: %w", err)
		}
		arguments = append(arguments, "--json-schema", string(encoded))
	}
	arguments = append(arguments, prompt)
	return arguments, nil
}

func validPermissionMode(value string) bool {
	switch value {
	case "plan", "acceptEdits", "bypassPermissions", "dontAsk", "auto", "manual":
		return true
	default:
		return false
	}
}

func newSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded), nil
}
