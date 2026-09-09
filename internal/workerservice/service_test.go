package workerservice

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workerjournal"
)

func TestJournalBackedServiceRunsControlsAndReplaysThroughHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.db")
	clock := newStepClock()
	script := worker.Script{
		Events: []worker.Event{
			{Type: worker.EventActivity, Text: "inspected durable state"},
			{Type: worker.EventMessage, Text: "implementation complete"},
		},
		Disposition: worker.DispositionSucceeded,
		Summary:     "completed deterministic provider work",
	}
	provider := worker.NewScriptedAdapter("codex", map[worker.Role]worker.Script{
		worker.RoleCoder: script,
	})
	first := newHTTPHarness(t, path, provider, clock)
	identity := validLaunchIdentity("ses_one", "att_one", "launch_one")
	request := validPutRequest()

	attempt, created, err := first.client.PutAttempt(t.Context(), identity, request)
	if err != nil || !created {
		t.Fatalf("start attempt: created=%t error=%v", created, err)
	}
	if attempt.State != workerhttp.AttemptStateRunning || attempt.ProviderSessionID == "" {
		t.Fatalf("started attempt = %+v", attempt)
	}
	retried, created, err := first.client.PutAttempt(t.Context(), identity, request)
	if err != nil || created || !reflect.DeepEqual(retried, attempt) {
		t.Fatalf("retry launch: attempt=%+v created=%t error=%v", retried, created, err)
	}

	streamContext, cancelStream := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelStream()
	reader, err := first.client.OpenEventStream(streamContext, attempt.AttemptReference, 0)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer reader.Close()
	if err := provider.Advance(t.Context(), identity.SessionID); err != nil {
		t.Fatalf("advance first provider event: %v", err)
	}
	assertNextEvent(t, reader, 1, workerhttp.EventActivity, "inspected durable state")

	pauseIdentity := commandIdentity(attempt.AttemptReference, "pause_one")
	if _, err := first.client.SendCommand(t.Context(), pauseIdentity, workerhttp.CommandRequest{
		Type: workerhttp.CommandPause,
	}); err != nil {
		t.Fatalf("pause attempt: %v", err)
	}
	assertNextEvent(t, reader, 2, workerhttp.EventPauseAcknowledged, "paused at a safe boundary")
	waitForAttemptState(t, first.client, attempt.AttemptReference, workerhttp.AttemptStatePaused)
	retriedPause, err := first.client.SendCommand(t.Context(), pauseIdentity, workerhttp.CommandRequest{
		Type: workerhttp.CommandPause,
	})
	if err != nil || retriedPause.State != workerhttp.AttemptStatePaused {
		t.Fatalf("retry applied pause: attempt=%+v error=%v", retriedPause, err)
	}
	_, err = first.client.SendCommand(t.Context(), pauseIdentity, workerhttp.CommandRequest{
		Type: workerhttp.CommandMessage, Message: "changed request",
	})
	assertRemoteCode(t, err, workerhttp.ErrorAttemptConflict)

	continueIdentity := commandIdentity(attempt.AttemptReference, "continue_one")
	if _, err := first.client.SendCommand(t.Context(), continueIdentity, workerhttp.CommandRequest{
		Type: workerhttp.CommandContinue,
	}); err != nil {
		t.Fatalf("continue attempt: %v", err)
	}
	assertNextEvent(t, reader, 3, workerhttp.EventContinued, "session continued")
	waitForAttemptState(t, first.client, attempt.AttemptReference, workerhttp.AttemptStateRunning)

	if err := provider.Advance(t.Context(), identity.SessionID); err != nil {
		t.Fatalf("advance final provider event: %v", err)
	}
	assertNextEvent(t, reader, 4, workerhttp.EventMessage, "implementation complete")
	assertNextEvent(t, reader, 5, workerhttp.EventAttemptTerminal, script.Summary)
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal event stream error = %v, want EOF", err)
	}
	terminal := waitForAttemptState(t, first.client, attempt.AttemptReference, workerhttp.AttemptStateTerminal)
	if terminal.Result == nil || terminal.Result.Outcome != workerhttp.OutcomeCompleted ||
		terminal.Result.Disposition != workerhttp.DispositionSucceeded {
		t.Fatalf("terminal attempt = %+v", terminal)
	}
	first.close(t)

	reopenedProvider := worker.NewScriptedAdapter("codex", map[worker.Role]worker.Script{
		worker.RoleCoder: script,
	})
	reopened := newHTTPHarness(t, path, reopenedProvider, clock)
	defer reopened.close(t)
	if reopened.recovery != (workerjournal.RecoveryResult{}) {
		t.Fatalf("terminal journal startup recovery = %+v, want no changes", reopened.recovery)
	}
	stored, err := reopened.client.GetAttempt(t.Context(), attempt.AttemptReference)
	if err != nil || !reflect.DeepEqual(stored, terminal) {
		t.Fatalf("reopened attempt=%+v error=%v, want %+v", stored, err, terminal)
	}
	replayReader, err := reopened.client.OpenEventStream(t.Context(), attempt.AttemptReference, 3)
	if err != nil {
		t.Fatalf("open replay after database reopen: %v", err)
	}
	defer replayReader.Close()
	assertNextEvent(t, replayReader, 4, workerhttp.EventMessage, "implementation complete")
	assertNextEvent(t, replayReader, 5, workerhttp.EventAttemptTerminal, script.Summary)
	if _, err := replayReader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("reopened replay error = %v, want EOF", err)
	}
	retried, created, err = reopened.client.PutAttempt(t.Context(), identity, request)
	if err != nil || created || !reflect.DeepEqual(retried, terminal) {
		t.Fatalf("reopened launch retry: attempt=%+v created=%t error=%v", retried, created, err)
	}
	if err := reopenedProvider.Advance(t.Context(), identity.SessionID); !errors.Is(err, worker.ErrSessionNotFound) {
		t.Fatalf("reopened retry launched a provider session: %v", err)
	}
}

func TestJournalBackedServiceRecordsProviderReportedFailure(t *testing.T) {
	provider := immediateResultAdapter{result: worker.Result{
		Outcome:           worker.OutcomeFailed,
		ProviderSessionID: "codex_thread_failed",
		Summary:           "Codex reported that the turn failed.",
	}}
	harness := newHTTPHarness(t, filepath.Join(t.TempDir(), "worker.db"), provider, newStepClock())
	defer harness.close(t)
	identity := validLaunchIdentity("ses_failed", "att_failed", "launch_failed")

	attempt, created, err := harness.client.PutAttempt(t.Context(), identity, validPutRequest())
	if err != nil || !created {
		t.Fatalf("start provider-reported failure: attempt=%+v created=%t error=%v", attempt, created, err)
	}
	terminal := waitForAttemptState(t, harness.client, attempt.AttemptReference, workerhttp.AttemptStateTerminal)
	if terminal.Result == nil || terminal.Result.Outcome != workerhttp.OutcomeFailed ||
		terminal.Result.Disposition != "" || terminal.Result.Error == nil ||
		terminal.Result.Error.Code != workerhttp.ErrorInternal ||
		terminal.Result.Error.Retryable ||
		terminal.Result.Summary != provider.result.Summary {
		t.Fatalf("stored provider failure = %+v", terminal.Result)
	}
}

func TestJournalBackedServiceMarksInterruptedWorkIndeterminateOnStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.db")
	clock := newStepClock()
	script := worker.Script{
		Events:      []worker.Event{{Type: worker.EventActivity, Text: "work in progress"}},
		Disposition: worker.DispositionSucceeded,
		Summary:     "completed work",
	}
	provider := worker.NewScriptedAdapter("codex", map[worker.Role]worker.Script{
		worker.RoleCoder: script,
	})
	first := newHTTPHarness(t, path, provider, clock)
	identity := validLaunchIdentity("ses_interrupted", "att_interrupted", "launch_interrupted")
	request := validPutRequest()
	attempt, created, err := first.client.PutAttempt(t.Context(), identity, request)
	if err != nil || !created || attempt.State != workerhttp.AttemptStateRunning {
		t.Fatalf("start interrupted attempt: attempt=%+v created=%t error=%v", attempt, created, err)
	}
	pauseRequest := workerhttp.CommandRequest{Type: workerhttp.CommandPause}
	digest, err := workerjournal.DigestRequest(pauseRequest)
	if err != nil {
		t.Fatalf("digest pending command: %v", err)
	}
	pendingAt := clock.Now()
	pendingIdentity := commandIdentity(attempt.AttemptReference, "pause_uncertain")
	if _, claimed, err := first.journal.ClaimMutation(t.Context(), workerjournal.Mutation{
		MutationIdentity: pendingIdentity,
		Kind:             workerjournal.MutationPause,
		RequestDigest:    digest,
		Status:           workerjournal.MutationPending,
		RequestedAt:      pendingAt,
		UpdatedAt:        pendingAt,
	}); err != nil || !claimed {
		t.Fatalf("record pending command: claimed=%t error=%v", claimed, err)
	}
	first.cancel()
	waitForNoActiveSession(t, first.service, attempt.AttemptReference)
	first.server.Close()
	if err := first.db.Close(); err != nil {
		t.Fatalf("close interrupted journal: %v", err)
	}

	reopenedProvider := worker.NewScriptedAdapter("codex", map[worker.Role]worker.Script{
		worker.RoleCoder: script,
	})
	reopened := newHTTPHarness(t, path, reopenedProvider, clock)
	defer reopened.close(t)
	if reopened.recovery.AttemptsMarked != 1 || reopened.recovery.MutationsMarked != 1 {
		t.Fatalf("startup recovery = %+v", reopened.recovery)
	}
	stored, err := reopened.client.GetAttempt(t.Context(), attempt.AttemptReference)
	if err != nil || stored.State != workerhttp.AttemptStateIndeterminate {
		t.Fatalf("recovered attempt=%+v error=%v", stored, err)
	}
	retried, created, err := reopened.client.PutAttempt(t.Context(), identity, request)
	if err != nil || created || retried.State != workerhttp.AttemptStateIndeterminate {
		t.Fatalf("retry interrupted launch: attempt=%+v created=%t error=%v", retried, created, err)
	}
	if err := reopenedProvider.Advance(t.Context(), identity.SessionID); !errors.Is(err, worker.ErrSessionNotFound) {
		t.Fatalf("interrupted launch retry started a second provider: %v", err)
	}

	_, err = reopened.client.SendCommand(t.Context(), pendingIdentity, pauseRequest)
	assertRemoteCode(t, err, workerhttp.ErrorIndeterminateState)
	replacement := validLaunchIdentity(identity.SessionID, "att_replacement", "launch_replacement")
	_, _, err = reopened.client.PutAttempt(t.Context(), replacement, request)
	assertRemoteCode(t, err, workerhttp.ErrorAttemptActive)

	replay, err := reopened.client.OpenEventStream(t.Context(), attempt.AttemptReference, 0)
	if err != nil {
		t.Fatalf("open indeterminate event stream: %v", err)
	}
	defer replay.Close()
	if _, err := replay.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("indeterminate stream error = %v, want EOF", err)
	}
	_ = provider.Advance(t.Context(), identity.SessionID)
}

func TestJournalBackedServiceResumesAtRecoveryBoundaryAndStopsCooperatively(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.db")
	clock := newStepClock()
	provider := worker.NewScriptedAdapter("codex", map[worker.Role]worker.Script{
		worker.RoleCoder: {
			Events:      []worker.Event{{Type: worker.EventActivity, Text: "resumed work"}},
			Disposition: worker.DispositionSucceeded,
			Summary:     "resumed session stopped",
		},
	})
	harness := newHTTPHarness(t, path, provider, clock)
	defer harness.close(t)
	identity := validLaunchIdentity("ses_resume", "att_resume", "launch_resume")
	providerSessionID := "codex:coder:0:ses_resume"
	request := validPutRequest()
	request.Mode = workerhttp.AttemptModeResume
	request.ProviderSessionID = providerSessionID
	request.Instructions = "Reconcile durable state before continuing."

	attempt, created, err := harness.client.PutAttempt(t.Context(), identity, request)
	if err != nil || !created {
		t.Fatalf("resume attempt: attempt=%+v created=%t error=%v", attempt, created, err)
	}
	if attempt.ProviderSessionID != providerSessionID {
		t.Fatalf("resumed provider session ID = %q, want %q", attempt.ProviderSessionID, providerSessionID)
	}
	waitForAttemptState(t, harness.client, attempt.AttemptReference, workerhttp.AttemptStatePaused)
	replay, err := harness.client.OpenEventStream(t.Context(), attempt.AttemptReference, 0)
	if err != nil {
		t.Fatalf("open resumed event replay: %v", err)
	}
	defer replay.Close()
	assertNextEvent(
		t,
		replay,
		1,
		workerhttp.EventRecoveryAssessment,
		"Recovery assessment: durable activity matches the restored simulated conversation and workflow phase. Repository, worktree, Git HEAD/status/diff, interrupted tests, and Forgejo PR state are not applicable to this simulated worker.",
	)

	if _, err := harness.client.SendCommand(
		t.Context(),
		commandIdentity(attempt.AttemptReference, "continue_resume"),
		workerhttp.CommandRequest{Type: workerhttp.CommandContinue},
	); err != nil {
		t.Fatalf("continue resumed attempt: %v", err)
	}
	assertNextEvent(t, replay, 2, workerhttp.EventContinued, "session continued")
	waitForAttemptState(t, harness.client, attempt.AttemptReference, workerhttp.AttemptStateRunning)

	if _, err := harness.client.SendCommand(
		t.Context(),
		commandIdentity(attempt.AttemptReference, "stop_resume"),
		workerhttp.CommandRequest{Type: workerhttp.CommandStop},
	); err != nil {
		t.Fatalf("stop resumed attempt: %v", err)
	}
	assertNextEvent(t, replay, 3, workerhttp.EventAttemptTerminal, "resumed session stopped")
	terminal := waitForAttemptState(t, harness.client, attempt.AttemptReference, workerhttp.AttemptStateTerminal)
	if terminal.Result == nil || terminal.Result.Outcome != workerhttp.OutcomeStopped ||
		terminal.Result.Disposition != "" {
		t.Fatalf("stopped resumed attempt = %+v", terminal)
	}
}

func TestJournalBackedServiceFailsClosedWhenNormalizationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.db")
	clock := newStepClock()
	provider := worker.NewScriptedAdapter("codex", map[worker.Role]worker.Script{
		worker.RoleCoder: {
			Events:      []worker.Event{{Type: worker.EventActivity, Text: "possibly sensitive"}},
			Disposition: worker.DispositionSucceeded,
			Summary:     "should not complete",
		},
	})
	db, journal := openJournal(t, path)
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, _, err := New(t.Context(), Config{
		Journal: journal, Provider: provider, Lifetime: lifetime, Now: clock.Now,
		Normalizer: NormalizerFunc(func(context.Context, worker.Event) (NormalizedEvent, error) {
			return NormalizedEvent{}, errors.New("redaction unavailable")
		}),
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	identity := validLaunchIdentity("ses_redaction", "att_redaction", "launch_redaction")
	attempt, _, err := service.PutAttempt(t.Context(), identity, validPutRequest())
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	if err := provider.Advance(t.Context(), identity.SessionID); err != nil {
		t.Fatalf("advance unsafe event: %v", err)
	}
	indeterminate := waitForStoredState(t, service, attempt.AttemptReference, workerhttp.AttemptStateIndeterminate)
	events, err := journal.ListEventsAfter(t.Context(), attempt.AttemptReference, 0)
	if err != nil {
		t.Fatalf("list redaction failure events: %v", err)
	}
	if len(events) != 1 || events[0].Type != workerhttp.EventRedactionFailure ||
		strings.Contains(events[0].Text, "sensitive") {
		t.Fatalf("unsafe normalization event leaked: %+v", events)
	}
	if indeterminate.LatestEventSequence != 1 {
		t.Fatalf("indeterminate attempt cursor = %d, want 1", indeterminate.LatestEventSequence)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close journal: %v", err)
	}
}

func TestJournalBackedServiceFencesProviderStartedWithoutIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.db")
	clock := newStepClock()
	inner := worker.NewScriptedAdapter("codex", map[worker.Role]worker.Script{
		worker.RoleCoder: {
			Events:      []worker.Event{{Type: worker.EventActivity, Text: "started without identity"}},
			Disposition: worker.DispositionSucceeded,
			Summary:     "unsafe launch",
		},
	})
	provider := &identityOverrideAdapter{inner: inner}
	db, journal := openJournal(t, path)
	defer db.Close()
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, _, err := New(t.Context(), Config{
		Journal: journal, Provider: provider, Normalizer: safeScriptNormalizer(),
		Lifetime: lifetime, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	identity := validLaunchIdentity("ses_missing_identity", "att_one", "launch_one")
	attempt, created, err := service.PutAttempt(t.Context(), identity, validPutRequest())
	if err != nil || !created || attempt.State != workerhttp.AttemptStateIndeterminate {
		t.Fatalf("unsafe provider launch: attempt=%+v created=%t error=%v", attempt, created, err)
	}
	if attempt.ProviderSessionID != "" {
		t.Fatalf("unsafe provider session ID = %q, want empty", attempt.ProviderSessionID)
	}
	replacement := validLaunchIdentity(identity.SessionID, "att_two", "launch_two")
	if _, _, err := service.PutAttempt(t.Context(), replacement, validPutRequest()); err == nil {
		t.Fatal("expected uncertain provider launch to fence a replacement")
	} else {
		var serviceError *workerhttp.ServiceError
		if !errors.As(err, &serviceError) || serviceError.ProtocolError.Code != workerhttp.ErrorAttemptActive {
			t.Fatalf("replacement error = %v, want attempt_active", err)
		}
	}
	_ = inner.Advance(t.Context(), identity.SessionID)
}

type httpHarness struct {
	db       *sql.DB
	journal  *workerjournal.Store
	service  *Service
	recovery workerjournal.RecoveryResult
	client   *workerhttp.Client
	server   *httptest.Server
	cancel   context.CancelFunc
}

func newHTTPHarness(
	t *testing.T,
	path string,
	provider worker.Adapter,
	clock *stepClock,
) *httpHarness {
	return newHTTPHarnessWithCapabilities(t, path, provider, clock, []workerhttp.Capability{
		workerhttp.CapabilityStart,
		workerhttp.CapabilityResume,
		workerhttp.CapabilityMessage,
		workerhttp.CapabilityPause,
		workerhttp.CapabilityContinue,
		workerhttp.CapabilityCooperativeStop,
		workerhttp.CapabilityEventReplay,
	})
}

func newHTTPHarnessWithCapabilities(
	t *testing.T,
	path string,
	provider worker.Adapter,
	clock *stepClock,
	capabilities []workerhttp.Capability,
) *httpHarness {
	t.Helper()
	db, journal := openJournal(t, path)
	lifetime, cancel := context.WithCancel(context.Background())
	service, recovery, err := New(t.Context(), Config{
		Journal: journal, Provider: provider, Normalizer: safeScriptNormalizer(),
		Lifetime: lifetime, Now: clock.Now,
	})
	if err != nil {
		cancel()
		_ = db.Close()
		t.Fatalf("create journal-backed service: %v", err)
	}
	handler, err := workerhttp.NewServer(workerhttp.ServerConfig{
		BearerToken:           "worker-test-token",
		Provider:              workerhttp.ProviderCodex,
		Capabilities:          capabilities,
		MaxConcurrentAttempts: 1,
		EventSource:           service,
		EventStreamHeartbeat:  time.Hour,
	}, service)
	if err != nil {
		cancel()
		_ = db.Close()
		t.Fatalf("create worker HTTP server: %v", err)
	}
	server := httptest.NewServer(handler)
	client, err := workerhttp.NewClient(workerhttp.ClientConfig{
		BaseURL: server.URL, BearerToken: "worker-test-token",
		RequestTimeout: 2 * time.Second, HTTPClient: server.Client(),
	})
	if err != nil {
		server.Close()
		cancel()
		_ = db.Close()
		t.Fatalf("create worker HTTP client: %v", err)
	}
	return &httpHarness{
		db: db, journal: journal, service: service, recovery: recovery,
		client: client, server: server, cancel: cancel,
	}
}

func (harness *httpHarness) close(t *testing.T) {
	t.Helper()
	harness.cancel()
	harness.server.Close()
	if err := harness.db.Close(); err != nil {
		t.Fatalf("close worker journal: %v", err)
	}
}

func openJournal(t *testing.T, path string) (*sql.DB, *workerjournal.Store) {
	t.Helper()
	db, err := workerjournal.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatalf("open worker journal: %v", err)
	}
	if err := workerjournal.Migrate(t.Context(), db); err != nil {
		_ = db.Close()
		t.Fatalf("migrate worker journal: %v", err)
	}
	return db, workerjournal.NewStore(db)
}

func safeScriptNormalizer() Normalizer {
	return NormalizerFunc(func(_ context.Context, event worker.Event) (NormalizedEvent, error) {
		normalized := NormalizedEvent{
			Type: workerhttp.EventType(event.Type), Text: event.Text,
			Redaction: workerhttp.RedactionMetadata{},
		}
		if event.RecoveryAssessment != nil {
			normalized.RecoveryAssessment = &workerhttp.RecoveryAssessment{
				Consistent:         event.RecoveryAssessment.Consistent,
				RequiresUserReview: event.RecoveryAssessment.RequiresUserReview,
			}
		}
		return normalized, nil
	})
}

func validLaunchIdentity(
	sessionID string,
	attemptID string,
	idempotencyKey string,
) workerhttp.MutationIdentity {
	return workerhttp.MutationIdentity{
		AttemptReference: workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID},
		IdempotencyKey:   idempotencyKey,
	}
}

func commandIdentity(
	reference workerhttp.AttemptReference,
	idempotencyKey string,
) workerhttp.MutationIdentity {
	return workerhttp.MutationIdentity{
		AttemptReference: reference,
		IdempotencyKey:   idempotencyKey,
	}
}

func validPutRequest() workerhttp.PutAttemptRequest {
	return workerhttp.PutAttemptRequest{
		Mode: workerhttp.AttemptModeStart,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "profile_test", ProjectID: "prj_test", FeatureID: "fea_test",
			Role: workerhttp.RoleCoder, WorkspaceID: "workspace_test",
			ConfigurationRevision: 1,
			MaterializationDigest: "sha256:" + strings.Repeat("a", 64),
		},
		Instructions: "Implement the accepted plan.",
	}
}

func assertNextEvent(
	t *testing.T,
	reader *workerhttp.EventReader,
	sequence int64,
	eventType workerhttp.EventType,
	text string,
) {
	t.Helper()
	event, err := reader.Next()
	if err != nil {
		t.Fatalf("read event %d: %v", sequence, err)
	}
	if event.Sequence != sequence || event.Type != eventType || event.Text != text {
		t.Fatalf("event = %+v, want sequence=%d type=%q text=%q", event, sequence, eventType, text)
	}
}

func waitForAttemptState(
	t *testing.T,
	client *workerhttp.Client,
	reference workerhttp.AttemptReference,
	state workerhttp.AttemptState,
) workerhttp.Attempt {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last workerhttp.Attempt
	var lastErr error
	for time.Now().Before(deadline) {
		attempt, err := client.GetAttempt(t.Context(), reference)
		last, lastErr = attempt, err
		if err == nil && attempt.State == state {
			return attempt
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf(
		"attempt %s/%s did not reach %q; last attempt=%+v error=%v",
		reference.SessionID,
		reference.AttemptID,
		state,
		last,
		lastErr,
	)
	return workerhttp.Attempt{}
}

func waitForStoredState(
	t *testing.T,
	service *Service,
	reference workerhttp.AttemptReference,
	state workerhttp.AttemptState,
) workerhttp.Attempt {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		attempt, err := service.GetAttempt(t.Context(), reference)
		if err == nil && attempt.State == state {
			return attempt
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("stored attempt %s/%s did not reach %q", reference.SessionID, reference.AttemptID, state)
	return workerhttp.Attempt{}
}

func waitForNoActiveSession(
	t *testing.T,
	service *Service,
	reference workerhttp.AttemptReference,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !service.hasActiveSession(reference) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("provider session %s/%s remained active", reference.SessionID, reference.AttemptID)
}

func assertRemoteCode(t *testing.T, err error, code workerhttp.ErrorCode) {
	t.Helper()
	var remote *workerhttp.RemoteError
	if !errors.As(err, &remote) || remote.ProtocolError.Code != code {
		t.Fatalf("remote error = %v, want code %q", err, code)
	}
}

type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

type identityOverrideAdapter struct {
	inner worker.Adapter
}

type immediateResultAdapter struct {
	result worker.Result
}

func (adapter immediateResultAdapter) Start(
	context.Context,
	worker.SessionRequest,
) (worker.Session, error) {
	return newImmediateResultSession(adapter.result), nil
}

func (adapter immediateResultAdapter) Resume(
	context.Context,
	worker.ResumeRequest,
) (worker.Session, error) {
	return newImmediateResultSession(adapter.result), nil
}

type immediateResultSession struct {
	result worker.Result
	events chan worker.Event
}

func newImmediateResultSession(result worker.Result) *immediateResultSession {
	events := make(chan worker.Event)
	close(events)
	return &immediateResultSession{result: result, events: events}
}

func (session *immediateResultSession) ProviderSessionID() string {
	return session.result.ProviderSessionID
}

func (session *immediateResultSession) Events() <-chan worker.Event {
	return session.events
}

func (*immediateResultSession) Send(context.Context, worker.Command) error {
	return nil
}

func (session *immediateResultSession) Wait(context.Context) (worker.Result, error) {
	return session.result, nil
}

func (adapter *identityOverrideAdapter) Start(
	ctx context.Context,
	request worker.SessionRequest,
) (worker.Session, error) {
	session, err := adapter.inner.Start(ctx, request)
	if err != nil {
		return nil, err
	}
	return identityOverrideSession{Session: session}, nil
}

func (adapter *identityOverrideAdapter) Resume(
	ctx context.Context,
	request worker.ResumeRequest,
) (worker.Session, error) {
	session, err := adapter.inner.Resume(ctx, request)
	if err != nil {
		return nil, err
	}
	return identityOverrideSession{Session: session}, nil
}

type identityOverrideSession struct {
	worker.Session
}

func (identityOverrideSession) ProviderSessionID() string {
	return ""
}

func newStepClock() *stepClock {
	return &stepClock{now: time.Date(2026, time.September, 9, 6, 0, 0, 0, time.UTC)}
}

func (clock *stepClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(time.Millisecond)
	return clock.now
}
