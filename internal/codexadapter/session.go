package codexadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	outputContract  worker.OutputContract
	events          chan worker.Event
	done            chan struct{}

	mu               sync.Mutex
	turnID           string
	lastAgentMessage string
	pendingMessage   string
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
	outputContract worker.OutputContract,
) *session {
	return &session{
		client:          client,
		process:         process,
		threadID:        threadID,
		shutdownTimeout: shutdownTimeout,
		outputContract:  outputContract,
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
			completed, err := session.completedItem(params.Item)
			return completed, false, worker.Result{}, err
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

func (session *session) completedItem(item threadItem) (*worker.Event, error) {
	switch item.Type {
	case "agentMessage":
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return nil, nil
		}
		if session.outputContract == worker.OutputContractPlanningLead ||
			session.outputContract == worker.OutputContractImplementationLead ||
			session.outputContract == worker.OutputContractImplementationReview ||
			session.outputContract == worker.OutputContractImplementationReadiness {
			session.mu.Lock()
			session.pendingMessage = text
			session.mu.Unlock()
			return nil, nil
		}
		session.mu.Lock()
		session.lastAgentMessage = text
		session.mu.Unlock()
		return event(worker.EventMessage, text), nil
	case "commandExecution":
		if item.ExitCode != nil {
			return event(worker.EventActivity, fmt.Sprintf("Codex finished a command with exit code %d.", *item.ExitCode)), nil
		}
		return event(worker.EventActivity, "Codex finished a command."), nil
	case "fileChange":
		return event(worker.EventActivity, "Codex finished a file change."), nil
	case "webSearch":
		return event(worker.EventActivity, "Codex finished a web search."), nil
	case "mcpToolCall", "dynamicToolCall":
		return event(worker.EventActivity, "Codex finished a tool call."), nil
	case "plan":
		return event(worker.EventActivity, "Codex updated its plan."), nil
	case "subAgentActivity", "collabAgentToolCall":
		return event(worker.EventActivity, "Codex finished delegated agent work."), nil
	case "reasoning":
		return nil, nil
	default:
		return nil, nil
	}
}

type planningLeadResponse struct {
	Action  string `json:"action"`
	Content string `json:"content"`
}

type implementationLeadResponse struct {
	Action            string `json:"action"`
	Summary           string `json:"summary"`
	CommitID          string `json:"commit_id"`
	PullRequestNumber int64  `json:"pull_request_number"`
}

type implementationReviewResponse struct {
	Action            string `json:"action"`
	Summary           string `json:"summary"`
	CommitID          string `json:"commit_id"`
	PullRequestNumber int64  `json:"pull_request_number"`
	ReviewID          int64  `json:"review_id"`
}

type implementationReadinessResponse struct {
	Action  string `json:"action"`
	Summary string `json:"summary"`
}

func decodePlanningLeadResponse(text string) (planningLeadResponse, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	var response planningLeadResponse
	if err := decoder.Decode(&response); err != nil {
		return planningLeadResponse{}, fmt.Errorf("%w: decode planning lead response: %v", ErrProtocol, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return planningLeadResponse{}, fmt.Errorf("%w: planning lead response contains trailing JSON", ErrProtocol)
	}
	response.Content = strings.TrimSpace(response.Content)
	if response.Content == "" || (response.Action != "respond" && response.Action != "submit_plan") {
		return planningLeadResponse{}, fmt.Errorf("%w: planning lead response is incomplete", ErrProtocol)
	}
	return response, nil
}

func decodeImplementationLeadResponse(text string) (implementationLeadResponse, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	var response implementationLeadResponse
	if err := decoder.Decode(&response); err != nil {
		return implementationLeadResponse{}, fmt.Errorf(
			"%w: decode implementation lead response: %v", ErrProtocol, err,
		)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return implementationLeadResponse{}, fmt.Errorf(
			"%w: implementation lead response contains trailing JSON", ErrProtocol,
		)
	}
	response.Summary = strings.TrimSpace(response.Summary)
	response.CommitID = strings.TrimSpace(response.CommitID)
	if response.Summary == "" || response.PullRequestNumber < 1 {
		return implementationLeadResponse{}, fmt.Errorf(
			"%w: implementation lead response is incomplete", ErrProtocol,
		)
	}
	switch response.Action {
	case "published":
		publication := worker.ImplementationPublication{
			CommitID: response.CommitID, PullRequestNumber: response.PullRequestNumber,
		}
		if err := publication.Validate(); err != nil {
			return implementationLeadResponse{}, fmt.Errorf(
				"%w: implementation lead publication is invalid: %v", ErrProtocol, err,
			)
		}
	case "blocked":
		if response.CommitID != "" {
			return implementationLeadResponse{}, fmt.Errorf(
				"%w: blocked implementation cannot claim a commit", ErrProtocol,
			)
		}
	default:
		return implementationLeadResponse{}, fmt.Errorf(
			"%w: implementation lead response has an unknown action", ErrProtocol,
		)
	}
	return response, nil
}

func decodeImplementationReviewResponse(text string) (implementationReviewResponse, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	var response implementationReviewResponse
	if err := decoder.Decode(&response); err != nil {
		return implementationReviewResponse{}, fmt.Errorf(
			"%w: decode implementation review response: %v", ErrProtocol, err,
		)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return implementationReviewResponse{}, fmt.Errorf(
			"%w: implementation review response contains trailing JSON", ErrProtocol,
		)
	}
	response.Summary = strings.TrimSpace(response.Summary)
	response.CommitID = strings.TrimSpace(response.CommitID)
	if response.Summary == "" || response.PullRequestNumber < 1 {
		return implementationReviewResponse{}, fmt.Errorf(
			"%w: implementation review response is incomplete", ErrProtocol,
		)
	}
	switch response.Action {
	case "approved", "changes_requested":
		review := worker.ReviewPublication{
			CommitID: response.CommitID, PullRequestNumber: response.PullRequestNumber,
			ReviewID: response.ReviewID,
		}
		if err := review.Validate(); err != nil {
			return implementationReviewResponse{}, fmt.Errorf(
				"%w: implementation review publication is invalid: %v", ErrProtocol, err,
			)
		}
	case "blocked":
		if response.CommitID != "" || response.ReviewID != 0 {
			return implementationReviewResponse{}, fmt.Errorf(
				"%w: blocked review cannot claim a commit or review", ErrProtocol,
			)
		}
	default:
		return implementationReviewResponse{}, fmt.Errorf(
			"%w: implementation review response has an unknown action", ErrProtocol,
		)
	}
	return response, nil
}

func decodeImplementationReadinessResponse(text string) (implementationReadinessResponse, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.DisallowUnknownFields()
	var response implementationReadinessResponse
	if err := decoder.Decode(&response); err != nil {
		return implementationReadinessResponse{}, fmt.Errorf(
			"%w: decode implementation readiness response: %v", ErrProtocol, err,
		)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return implementationReadinessResponse{}, fmt.Errorf(
			"%w: implementation readiness response contains trailing JSON", ErrProtocol,
		)
	}
	response.Summary = strings.TrimSpace(response.Summary)
	if response.Summary == "" {
		return implementationReadinessResponse{}, fmt.Errorf(
			"%w: implementation readiness response is incomplete", ErrProtocol,
		)
	}
	switch response.Action {
	case "ready_to_merge", "concern", "blocked":
		return response, nil
	default:
		return implementationReadinessResponse{}, fmt.Errorf(
			"%w: implementation readiness response has an unknown action", ErrProtocol,
		)
	}
}

func (session *session) completedTurn(
	turn turnRecord,
) (*worker.Event, bool, worker.Result, error) {
	switch turn.Status {
	case "completed":
		structured, disposition, publication, review, err := session.completeStructuredResponse()
		if err != nil {
			return nil, false, worker.Result{}, err
		}
		return structured, true, worker.Result{
			Outcome:           worker.OutcomeCompleted,
			Disposition:       disposition,
			ProviderSessionID: session.threadID,
			Summary:           session.summary(),
			Publication:       publication,
			Review:            review,
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

func (session *session) completeStructuredResponse() (
	*worker.Event,
	worker.Disposition,
	*worker.ImplementationPublication,
	*worker.ReviewPublication,
	error,
) {
	if session.outputContract == "" {
		return nil, worker.DispositionSucceeded, nil, nil, nil
	}
	session.mu.Lock()
	raw := session.pendingMessage
	session.mu.Unlock()
	if strings.TrimSpace(raw) == "" {
		return nil, "", nil, nil, fmt.Errorf(
			"%w: structured turn omitted its final response", ErrProtocol,
		)
	}
	switch session.outputContract {
	case worker.OutputContractPlanningLead:
		response, err := decodePlanningLeadResponse(raw)
		if err != nil {
			return nil, "", nil, nil, err
		}
		eventType := worker.EventMessage
		if response.Action == "submit_plan" {
			eventType = worker.EventPlanSubmitted
		}
		session.mu.Lock()
		session.lastAgentMessage = response.Content
		session.mu.Unlock()
		return event(eventType, response.Content), worker.DispositionSucceeded, nil, nil, nil
	case worker.OutputContractImplementationLead:
		response, err := decodeImplementationLeadResponse(raw)
		if err != nil {
			return nil, "", nil, nil, err
		}
		session.mu.Lock()
		session.lastAgentMessage = response.Summary
		session.mu.Unlock()
		if response.Action == "blocked" {
			return event(worker.EventInputRequired, response.Summary),
				worker.DispositionInputRequired, nil, nil, nil
		}
		return event(worker.EventMessage, response.Summary),
			worker.DispositionSucceeded,
			&worker.ImplementationPublication{
				CommitID: response.CommitID, PullRequestNumber: response.PullRequestNumber,
			}, nil, nil
	case worker.OutputContractImplementationReview:
		response, err := decodeImplementationReviewResponse(raw)
		if err != nil {
			return nil, "", nil, nil, err
		}
		session.mu.Lock()
		session.lastAgentMessage = response.Summary
		session.mu.Unlock()
		if response.Action == "blocked" {
			return event(worker.EventInputRequired, response.Summary),
				worker.DispositionInputRequired, nil, nil, nil
		}
		disposition := worker.DispositionSucceeded
		if response.Action == "changes_requested" {
			disposition = worker.DispositionChangesRequested
		}
		return event(worker.EventMessage, response.Summary), disposition, nil,
			&worker.ReviewPublication{
				CommitID: response.CommitID, PullRequestNumber: response.PullRequestNumber,
				ReviewID: response.ReviewID,
			}, nil
	case worker.OutputContractImplementationReadiness:
		response, err := decodeImplementationReadinessResponse(raw)
		if err != nil {
			return nil, "", nil, nil, err
		}
		session.mu.Lock()
		session.lastAgentMessage = response.Summary
		session.mu.Unlock()
		switch response.Action {
		case "ready_to_merge":
			return event(worker.EventMessage, response.Summary), worker.DispositionSucceeded, nil, nil, nil
		case "concern":
			return event(worker.EventMessage, response.Summary), worker.DispositionChangesRequested, nil, nil, nil
		default:
			return event(worker.EventInputRequired, response.Summary), worker.DispositionInputRequired, nil, nil, nil
		}
	default:
		return nil, "", nil, nil, fmt.Errorf(
			"%w: unsupported structured output contract %q", ErrProtocol, session.outputContract,
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
