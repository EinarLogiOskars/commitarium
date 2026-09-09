package codexadapter

import (
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

var (
	ErrUnsupportedCommand = errors.New("Codex adapter command is unsupported")
	ErrSessionFinished    = errors.New("Codex session is already finished")
	ErrEventBackpressure  = errors.New("Codex activity consumer fell behind")
)

type session struct {
	client          *protocolClient
	process         *processsupervisor.Process
	threadID        string
	shutdownTimeout time.Duration
	events          chan worker.Event
	done            chan struct{}

	mu               sync.Mutex
	turnID           string
	lastAgentMessage string
	forced           bool
	result           worker.Result
	waitErr          error
	finished         bool
}

var _ worker.ForceStoppableSession = (*session)(nil)

func newSession(
	client *protocolClient,
	process *processsupervisor.Process,
	threadID string,
	shutdownTimeout time.Duration,
	eventBuffer int,
) *session {
	return &session{
		client:          client,
		process:         process,
		threadID:        threadID,
		shutdownTimeout: shutdownTimeout,
		events:          make(chan worker.Event, eventBuffer),
		done:            make(chan struct{}),
	}
}

func (session *session) start(turnID string) {
	session.mu.Lock()
	session.turnID = turnID
	session.mu.Unlock()
	go session.run()
}

func (session *session) ProviderSessionID() string {
	if session == nil {
		return ""
	}
	return session.threadID
}

func (session *session) Events() <-chan worker.Event {
	if session == nil {
		return nil
	}
	return session.events
}

func (session *session) Send(ctx context.Context, command worker.Command) error {
	if session == nil {
		return errors.New("Codex session is required")
	}
	if err := command.Validate(); err != nil {
		return err
	}
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return ErrSessionFinished
	}
	threadID := session.threadID
	turnID := session.turnID
	session.mu.Unlock()

	switch command.Type {
	case worker.CommandMessage:
		params := struct {
			ThreadID            string      `json:"threadId"`
			ExpectedTurnID      string      `json:"expectedTurnId"`
			ClientUserMessageID string      `json:"clientUserMessageId"`
			Input               []textInput `json:"input"`
		}{
			ThreadID:            threadID,
			ExpectedTurnID:      turnID,
			ClientUserMessageID: command.ID,
			Input:               []textInput{{Type: "text", Text: command.Message}},
		}
		var response struct {
			TurnID string `json:"turnId"`
		}
		if err := session.client.request(ctx, "turn/steer", params, &response); err != nil {
			return fmt.Errorf("steer Codex turn: %w", err)
		}
		if response.TurnID != turnID {
			return fmt.Errorf(
				"%w: turn/steer returned turn %q, want %q",
				ErrProtocol,
				response.TurnID,
				turnID,
			)
		}
		return nil
	case worker.CommandStop:
		params := struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
		}{ThreadID: threadID, TurnID: turnID}
		var response map[string]any
		if err := session.client.request(ctx, "turn/interrupt", params, &response); err != nil {
			return fmt.Errorf("interrupt Codex turn: %w", err)
		}
		return nil
	case worker.CommandPause, worker.CommandContinue:
		return fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedCommand, command.Type)
	}
}

func (session *session) Wait(ctx context.Context) (worker.Result, error) {
	if session == nil {
		return worker.Result{}, errors.New("Codex session is required")
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
		return errors.New("Codex session is required")
	}
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return nil
	}
	session.forced = true
	session.mu.Unlock()
	if _, err := session.process.ForceStop(ctx); err != nil {
		return fmt.Errorf("force-stop Codex app-server: %w", err)
	}
	return nil
}

func (session *session) run() {
	for {
		select {
		case notification, open := <-session.client.notifications:
			if !open {
				session.finishFromProcessExit()
				return
			}
			event, terminal, result, err := session.translate(notification)
			if err != nil {
				session.stopProcess()
				session.complete(worker.Result{}, err)
				return
			}
			if event != nil {
				select {
				case session.events <- *event:
				default:
					session.stopProcess()
					session.complete(worker.Result{}, ErrEventBackpressure)
					return
				}
			}
			if terminal {
				session.stopProcess()
				session.complete(result, nil)
				return
			}
		case <-session.client.done:
			session.finishFromProcessExit()
			return
		}
	}
}

func (session *session) finishFromProcessExit() {
	session.mu.Lock()
	forced := session.forced
	session.mu.Unlock()
	if forced {
		session.complete(worker.Result{
			Outcome:           worker.OutcomeStopped,
			ProviderSessionID: session.threadID,
			Summary:           "Codex process was force-stopped.",
		}, nil)
		return
	}
	session.complete(worker.Result{}, session.client.connectionError())
}

func (session *session) stopAfterFailedLaunch() {
	session.stopProcess()
	session.complete(worker.Result{}, errors.New("Codex turn did not start"))
}

func (session *session) stopProcess() {
	ctx, cancel := context.WithTimeout(context.Background(), session.shutdownTimeout)
	_, err := session.process.Terminate(ctx)
	cancel()
	if err == nil {
		return
	}
	forceCtx, forceCancel := context.WithTimeout(context.Background(), session.shutdownTimeout)
	defer forceCancel()
	_, _ = session.process.ForceStop(forceCtx)
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

type scopedNotification struct {
	ThreadID  string     `json:"threadId"`
	TurnID    string     `json:"turnId"`
	Turn      turnRecord `json:"turn"`
	Item      threadItem `json:"item"`
	WillRetry bool       `json:"willRetry"`
}

type turnRecord struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type threadItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Text     string `json:"text"`
	Status   string `json:"status"`
	ExitCode *int   `json:"exitCode"`
}

func (session *session) translate(
	message protocolMessage,
) (*worker.Event, bool, worker.Result, error) {
	switch message.Method {
	case "thread/started":
		var params struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := decodeParams(message, &params); err != nil {
			return nil, false, worker.Result{}, err
		}
		if strings.TrimSpace(params.Thread.ID) == "" || params.Thread.ID != session.threadID {
			return nil, false, worker.Result{}, session.scopeError(params.Thread.ID, "")
		}
		return nil, false, worker.Result{}, nil
	case "turn/started", "turn/completed", "item/started", "item/completed", "error":
		var params scopedNotification
		if err := decodeParams(message, &params); err != nil {
			return nil, false, worker.Result{}, err
		}
		turnID := params.TurnID
		if turnID == "" {
			turnID = params.Turn.ID
		}
		if params.ThreadID != session.threadID || turnID != session.currentTurnID() {
			return nil, false, worker.Result{}, session.scopeError(params.ThreadID, turnID)
		}
		switch message.Method {
		case "turn/started":
			if params.Turn.Status != "inProgress" {
				return nil, false, worker.Result{}, fmt.Errorf(
					"%w: turn/started used status %q",
					ErrProtocol,
					params.Turn.Status,
				)
			}
			return event(worker.EventActivity, "Codex started working."), false, worker.Result{}, nil
		case "item/started":
			if strings.TrimSpace(params.Item.ID) == "" || strings.TrimSpace(params.Item.Type) == "" {
				return nil, false, worker.Result{}, fmt.Errorf("%w: item/started omitted item identity or type", ErrProtocol)
			}
			return session.startedItem(params.Item), false, worker.Result{}, nil
		case "item/completed":
			if strings.TrimSpace(params.Item.ID) == "" || strings.TrimSpace(params.Item.Type) == "" {
				return nil, false, worker.Result{}, fmt.Errorf("%w: item/completed omitted item identity or type", ErrProtocol)
			}
			return session.completedItem(params.Item), false, worker.Result{}, nil
		case "error":
			text := "Codex reported an error."
			if params.WillRetry {
				text = "Codex reported a temporary error and will retry."
			}
			return event(worker.EventActivity, text), false, worker.Result{}, nil
		case "turn/completed":
			return session.completedTurn(params.Turn)
		}
	case "item/agentMessage/delta", "turn/diff/updated", "thread/tokenUsage/updated":
		// Deltas are intentionally ignored. The completed item is authoritative,
		// avoids duplicate text, and passes through the worker's safety filter.
		return nil, false, worker.Result{}, nil
	default:
		// App Server evolves quickly and emits many optional notifications. An
		// unknown notification cannot change Commitarium state, so it is ignored.
		return nil, false, worker.Result{}, nil
	}
	return nil, false, worker.Result{}, nil
}

func (session *session) startedItem(item threadItem) *worker.Event {
	switch item.Type {
	case "commandExecution":
		return event(worker.EventActivity, "Codex started a command.")
	case "fileChange":
		return event(worker.EventActivity, "Codex started a file change.")
	case "webSearch":
		return event(worker.EventActivity, "Codex started a web search.")
	case "mcpToolCall", "dynamicToolCall":
		return event(worker.EventActivity, "Codex started a tool call.")
	case "subAgentActivity", "collabAgentToolCall":
		return event(worker.EventActivity, "Codex started delegated agent work.")
	default:
		return nil
	}
}

func (session *session) completedItem(item threadItem) *worker.Event {
	switch item.Type {
	case "agentMessage":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return nil
		}
		session.mu.Lock()
		session.lastAgentMessage = text
		session.mu.Unlock()
		return event(worker.EventMessage, text)
	case "commandExecution":
		if item.ExitCode != nil {
			return event(worker.EventActivity, fmt.Sprintf("Codex finished a command with exit code %d.", *item.ExitCode))
		}
		return event(worker.EventActivity, "Codex finished a command.")
	case "fileChange":
		return event(worker.EventActivity, "Codex finished a file change.")
	case "webSearch":
		return event(worker.EventActivity, "Codex finished a web search.")
	case "mcpToolCall", "dynamicToolCall":
		return event(worker.EventActivity, "Codex finished a tool call.")
	case "plan":
		return event(worker.EventActivity, "Codex updated its plan.")
	case "subAgentActivity", "collabAgentToolCall":
		return event(worker.EventActivity, "Codex finished delegated agent work.")
	case "reasoning":
		return nil
	default:
		return nil
	}
}

func (session *session) completedTurn(
	turn turnRecord,
) (*worker.Event, bool, worker.Result, error) {
	summary := session.summary()
	switch turn.Status {
	case "completed":
		return nil, true, worker.Result{
			Outcome:           worker.OutcomeCompleted,
			Disposition:       worker.DispositionSucceeded,
			ProviderSessionID: session.threadID,
			Summary:           summary,
		}, nil
	case "interrupted":
		return nil, true, worker.Result{
			Outcome:           worker.OutcomeStopped,
			ProviderSessionID: session.threadID,
			Summary:           "Codex turn was interrupted.",
		}, nil
	case "failed":
		return nil, true, worker.Result{
			Outcome:           worker.OutcomeFailed,
			ProviderSessionID: session.threadID,
			Summary:           "Codex reported that the turn failed.",
		}, nil
	default:
		return nil, false, worker.Result{}, fmt.Errorf(
			"%w: turn/completed used status %q",
			ErrProtocol,
			turn.Status,
		)
	}
}

func (session *session) summary() string {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.lastAgentMessage != "" {
		return session.lastAgentMessage
	}
	return "Codex completed the turn."
}

func (session *session) currentTurnID() string {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.turnID
}

func (session *session) scopeError(threadID string, turnID string) error {
	return fmt.Errorf(
		"%w: notification scope thread=%q turn=%q, want thread=%q turn=%q",
		ErrProtocol,
		threadID,
		turnID,
		session.threadID,
		session.currentTurnID(),
	)
}

func decodeParams(message protocolMessage, destination any) error {
	if len(message.Params) == 0 {
		return fmt.Errorf("%w: %s omitted params", ErrProtocol, message.Method)
	}
	if err := json.Unmarshal(message.Params, destination); err != nil {
		return fmt.Errorf("%w: decode %s params", ErrProtocol, message.Method)
	}
	return nil
}

func event(eventType worker.EventType, text string) *worker.Event {
	return &worker.Event{Type: eventType, Text: text}
}
