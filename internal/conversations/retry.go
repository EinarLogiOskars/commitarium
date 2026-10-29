package conversations

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/redact"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A turn that ends without a reply runs again by itself before the user
// hears of it, as an agent's refused turn is handed back (ADR-032). When its
// reply was one Commitarium could not accept (too many actions, a missing or
// invalid field), the turn runs again with the reason, so the assistant
// fixes exactly that. When the worker or the provider's service failed it,
// the turn runs again as it was after a short wait. Only once the retries
// run out does the user see a plain line, with a Retry for the whole turn
// and, in Developer mode, the exact reason.

// maxTurnRetries is how many times one turn runs again by itself.
const maxTurnRetries = 2

// transientRetryDelays are the waits before a turn the worker or provider
// failed runs again, by retry.
var transientRetryDelays = []time.Duration{5 * time.Second, 30 * time.Second}

// maxRetryReason bounds the reason a correction quotes.
const maxRetryReason = 2000

// turnRetry is a turn's automatic retry.
type turnRetry struct {
	// Count is how many retries started or are due, from 1.
	Count int `json:"count"`
	// DueAt is set while the retry waits to start.
	DueAt *time.Time `json:"due_at,omitempty"`
	// Correction is the reason handed back to the assistant, or "" when
	// the turn runs again as it was.
	Correction string `json:"correction,omitempty"`
}

// TurnFailure is a turn that ended without a reply once its retries ran
// out.
type TurnFailure struct {
	// MessageID is the line that tells the user.
	MessageID string `json:"message_id"`
	// Detail is the exact reason, for Developer mode.
	Detail string `json:"detail,omitempty"`
}

type failureKind int

const (
	// failureFinal does not get better by running the turn again.
	failureFinal failureKind = iota
	// failureCorrectable is a reply the assistant can fix.
	failureCorrectable
	// failureTransient is the worker or the provider's service failing.
	failureTransient
)

// turnFailure says why a finished turn gave no reply, and whether running it
// again can help.
func turnFailure(attempt workerhttp.Attempt) (failureKind, string) {
	result := attempt.Result
	switch {
	case attempt.State == workerhttp.AttemptStateIndeterminate:
		return failureTransient, "the worker restarted during the turn"
	case result == nil:
		return failureTransient, "the turn ended without a result"
	case result.Error == nil:
		return failureFinal, strings.TrimSpace(string(result.Outcome) + ": " + result.Summary)
	case result.Error.Code == workerhttp.ErrorIncompleteResult:
		return failureCorrectable, invalidReplyReason(result.Error.Message)
	case result.Error.Code == workerhttp.ErrorInternal &&
		(worker.IsTransientFailure(result.Error.Message) || worker.IsTransientFailure(result.Summary)):
		return failureTransient, "the provider's service failed: " + result.Error.Message
	}
	return failureFinal, string(result.Error.Code) + ": " + result.Error.Message
}

// invalidReplyReason is what was wrong with a reply, without the layers of
// the worker's protocol around it.
func invalidReplyReason(message string) string {
	reason := strings.TrimSpace(message)
	if _, after, found := strings.Cut(reason, worker.ErrInvalidStructuredOutput.Error()+": "); found {
		reason = after
	} else {
		return "your reply ended without the structured result it must end with (" + reason + ")"
	}
	for _, layer := range []string{"assistant turn: ", "assistant planning turn: ", "invalid feature artifact: "} {
		reason = strings.TrimPrefix(reason, layer)
	}
	return reason
}

// failTurn handles a turn that ended without a reply: it schedules the
// turn's next retry while one is left, or tells the user. It leaves the
// conversation for the caller to write.
func (service *Service) failTurn(stored *record, kind failureKind, reason string, now time.Time) {
	reason = redact.Secrets(strings.TrimSpace(reason))
	log.Printf("assistant turn %q failed: %s", stored.AttemptID, reason)
	// The next run starts a fresh provider session from the state.
	stored.ProviderSessionID = ""
	tried := 0
	if stored.Retry != nil {
		tried = stored.Retry.Count
	}
	if kind != failureFinal && tried < maxTurnRetries {
		retry := &turnRetry{Count: tried + 1}
		due := now
		if kind == failureCorrectable {
			retry.Correction = reason
		} else {
			due = now.Add(transientRetryDelays[min(tried, len(transientRetryDelays)-1)])
		}
		retry.DueAt = &due
		stored.Retry = retry
		stored.Status = StatusRunning
		stored.UpdatedAt = now
		return
	}
	stored.Retry = nil
	message := Message{
		ID: messageID(*stored, len(stored.Messages)), Role: "event", OccurredAt: now,
		Text: "Mayka couldn't finish her reply: " + plainFailure(kind, reason),
	}
	stored.Messages = append(stored.Messages, message)
	stored.Failure = &TurnFailure{MessageID: message.ID, Detail: reason}
	stored.Status = StatusIdle
	stored.UpdatedAt = now
	if len(stored.Queued) > 0 || len(stored.PendingEvents) > 0 {
		service.nudgeWatcher()
	}
}

// plainFailure says in the user's words what went wrong.
func plainFailure(kind failureKind, reason string) string {
	switch {
	case strings.Contains(reason, "actions per turn"):
		return "it tried to do too much at once."
	case kind == failureCorrectable:
		return "its reply was not in a form Commitarium could use."
	case strings.Contains(reason, "worker"):
		return "its agent could not be reached."
	case kind == failureTransient:
		return "its agent's service did not answer."
	}
	return "its agent stopped with an error."
}

// correctionPrompt tells the assistant what was wrong with its last reply.
func correctionPrompt(reason string) string {
	reason = strings.TrimSuffix(strings.TrimSpace(reason), ".")
	reason = bounded(reason, maxRetryReason)
	return "Commitarium couldn't accept your last reply: " + reason + ". Send it again with that fixed; if you " +
		"have more actions than allowed, send the first ones now and the rest next turn."
}

// failAndRetry fails a turn and starts its retry when that is due at once.
func (service *Service) failAndRetry(
	ctx context.Context, stored record, kind failureKind, reason string, now time.Time,
) (record, error) {
	service.failTurn(&stored, kind, reason, now)
	if err := service.write(stored); err != nil || stored.Status != StatusRunning {
		return stored, err
	}
	return service.retryDue(ctx, stored)
}

// retryDue starts the turn's retry once it is due. It returns the
// conversation as it then stands.
func (service *Service) retryDue(ctx context.Context, stored record) (record, error) {
	retry := stored.Retry
	if retry == nil || retry.DueAt == nil || service.now().Before(*retry.DueAt) || stored.LastTurn == nil {
		return stored, nil
	}
	settings, err := service.Readiness(ctx)
	if err != nil {
		service.failTurn(&stored, failureFinal, "the assistant is not set up: "+err.Error(), service.now())
		return stored, service.write(stored)
	}
	retry.DueAt = nil
	prompt := stored.LastTurn.Prompt
	if retry.Correction != "" {
		prompt += "\n\n" + correctionPrompt(retry.Correction)
	}
	key := fmt.Sprintf("retry:%d:%d", stored.Turn, retry.Count)
	if _, err := service.launchTurn(ctx, stored, settings, key, stored.LastTurn.Role, stored.LastTurn.Prompt, prompt); err != nil {
		return stored, err
	}
	return service.readChat(stored.Scope, stored.Chat)
}

// RetryTurn runs again the turn whose retries ran out, as the user asked,
// in ctx's chat or the scope's most recently used one.
func (service *Service) RetryTurn(ctx context.Context, scope string) (Conversation, error) {
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	settings, err := service.Readiness(ctx)
	if err != nil {
		return Conversation{}, err
	}
	chat, err := service.chatFor(ctx, scope)
	if err != nil {
		return Conversation{}, err
	}
	stored, done, err := service.open(scope, chat)
	if err != nil {
		return Conversation{}, err
	}
	defer done()
	if stored.Status == StatusRunning {
		return Conversation{}, ErrBusy
	}
	if stored.Failure == nil || stored.LastTurn == nil {
		return Conversation{}, ErrNothingToRetry
	}
	prompt := stored.LastTurn.Prompt
	if len(stored.PendingEvents) > 0 {
		prompt = eventsPrompt(stored.PendingEvents) + "\n\n" + prompt
		stored.PendingEvents = nil
	}
	return service.startTurn(ctx, stored, settings, fmt.Sprintf("retry-turn:%d", stored.Turn+1), stored.LastTurn.Role, prompt)
}
