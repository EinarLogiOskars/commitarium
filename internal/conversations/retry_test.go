package conversations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// invalidReply is how the worker ends a turn whose structured reply failed
// its contract.
func invalidReply(reason string) workerhttp.TerminalResult {
	return workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeFailed, Summary: "The provider process ended without returning a usable structured result.",
		Error: &workerhttp.ProtocolError{
			Code: workerhttp.ErrorIncompleteResult, Retryable: true,
			Message: "invalid Claude Code stream protocol: invalid structured worker output: assistant turn: " +
				"invalid feature artifact: " + reason,
		},
	}
}

func replied(summary string) workerhttp.TerminalResult {
	return workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: summary,
		AssistantTurn: &featureartifact.AssistantTurn{},
	}
}

// A reply Commitarium refuses goes back to the assistant with the reason,
// and its corrected reply is the turn's.
func TestAnInvalidTurnIsRetriedWithItsReason(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "Build the whole vision", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(invalidReply("at most 8 actions per turn"))
	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil || conversation.Status != StatusRunning || len(worker.puts) != 2 {
		t.Fatalf("the refused turn did not run again: %+v puts=%d err=%v", conversation, len(worker.puts), err)
	}
	retry := worker.puts[1].Instructions
	if !strings.Contains(retry, "Build the whole vision") ||
		!strings.Contains(retry, "Commitarium couldn't accept your last reply: at most 8 actions per turn. "+
			"Send it again with that fixed; if you have more actions than allowed, send the first ones now") {
		t.Fatalf("the retry did not hand the reason back:\n%s", retry)
	}
	if conversation.Activity == nil || conversation.Activity.Retry != 1 {
		t.Fatalf("the activity does not show the retry: %+v", conversation.Activity)
	}
	worker.finish(replied("Starting the first eight now."))
	conversation, err = service.Get(t.Context(), "prj_test")
	if err != nil || conversation.Status != StatusIdle || conversation.Failure != nil || conversation.Activity != nil {
		t.Fatalf("after the corrected reply = %+v err=%v", conversation, err)
	}
	last := conversation.Messages[len(conversation.Messages)-1]
	if last.Role != "assistant" || last.Text != "Starting the first eight now." ||
		strings.Count(messagesText(conversation), "couldn't") != 0 {
		t.Fatalf("the user saw the failure: %+v", conversation.Messages)
	}
	stored, _ := service.read("prj_test")
	if stored.Retry != nil || stored.LastTurn.Prompt != "The user says:\nBuild the whole vision" {
		t.Fatalf("the retry stayed: retry=%+v last=%+v", stored.Retry, stored.LastTurn)
	}
}

// Once the retries run out the user hears plainly what happened, and Retry
// runs the turn again without retyping.
func TestRetriesRunOutInAPlainMessageAndRetry(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "Build the whole vision", "msg-1"); err != nil {
		t.Fatal(err)
	}
	for range maxTurnRetries + 1 {
		worker.finish(invalidReply("at most 8 actions per turn"))
		if _, err := service.Get(t.Context(), "prj_test"); err != nil {
			t.Fatal(err)
		}
	}
	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil || conversation.Status != StatusIdle || len(worker.puts) != maxTurnRetries+1 {
		t.Fatalf("after the retries = %+v puts=%d err=%v", conversation, len(worker.puts), err)
	}
	last := conversation.Messages[len(conversation.Messages)-1]
	if last.Role != "event" || last.Text != "The assistant couldn't finish its reply: it tried to do too much at once." {
		t.Fatalf("the failure line = %+v", last)
	}
	if conversation.Failure == nil || conversation.Failure.MessageID != last.ID ||
		conversation.Failure.Detail != "at most 8 actions per turn" {
		t.Fatalf("failure = %+v", conversation.Failure)
	}
	if strings.Contains(messagesText(conversation), "Send your message again") {
		t.Fatal("the user was told to send their message again")
	}

	conversation, err = service.RetryTurn(t.Context(), "prj_test")
	if err != nil || conversation.Status != StatusRunning || conversation.Failure != nil ||
		len(worker.puts) != maxTurnRetries+2 || !strings.Contains(worker.puts[len(worker.puts)-1].Instructions, "Build the whole vision") {
		t.Fatalf("retry = %+v puts=%d err=%v", conversation, len(worker.puts), err)
	}
	if _, err := service.RetryTurn(t.Context(), "prj_test"); !errors.Is(err, ErrBusy) {
		t.Fatalf("a retry while one runs: %v", err)
	}
	worker.finish(replied("Done."))
	if _, err := service.Get(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RetryTurn(t.Context(), "prj_test"); !errors.Is(err, ErrNothingToRetry) {
		t.Fatalf("a retry with nothing failed: %v", err)
	}
}

// unreachableWorker fails its first puts, as a worker that is restarting.
type unreachableWorker struct {
	*workerStub
	failures int
}

func (worker *unreachableWorker) PutAttempt(
	ctx context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	if worker.failures > 0 {
		worker.failures--
		return workerhttp.Attempt{}, false, workerhttp.ErrRequestFailed
	}
	return worker.workerStub.PutAttempt(ctx, identity, request)
}

// A worker that cannot be reached, or restarts during the turn, has the turn
// run again after a wait, as it was.
func TestTransientFailuresRetry(t *testing.T) {
	service, stub := newTestService(t)
	worker := &unreachableWorker{workerStub: stub, failures: 1}
	service.config.Workers = func(string) (workerhttp.Service, error) { return worker, nil }
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	conversation, err := service.Send(t.Context(), "prj_test", "Hi", "msg-1")
	if err != nil || conversation.Status != StatusRunning || len(stub.puts) != 0 ||
		conversation.Activity != nil || len(conversation.Messages) != 1 {
		t.Fatalf("an unreachable worker = %+v err=%v", conversation, err)
	}
	if conversation, _ = service.Get(t.Context(), "prj_test"); conversation.Activity == nil ||
		conversation.Activity.Label != "Trying again" || len(stub.puts) != 0 {
		t.Fatalf("before the retry is due: %+v puts=%d", conversation.Activity, len(stub.puts))
	}
	now = now.Add(transientRetryDelays[0])
	if conversation, err = service.Get(t.Context(), "prj_test"); err != nil || len(stub.puts) != 1 ||
		conversation.Status != StatusRunning {
		t.Fatalf("the due retry did not start: %+v puts=%d err=%v", conversation, len(stub.puts), err)
	}

	stub.attempt.State = workerhttp.AttemptStateIndeterminate
	if conversation, _ = service.Get(t.Context(), "prj_test"); conversation.Status != StatusRunning || len(stub.puts) != 1 {
		t.Fatalf("a restarted worker = %+v puts=%d", conversation, len(stub.puts))
	}
	now = now.Add(transientRetryDelays[1])
	if _, err := service.Get(t.Context(), "prj_test"); err != nil || len(stub.puts) != 2 ||
		strings.Contains(stub.puts[1].Instructions, "couldn't accept") {
		t.Fatalf("the restarted turn did not run again as it was: puts=%d err=%v", len(stub.puts), err)
	}
	stub.finish(replied("Hello."))
	if conversation, _ = service.Get(t.Context(), "prj_test"); conversation.Status != StatusIdle ||
		conversation.Messages[len(conversation.Messages)-1].Text != "Hello." {
		t.Fatalf("after the retries = %+v", conversation)
	}
}

// A failure no retry fixes tells the user at once.
func TestAFinalFailureIsNotRetried(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.Send(t.Context(), "prj_test", "Hi", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeFailed,
		Error: &workerhttp.ProtocolError{Code: workerhttp.ErrorWorkspaceUnavailable, Message: "no checkout"}})
	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil || conversation.Status != StatusIdle || len(worker.puts) != 1 || conversation.Failure == nil ||
		conversation.Failure.Detail != "workspace_unavailable: no checkout" {
		t.Fatalf("final failure = %+v puts=%d err=%v", conversation, len(worker.puts), err)
	}
}

func messagesText(conversation Conversation) string {
	var builder strings.Builder
	for _, message := range conversation.Messages {
		builder.WriteString(message.Text + "\n")
	}
	return builder.String()
}
