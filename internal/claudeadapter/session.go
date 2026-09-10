package claudeadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/processsupervisor"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

const maxProtocolLineBytes = 8 * 1024 * 1024

var (
	ErrUnsupportedCommand = errors.New("Claude adapter command is unsupported")
	ErrSessionFinished    = errors.New("Claude session is already finished")
	ErrEventBackpressure  = errors.New("Claude activity consumer fell behind")
)

type session struct {
	process           *processsupervisor.Process
	providerSessionID string
	workingDirectory  string
	outputContract    worker.OutputContract
	shutdownTimeout   time.Duration
	events            chan worker.Event
	done              chan struct{}

	mu               sync.Mutex
	finished         bool
	stopRequested    bool
	forced           bool
	initialized      bool
	lastAgentMessage string
	agentMessages    int
	toolNames        map[string]string
	result           worker.Result
	waitErr          error
}

var _ worker.ForceStoppableSession = (*session)(nil)

func newSession(
	process *processsupervisor.Process,
	providerSessionID string,
	workingDirectory string,
	outputContract worker.OutputContract,
	shutdownTimeout time.Duration,
	eventBuffer int,
) *session {
	return &session{
		process: process, providerSessionID: providerSessionID,
		workingDirectory: workingDirectory, outputContract: outputContract,
		shutdownTimeout: shutdownTimeout,
		events:          make(chan worker.Event, eventBuffer), done: make(chan struct{}),
		toolNames: make(map[string]string),
	}
}

func (session *session) start() {
	go session.run()
}

func (session *session) ProviderSessionID() string {
	if session == nil {
		return ""
	}
	return session.providerSessionID
}

func (session *session) Events() <-chan worker.Event {
	if session == nil {
		return nil
	}
	return session.events
}

func (session *session) Send(ctx context.Context, command worker.Command) error {
	if session == nil {
		return errors.New("Claude session is required")
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := command.Validate(); err != nil {
		return err
	}
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return ErrSessionFinished
	}
	if command.Type != worker.CommandStop {
		session.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
	session.stopRequested = true
	session.mu.Unlock()
	if _, err := session.process.Terminate(ctx); err != nil {
		return fmt.Errorf("stop Claude Code: %w", err)
	}
	return nil
}

func (session *session) Wait(ctx context.Context) (worker.Result, error) {
	if session == nil {
		return worker.Result{}, errors.New("Claude session is required")
	}
	if ctx == nil {
		return worker.Result{}, errors.New("context is required")
	}
	select {
	case <-session.done:
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.result, session.waitErr
	case <-ctx.Done():
		return worker.Result{}, ctx.Err()
	}
}

func (session *session) ForceStop(ctx context.Context, _ string) error {
	if session == nil {
		return errors.New("Claude session is required")
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return nil
	}
	session.forced = true
	session.mu.Unlock()
	if _, err := session.process.ForceStop(ctx); err != nil {
		return fmt.Errorf("force-stop Claude Code: %w", err)
	}
	return nil
}

func (session *session) run() {
	var stdout []byte
	var terminal *worker.Result
	var protocolErr error
	for output := range session.process.Output() {
		if output.Stream != processsupervisor.StreamStdout || protocolErr != nil {
			continue
		}
		stdout = append(stdout, output.Data...)
		for {
			newline := bytes.IndexByte(stdout, '\n')
			if newline < 0 {
				break
			}
			line := bytes.TrimSpace(stdout[:newline])
			stdout = stdout[newline+1:]
			if len(line) == 0 {
				continue
			}
			if len(line) > maxProtocolLineBytes {
				protocolErr = fmt.Errorf("%w: message exceeds the size limit", ErrProtocol)
				session.abortProcess()
				break
			}
			if terminal != nil {
				protocolErr = fmt.Errorf("%w: output followed the terminal result", ErrProtocol)
				session.abortProcess()
				break
			}
			events, result, err := session.translate(line)
			if err != nil {
				protocolErr = err
				session.abortProcess()
				break
			}
			if !session.publish(events) {
				protocolErr = ErrEventBackpressure
				session.abortProcess()
				break
			}
			terminal = result
		}
		if len(stdout) > maxProtocolLineBytes && protocolErr == nil {
			protocolErr = fmt.Errorf("%w: message exceeds the size limit", ErrProtocol)
			session.abortProcess()
		}
	}
	if protocolErr == nil && len(bytes.TrimSpace(stdout)) > 0 {
		if terminal != nil {
			protocolErr = fmt.Errorf("%w: output followed the terminal result", ErrProtocol)
		} else {
			events, result, err := session.translate(bytes.TrimSpace(stdout))
			if err != nil {
				protocolErr = err
			} else if !session.publish(events) {
				protocolErr = ErrEventBackpressure
			} else {
				terminal = result
			}
		}
	}

	exit, waitErr := session.process.Wait(context.Background())
	stopped, forced := session.stopState()
	if stopped || forced {
		summary := "Claude Code was stopped."
		if forced {
			summary = "Claude Code was force-stopped."
		}
		session.complete(worker.Result{
			Outcome:           worker.OutcomeStopped,
			ProviderSessionID: session.providerSessionID,
			Summary:           summary,
		}, nil)
		return
	}
	if protocolErr != nil {
		session.complete(worker.Result{}, protocolErr)
		return
	}
	if waitErr != nil {
		session.complete(worker.Result{}, fmt.Errorf("wait for Claude Code: %w", waitErr))
		return
	}
	if terminal == nil {
		session.complete(worker.Result{}, fmt.Errorf(
			"%w: Claude Code exited before returning a result (exit code %d, signal %q)",
			ErrProtocol, exit.ExitCode, exit.Signal,
		))
		return
	}
	if terminal.Outcome == worker.OutcomeCompleted && exit.ExitCode != 0 {
		session.complete(worker.Result{}, fmt.Errorf(
			"%w: successful result exited with code %d", ErrProtocol, exit.ExitCode,
		))
		return
	}
	if err := terminal.Validate(); err != nil {
		session.complete(worker.Result{}, fmt.Errorf("%w: invalid terminal result: %v", ErrProtocol, err))
		return
	}
	session.complete(*terminal, nil)
}

type streamMessage struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	SessionID        string          `json:"session_id"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	CWD              string          `json:"cwd"`
	Message          json.RawMessage `json:"message"`
}

type messageBody struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func (session *session) translate(raw []byte) ([]worker.Event, *worker.Result, error) {
	var message streamMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, nil, fmt.Errorf("%w: decode JSONL message", ErrProtocol)
	}
	if strings.TrimSpace(message.Type) == "" {
		return nil, nil, fmt.Errorf("%w: message omitted its type", ErrProtocol)
	}
	switch message.Type {
	case "system":
		if message.Subtype != "init" {
			return nil, nil, nil
		}
		if err := session.verifyScope(message.SessionID); err != nil {
			return nil, nil, err
		}
		if message.CWD != session.workingDirectory {
			return nil, nil, fmt.Errorf(
				"%w: init working directory %q does not match %q",
				ErrProtocol, message.CWD, session.workingDirectory,
			)
		}
		session.mu.Lock()
		if session.initialized {
			session.mu.Unlock()
			return nil, nil, fmt.Errorf("%w: duplicate init message", ErrProtocol)
		}
		session.initialized = true
		session.mu.Unlock()
		return []worker.Event{{Type: worker.EventActivity, Text: "Claude started working."}}, nil, nil

	case "assistant", "user":
		if err := session.requireInitialized(message.SessionID); err != nil {
			return nil, nil, err
		}
		var body messageBody
		if err := json.Unmarshal(message.Message, &body); err != nil {
			return nil, nil, fmt.Errorf("%w: decode %s message", ErrProtocol, message.Type)
		}
		if body.Role != message.Type {
			return nil, nil, fmt.Errorf("%w: %s message used role %q", ErrProtocol, message.Type, body.Role)
		}
		if message.Type == "assistant" {
			return session.assistantEvents(body.Content), nil, nil
		}
		return session.userEvents(body.Content), nil, nil

	case "result":
		if err := session.requireInitialized(message.SessionID); err != nil {
			return nil, nil, err
		}
		return session.translateResult(message)

	case "stream_event", "rate_limit_event", "tool_progress", "auth_status":
		return nil, nil, nil
	default:
		// Claude Code uses semantic versioning for this stream. Unknown records
		// are ignored unless they claim to be one of the state-bearing types above.
		return nil, nil, nil
	}
}

func (session *session) assistantEvents(blocks []contentBlock) []worker.Event {
	events := make([]worker.Event, 0)
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text == "" || session.outputContract != "" {
				continue
			}
			session.mu.Lock()
			session.lastAgentMessage = text
			session.agentMessages++
			session.mu.Unlock()
			events = append(events, worker.Event{Type: worker.EventMessage, Text: text})
		case "tool_use", "server_tool_use":
			name := strings.TrimSpace(block.Name)
			id := strings.TrimSpace(block.ID)
			if name == "" || id == "" {
				continue
			}
			session.mu.Lock()
			session.toolNames[id] = name
			session.mu.Unlock()
			events = append(events, worker.Event{Type: worker.EventActivity, Text: startedToolText(name)})
		}
	}
	return events
}

func (session *session) userEvents(blocks []contentBlock) []worker.Event {
	events := make([]worker.Event, 0)
	for _, block := range blocks {
		if block.Type != "tool_result" || strings.TrimSpace(block.ToolUseID) == "" {
			continue
		}
		session.mu.Lock()
		name := session.toolNames[block.ToolUseID]
		delete(session.toolNames, block.ToolUseID)
		session.mu.Unlock()
		text := finishedToolText(name, block.IsError)
		events = append(events, worker.Event{Type: worker.EventActivity, Text: text})
	}
	return events
}

func (session *session) translateResult(
	message streamMessage,
) ([]worker.Event, *worker.Result, error) {
	if message.Subtype != "success" || message.IsError {
		summary := strings.TrimSpace(message.Result)
		if summary == "" {
			summary = "Claude reported that the turn failed."
		}
		return nil, &worker.Result{
			Outcome:           worker.OutcomeFailed,
			ProviderSessionID: session.providerSessionID,
			Summary:           summary,
		}, nil
	}
	if session.outputContract != "" {
		if len(message.StructuredOutput) == 0 || bytes.Equal(bytes.TrimSpace(message.StructuredOutput), []byte("null")) {
			return nil, nil, fmt.Errorf("%w: structured turn omitted structured_output", ErrProtocol)
		}
		resolved, err := worker.ResolveStructuredOutput(session.outputContract, message.StructuredOutput)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		session.mu.Lock()
		session.lastAgentMessage = resolved.Event.Text
		session.mu.Unlock()
		return []worker.Event{resolved.Event}, &worker.Result{
			Outcome: worker.OutcomeCompleted, Disposition: resolved.Disposition,
			ProviderSessionID: session.providerSessionID, Summary: resolved.Event.Text,
			Publication: resolved.Publication, Review: resolved.Review,
		}, nil
	}

	text := strings.TrimSpace(message.Result)
	session.mu.Lock()
	lastMessage := session.lastAgentMessage
	messageCount := session.agentMessages
	if text != "" && lastMessage == "" {
		session.lastAgentMessage = text
		lastMessage = text
	}
	session.mu.Unlock()
	events := make([]worker.Event, 0, 1)
	if text != "" && messageCount == 0 {
		// Claude normally repeats its final assistant text in the result record.
		// Emit the result only when the stream did not already carry an assistant
		// message, so API consumers never see the same answer twice.
		events = append(events, worker.Event{Type: worker.EventMessage, Text: text})
	}
	if lastMessage == "" {
		lastMessage = "Claude completed the turn."
	}
	return events, &worker.Result{
		Outcome: worker.OutcomeCompleted, Disposition: worker.DispositionSucceeded,
		ProviderSessionID: session.providerSessionID, Summary: lastMessage,
	}, nil
}

func (session *session) verifyScope(providerSessionID string) error {
	if providerSessionID != session.providerSessionID {
		return fmt.Errorf(
			"%w: message session %q does not match %q",
			ErrProtocol, providerSessionID, session.providerSessionID,
		)
	}
	return nil
}

func (session *session) requireInitialized(providerSessionID string) error {
	if err := session.verifyScope(providerSessionID); err != nil {
		return err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.initialized {
		return fmt.Errorf("%w: message arrived before init", ErrProtocol)
	}
	return nil
}

func (session *session) publish(events []worker.Event) bool {
	for _, event := range events {
		select {
		case session.events <- event:
		default:
			return false
		}
	}
	return true
}

func (session *session) abortProcess() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), session.shutdownTimeout)
		defer cancel()
		_, _ = session.process.ForceStop(ctx)
	}()
}

func (session *session) stopState() (bool, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.stopRequested, session.forced
}

func (session *session) complete(result worker.Result, err error) {
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return
	}
	session.finished = true
	session.result = result
	session.waitErr = err
	close(session.events)
	close(session.done)
	session.mu.Unlock()
}

func startedToolText(name string) string {
	switch name {
	case "Edit", "Write", "NotebookEdit":
		return "Claude started a file change."
	case "WebSearch", "WebFetch":
		return "Claude started a web request."
	case "Task", "Agent":
		return "Claude started delegated agent work."
	case "Read", "Glob", "Grep":
		return "Claude started inspecting the workspace."
	default:
		return "Claude started a command or tool."
	}
}

func finishedToolText(name string, failed bool) string {
	result := "finished"
	if failed {
		result = "failed"
	}
	switch name {
	case "Edit", "Write", "NotebookEdit":
		return "Claude " + result + " a file change."
	case "WebSearch", "WebFetch":
		return "Claude " + result + " a web request."
	case "Task", "Agent":
		return "Claude " + result + " delegated agent work."
	case "Read", "Glob", "Grep":
		return "Claude " + result + " inspecting the workspace."
	default:
		return "Claude " + result + " a command or tool."
	}
}
