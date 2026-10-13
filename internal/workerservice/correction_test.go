package workerservice

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A rejected structured result goes back to the same provider conversation,
// inside the same attempt, until a corrected result arrives.
func TestRejectedStructuredResultIsResubmittedToTheSameConversation(t *testing.T) {
	provider := &correctingAdapter{rejections: 1}
	harness := newHTTPHarness(t, filepath.Join(t.TempDir(), "worker.db"), provider, newStepClock())
	defer harness.close(t)
	request := validPutRequest()
	request.OutputContract = workerhttp.OutputContractImplementationLead
	request.Assignment.Role = workerhttp.RoleLead
	identity := validLaunchIdentity("ses_correct", "att_correct", "launch_correct")

	attempt, created, err := harness.client.PutAttempt(t.Context(), identity, request)
	if err != nil || !created {
		t.Fatalf("start provider turn: attempt=%+v created=%t error=%v", attempt, created, err)
	}
	terminal := waitForAttemptState(t, harness.client, attempt.AttemptReference, workerhttp.AttemptStateTerminal)
	if terminal.Result == nil || terminal.Result.Outcome != workerhttp.OutcomeCompleted ||
		terminal.Result.Summary != "corrected" {
		t.Fatalf("corrected result = %+v", terminal.Result)
	}
	resumes := provider.resumeRequests()
	if len(resumes) != 1 || resumes[0].ProviderSessionID != "thread_correct" ||
		resumes[0].OutputContract != worker.OutputContractImplementationLead ||
		!strings.Contains(resumes[0].Recovery.Briefing, "system_packages must be empty") ||
		strings.Contains(resumes[0].Recovery.Briefing, "Codex protocol error") {
		t.Fatalf("correction resumes = %+v", resumes)
	}
	events, err := harness.journal.ListEventsAfter(t.Context(), attempt.AttemptReference, 0)
	if err != nil || len(events) == 0 || events[0].Type != workerhttp.EventActivity ||
		!strings.Contains(events[0].Text, "asked the agent to correct it") {
		t.Fatalf("correction events = %+v err=%v", events, err)
	}
}

// After two corrections the turn ends as an incomplete result, as before.
func TestRejectedStructuredResultFailsAfterTwoCorrections(t *testing.T) {
	provider := &correctingAdapter{rejections: 3}
	harness := newHTTPHarness(t, filepath.Join(t.TempDir(), "worker.db"), provider, newStepClock())
	defer harness.close(t)
	request := validPutRequest()
	request.OutputContract = workerhttp.OutputContractImplementationLead
	request.Assignment.Role = workerhttp.RoleLead
	identity := validLaunchIdentity("ses_correct_fail", "att_correct_fail", "launch_correct_fail")

	attempt, _, err := harness.client.PutAttempt(t.Context(), identity, request)
	if err != nil {
		t.Fatalf("start provider turn: %v", err)
	}
	terminal := waitForAttemptState(t, harness.client, attempt.AttemptReference, workerhttp.AttemptStateTerminal)
	if terminal.Result == nil || terminal.Result.Error == nil ||
		terminal.Result.Error.Code != workerhttp.ErrorIncompleteResult {
		t.Fatalf("result after exhausted corrections = %+v", terminal.Result)
	}
	if got := len(provider.resumeRequests()); got != maxResultCorrections {
		t.Fatalf("corrections = %d, want %d", got, maxResultCorrections)
	}
}

// correctingAdapter returns rejections invalid structured results before it
// completes.
type correctingAdapter struct {
	mu         sync.Mutex
	rejections int
	resumes    []worker.ResumeRequest
}

func (adapter *correctingAdapter) Start(context.Context, worker.SessionRequest) (worker.Session, error) {
	return adapter.next(), nil
}

func (adapter *correctingAdapter) Resume(_ context.Context, request worker.ResumeRequest) (worker.Session, error) {
	adapter.mu.Lock()
	adapter.resumes = append(adapter.resumes, request)
	adapter.mu.Unlock()
	return adapter.next(), nil
}

func (adapter *correctingAdapter) next() worker.Session {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.rejections > 0 {
		adapter.rejections--
		return newImmediateResultSession(worker.Result{ProviderSessionID: "thread_correct"}, fmt.Errorf(
			"Codex protocol error: %w: implementation lead system_packages must be empty when publishing",
			worker.ErrInvalidStructuredOutput,
		))
	}
	return newImmediateResultSession(worker.Result{
		Outcome: worker.OutcomeCompleted, Disposition: worker.DispositionSucceeded,
		ProviderSessionID: "thread_correct", Summary: "corrected",
	}, nil)
}

func (adapter *correctingAdapter) resumeRequests() []worker.ResumeRequest {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return append([]worker.ResumeRequest(nil), adapter.resumes...)
}
