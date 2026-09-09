//go:build darwin || linux

package workerservice

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/processsupervisor"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workerjournal"
)

func TestForceStopKillsOnlyTheAuthenticatedAttemptAndReplaysItsResult(t *testing.T) {
	provider := newProcessTestAdapter(t)
	harness := newHTTPHarnessWithCapabilities(
		t,
		filepath.Join(t.TempDir(), "worker.db"),
		provider,
		newStepClock(),
		[]workerhttp.Capability{
			workerhttp.CapabilityStart,
			workerhttp.CapabilityForceStop,
			workerhttp.CapabilityEventReplay,
		},
	)
	defer harness.close(t)

	firstIdentity := validLaunchIdentity("ses_force", "att_first", "launch_first")
	first, created, err := harness.client.PutAttempt(t.Context(), firstIdentity, validPutRequest())
	if err != nil || !created || first.State != workerhttp.AttemptStateRunning {
		t.Fatalf("start first process attempt: attempt=%+v created=%t error=%v", first, created, err)
	}
	provider.waitUntilReady(t, firstIdentity.AttemptID)
	if got := provider.request(firstIdentity.AttemptID).AttemptID; got != firstIdentity.AttemptID {
		t.Fatalf("provider attempt ID = %q, want %q", got, firstIdentity.AttemptID)
	}

	forceIdentity := commandIdentity(first.AttemptReference, "force_first")
	forceRequest := workerhttp.ForceStopRequest{Reason: "cooperative stop deadline expired"}
	terminal, err := harness.client.ForceStop(t.Context(), forceIdentity, forceRequest)
	if err != nil {
		t.Fatalf("force-stop first process attempt: %v", err)
	}
	assertStoppedAttempt(t, terminal)
	if calls := provider.forceStopCalls.Load(); calls != 1 {
		t.Fatalf("provider force-stop calls = %d, want 1", calls)
	}
	storedMutation, err := harness.journal.GetMutation(t.Context(), forceIdentity)
	if err != nil || storedMutation.Kind != workerjournal.MutationForceStop ||
		storedMutation.Status != workerjournal.MutationApplied {
		t.Fatalf("stored force-stop mutation = %+v, error=%v", storedMutation, err)
	}
	events, err := harness.journal.ListEventsAfter(t.Context(), first.AttemptReference, 0)
	if err != nil || len(events) != 1 || events[0].Type != workerhttp.EventAttemptTerminal {
		t.Fatalf("stored terminal events = %+v, error=%v", events, err)
	}

	replayed, err := harness.client.ForceStop(t.Context(), forceIdentity, forceRequest)
	if err != nil || replayed.State != workerhttp.AttemptStateTerminal {
		t.Fatalf("replay force-stop result: attempt=%+v error=%v", replayed, err)
	}
	if calls := provider.forceStopCalls.Load(); calls != 1 {
		t.Fatalf("replayed request sent another force-stop: calls=%d", calls)
	}
	_, err = harness.client.ForceStop(t.Context(), forceIdentity, workerhttp.ForceStopRequest{
		Reason: "different reason with the same idempotency key",
	})
	assertRemoteCode(t, err, workerhttp.ErrorAttemptConflict)

	secondIdentity := validLaunchIdentity(firstIdentity.SessionID, "att_second", "launch_second")
	second, created, err := harness.client.PutAttempt(t.Context(), secondIdentity, validPutRequest())
	if err != nil || !created || second.State != workerhttp.AttemptStateRunning {
		t.Fatalf("start replacement process attempt: attempt=%+v created=%t error=%v", second, created, err)
	}
	provider.waitUntilReady(t, secondIdentity.AttemptID)
	_, err = harness.client.ForceStop(
		t.Context(),
		commandIdentity(first.AttemptReference, "stale_force"),
		workerhttp.ForceStopRequest{Reason: "stale request must not reach replacement"},
	)
	assertRemoteCode(t, err, workerhttp.ErrorAttemptConflict)
	if calls := provider.forceStopCalls.Load(); calls != 1 {
		t.Fatalf("stale request reached current process: force-stop calls=%d", calls)
	}
	current, err := harness.client.GetAttempt(t.Context(), second.AttemptReference)
	if err != nil || current.State != workerhttp.AttemptStateRunning {
		t.Fatalf("replacement after stale force-stop = %+v, error=%v", current, err)
	}

	secondTerminal, err := harness.client.ForceStop(
		t.Context(),
		commandIdentity(second.AttemptReference, "force_second"),
		workerhttp.ForceStopRequest{Reason: "test cleanup through the authenticated handle"},
	)
	if err != nil {
		t.Fatalf("force-stop replacement process attempt: %v", err)
	}
	assertStoppedAttempt(t, secondTerminal)
	if calls := provider.forceStopCalls.Load(); calls != 2 {
		t.Fatalf("provider force-stop calls after replacement = %d, want 2", calls)
	}
}

func TestForceStopDeliveryFailureBecomesDurablyIndeterminate(t *testing.T) {
	provider := newProcessTestAdapter(t)
	provider.forceStopErr = errors.New("test cannot prove whether the signal was delivered")
	harness := newHTTPHarnessWithCapabilities(
		t,
		filepath.Join(t.TempDir(), "worker.db"),
		provider,
		newStepClock(),
		[]workerhttp.Capability{
			workerhttp.CapabilityStart,
			workerhttp.CapabilityForceStop,
			workerhttp.CapabilityEventReplay,
		},
	)
	defer harness.close(t)

	launchIdentity := validLaunchIdentity("ses_uncertain_force", "att_uncertain_force", "launch_uncertain_force")
	attempt, created, err := harness.client.PutAttempt(t.Context(), launchIdentity, validPutRequest())
	if err != nil || !created {
		t.Fatalf("start process attempt: attempt=%+v created=%t error=%v", attempt, created, err)
	}
	provider.waitUntilReady(t, launchIdentity.AttemptID)
	forceIdentity := commandIdentity(attempt.AttemptReference, "force_uncertain")
	request := workerhttp.ForceStopRequest{Reason: "test uncertain process control"}
	_, err = harness.client.ForceStop(t.Context(), forceIdentity, request)
	assertRemoteCode(t, err, workerhttp.ErrorIndeterminateState)

	stored := waitForAttemptState(
		t,
		harness.client,
		attempt.AttemptReference,
		workerhttp.AttemptStateIndeterminate,
	)
	if stored.Result != nil {
		t.Fatalf("indeterminate attempt unexpectedly has a result: %+v", stored)
	}
	mutation, err := harness.journal.GetMutation(t.Context(), forceIdentity)
	if err != nil || mutation.Status != workerjournal.MutationIndeterminate {
		t.Fatalf("uncertain force-stop mutation = %+v, error=%v", mutation, err)
	}
	_, err = harness.client.ForceStop(t.Context(), forceIdentity, request)
	assertRemoteCode(t, err, workerhttp.ErrorIndeterminateState)
	if calls := provider.forceStopCalls.Load(); calls != 1 {
		t.Fatalf("indeterminate retry redelivered force-stop: calls=%d", calls)
	}
}

func TestWorkerServiceForceStopHelper(t *testing.T) {
	if os.Getenv("COMMITARIUM_FORCE_STOP_HELPER") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	if err := os.WriteFile(os.Getenv("COMMITARIUM_FORCE_STOP_READY"), []byte("ready"), 0o600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

type processTestAdapter struct {
	supervisor     *processsupervisor.Supervisor
	executable     string
	directory      string
	forceStopErr   error
	forceStopCalls atomic.Int32

	mu       sync.Mutex
	requests map[string]worker.SessionRequest
	sessions []*processTestSession
}

func newProcessTestAdapter(t *testing.T) *processTestAdapter {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate worker service test executable: %v", err)
	}
	adapter := &processTestAdapter{
		supervisor: processsupervisor.New(), executable: executable,
		directory: t.TempDir(), requests: make(map[string]worker.SessionRequest),
	}
	t.Cleanup(func() {
		adapter.mu.Lock()
		sessions := append([]*processTestSession(nil), adapter.sessions...)
		adapter.mu.Unlock()
		for _, session := range sessions {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = session.process.ForceStop(ctx)
			cancel()
		}
	})
	return adapter
}

func (adapter *processTestAdapter) Start(
	ctx context.Context,
	request worker.SessionRequest,
) (worker.Session, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	readyPath := adapter.readyPath(request.AttemptID)
	process, err := adapter.supervisor.Start(ctx, processsupervisor.StartRequest{
		AttemptID:  request.AttemptID,
		Executable: adapter.executable,
		Arguments:  []string{"-test.run=^TestWorkerServiceForceStopHelper$"},
		Directory:  adapter.directory,
		Environment: append(os.Environ(),
			"COMMITARIUM_FORCE_STOP_HELPER=1",
			"COMMITARIUM_FORCE_STOP_READY="+readyPath,
		),
	})
	if err != nil {
		return nil, err
	}
	session := &processTestSession{
		providerSessionID: "process:" + request.AttemptID,
		process:           process,
		events:            make(chan worker.Event),
		done:              make(chan struct{}),
		forceStopCalls:    &adapter.forceStopCalls,
		forceStopErr:      adapter.forceStopErr,
	}
	adapter.mu.Lock()
	adapter.requests[request.AttemptID] = request
	adapter.sessions = append(adapter.sessions, session)
	adapter.mu.Unlock()
	go session.collect()
	return session, nil
}

func (adapter *processTestAdapter) Resume(
	context.Context,
	worker.ResumeRequest,
) (worker.Session, error) {
	return nil, errors.New("process test adapter does not resume")
}

func (adapter *processTestAdapter) request(attemptID string) worker.SessionRequest {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.requests[attemptID]
}

func (adapter *processTestAdapter) readyPath(attemptID string) string {
	return filepath.Join(adapter.directory, attemptID+".ready")
}

func (adapter *processTestAdapter) waitUntilReady(t *testing.T, attemptID string) {
	t.Helper()
	path := adapter.readyPath(attemptID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("provider process %q did not become ready", attemptID)
}

type processTestSession struct {
	providerSessionID string
	process           *processsupervisor.Process
	events            chan worker.Event
	done              chan struct{}
	forceStopCalls    *atomic.Int32
	forceStopErr      error

	mu      sync.Mutex
	result  worker.Result
	waitErr error
}

var _ worker.ForceStoppableSession = (*processTestSession)(nil)

func (session *processTestSession) ProviderSessionID() string {
	return session.providerSessionID
}

func (session *processTestSession) Events() <-chan worker.Event {
	return session.events
}

func (session *processTestSession) Send(context.Context, worker.Command) error {
	return worker.ErrInvalidSessionState
}

func (session *processTestSession) Wait(ctx context.Context) (worker.Result, error) {
	select {
	case <-ctx.Done():
		return worker.Result{}, ctx.Err()
	case <-session.done:
		session.mu.Lock()
		defer session.mu.Unlock()
		return session.result, session.waitErr
	}
}

func (session *processTestSession) ForceStop(ctx context.Context, _ string) error {
	session.forceStopCalls.Add(1)
	if session.forceStopErr != nil {
		return session.forceStopErr
	}
	_, err := session.process.ForceStop(ctx)
	return err
}

func (session *processTestSession) collect() {
	for range session.process.Output() {
	}
	result, err := session.process.Wait(context.Background())
	workerResult := worker.Result{
		Outcome:           worker.OutcomeStopped,
		ProviderSessionID: session.providerSessionID,
		Summary:           "provider process was force-stopped",
	}
	if err == nil && result.Signal == "" && result.ExitCode == 0 {
		workerResult.Outcome = worker.OutcomeCompleted
		workerResult.Disposition = worker.DispositionSucceeded
		workerResult.Summary = "provider process completed"
	}
	session.mu.Lock()
	session.result = workerResult
	session.waitErr = err
	session.mu.Unlock()
	close(session.done)
	close(session.events)
}

func assertStoppedAttempt(t *testing.T, attempt workerhttp.Attempt) {
	t.Helper()
	if attempt.State != workerhttp.AttemptStateTerminal || attempt.Result == nil ||
		attempt.Result.Outcome != workerhttp.OutcomeStopped {
		t.Fatalf("force-stopped attempt = %+v", attempt)
	}
}
