package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
)

const remoteLeadTestToken = "remote-lead-test-token"

type remoteLeadWorkerStub struct {
	mu         sync.Mutex
	putCalls   int
	putRequest workerhttp.PutAttemptRequest
	initial    workerhttp.Attempt
	terminal   workerhttp.Attempt
	events     []workerhttp.Event
}

type unavailableRemoteLeadWorker struct{}

type conversationalRemoteLeadWorker struct {
	mu          sync.Mutex
	putRequests []workerhttp.PutAttemptRequest
	initial     map[workerhttp.AttemptReference]workerhttp.Attempt
	terminal    map[workerhttp.AttemptReference]workerhttp.Attempt
	events      map[workerhttp.AttemptReference][]workerhttp.Event
}

func (stub *conversationalRemoteLeadWorker) PutAttempt(
	_ context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	attempt, ok := stub.initial[identity.AttemptReference]
	if !ok {
		return workerhttp.Attempt{}, false, errors.New("unexpected attempt identity")
	}
	stub.putRequests = append(stub.putRequests, request)
	return attempt, true, nil
}

func (stub *conversationalRemoteLeadWorker) GetAttempt(
	_ context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	attempt, ok := stub.terminal[reference]
	if !ok {
		return workerhttp.Attempt{}, errors.New("unexpected attempt identity")
	}
	return attempt, nil
}

func (*conversationalRemoteLeadWorker) SendCommand(
	context.Context,
	workerhttp.MutationIdentity,
	workerhttp.CommandRequest,
) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{}, errors.New("commands are not expected")
}

func (*conversationalRemoteLeadWorker) ForceStop(
	context.Context,
	workerhttp.MutationIdentity,
	workerhttp.ForceStopRequest,
) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{}, errors.New("force stop is not expected")
}

func (stub *conversationalRemoteLeadWorker) OpenEventStream(
	_ context.Context,
	reference workerhttp.AttemptReference,
	afterSequence int64,
) (workerhttp.EventStream, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	all, ok := stub.events[reference]
	if !ok {
		return workerhttp.EventStream{}, errors.New("unexpected attempt identity")
	}
	replay := make([]workerhttp.Event, 0, len(all))
	for _, event := range all {
		if event.Sequence > afterSequence {
			replay = append(replay, event)
		}
	}
	closed := make(chan workerhttp.Event)
	close(closed)
	return workerhttp.EventStream{
		Replay: replay, ReplayThrough: int64(len(all)), Live: closed, Close: func() {},
	}, nil
}

func (unavailableRemoteLeadWorker) PutAttempt(
	context.Context,
	workerhttp.MutationIdentity,
	workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	return workerhttp.Attempt{}, false, errors.New("worker connection unavailable")
}

func (unavailableRemoteLeadWorker) GetAttempt(
	context.Context,
	workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{}, errors.New("worker state unavailable")
}

type unexpectedRemoteLeadPump struct{ called bool }

func (pump *unexpectedRemoteLeadPump) Run(
	context.Context,
	string,
) (workeringest.PumpResult, error) {
	pump.called = true
	return workeringest.PumpResult{}, errors.New("unexpected pump call")
}

func (stub *remoteLeadWorkerStub) PutAttempt(
	_ context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.putCalls++
	stub.putRequest = request
	if stub.initial.SessionID != identity.SessionID || stub.initial.AttemptID != identity.AttemptID {
		return workerhttp.Attempt{}, false, errors.New("unexpected attempt identity")
	}
	return stub.initial, stub.putCalls == 1, nil
}

func (stub *remoteLeadWorkerStub) GetAttempt(
	_ context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	if stub.terminal.AttemptReference != reference {
		return workerhttp.Attempt{}, errors.New("unexpected attempt identity")
	}
	return stub.terminal, nil
}

func (stub *remoteLeadWorkerStub) SendCommand(
	context.Context,
	workerhttp.MutationIdentity,
	workerhttp.CommandRequest,
) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{}, errors.New("commands are not expected")
}

func (stub *remoteLeadWorkerStub) ForceStop(
	context.Context,
	workerhttp.MutationIdentity,
	workerhttp.ForceStopRequest,
) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{}, errors.New("force stop is not expected")
}

func (stub *remoteLeadWorkerStub) OpenEventStream(
	_ context.Context,
	reference workerhttp.AttemptReference,
	afterSequence int64,
) (workerhttp.EventStream, error) {
	closed := make(chan workerhttp.Event)
	close(closed)
	replay := make([]workerhttp.Event, 0, len(stub.events))
	for _, event := range stub.events {
		if event.Sequence > afterSequence {
			replay = append(replay, event)
		}
	}
	return workerhttp.EventStream{
		Replay: replay, ReplayThrough: int64(len(stub.events)), Live: closed,
		Close: func() {},
	}, nil
}

func TestRemoteLeadStartsOneWorkerTurnAndWaitsForUser(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_lead"
	workerStub := newCompletedRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	handler, err := workerhttp.NewServer(workerhttp.ServerConfig{
		BearerToken: remoteLeadTestToken, Provider: workerhttp.ProviderCodex,
		Capabilities: []workerhttp.Capability{
			workerhttp.CapabilityStart, workerhttp.CapabilityEventReplay,
		},
		MaxConcurrentAttempts: 1, EventSource: workerStub,
	}, workerStub)
	if err != nil {
		t.Fatalf("create worker server: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := workerhttp.NewClient(workerhttp.ClientConfig{
		BaseURL: server.URL, BearerToken: remoteLeadTestToken,
		RequestTimeout: time.Second, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create worker client: %v", err)
	}
	ingester := workeringest.NewService(executions, workeringest.FilterFunc(
		func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
			return event, nil
		},
	))
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Worker: client,
		Pump:     workeringest.NewPump(executions, ingester, workeringest.NewHTTPAttemptSource(client)),
		Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
	})
	if err != nil {
		t.Fatalf("create real lead starter: %v", err)
	}

	run, created, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID,
		storedFeature.Title+": "+storedFeature.Description,
	)
	if err != nil {
		t.Fatalf("start real lead: %v", err)
	}
	if !created || run.Status != execution.RunStatusRunning {
		t.Fatalf("unexpected admitted run %+v created=%v", run, created)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	session, err := executions.GetSession(t.Context(), remoteLeadSessionID(runID))
	if err != nil {
		t.Fatalf("get lead session: %v", err)
	}
	if session.Status != execution.SessionStatusWaitingForUser || session.ProviderSessionID != "codex-thread-test" {
		t.Fatalf("unexpected lead session %+v", session)
	}
	events, err := executions.EventsForSession(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list lead activity: %v", err)
	}
	if len(events) != 3 || events[1].Type != worker.EventMessage || events[1].Text != "What should success look like?" {
		t.Fatalf("unexpected durable lead activity %+v", events)
	}
	for sequence, event := range events {
		if event.WorkerAttemptID == "" || event.WorkerEventSequence != int64(sequence+1) {
			t.Fatalf("event did not retain worker provenance: %+v", event)
		}
	}
	workerStub.mu.Lock()
	putCalls, putRequest := workerStub.putCalls, workerStub.putRequest
	workerStub.mu.Unlock()
	if putCalls != 1 || putRequest.Assignment.ProjectID != storedProject.ID ||
		putRequest.Assignment.FeatureID != storedFeature.ID || putRequest.Assignment.Role != workerhttp.RoleLead {
		t.Fatalf("unexpected worker launch calls=%d request=%+v", putCalls, putRequest)
	}
	if !strings.Contains(putRequest.Instructions, "do not modify files") ||
		!strings.Contains(putRequest.Instructions, storedFeature.Title) {
		t.Fatalf("unexpected lead instructions %q", putRequest.Instructions)
	}
	unchangedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	if unchangedFeature.State != feature.StateDraft {
		t.Fatalf("goal clarification advanced feature to %q", unchangedFeature.State)
	}
}

func TestRemoteLeadRecoveryReusesDurableWorkerAttempt(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_recovery"
	workerStub := newCompletedRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	handler, err := workerhttp.NewServer(workerhttp.ServerConfig{
		BearerToken: remoteLeadTestToken, Provider: workerhttp.ProviderCodex,
		Capabilities: []workerhttp.Capability{
			workerhttp.CapabilityStart, workerhttp.CapabilityEventReplay,
		},
		MaxConcurrentAttempts: 1, EventSource: workerStub,
	}, workerStub)
	if err != nil {
		t.Fatalf("create worker server: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := workerhttp.NewClient(workerhttp.ClientConfig{
		BaseURL: server.URL, BearerToken: remoteLeadTestToken,
		RequestTimeout: time.Second, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create worker client: %v", err)
	}
	ingester := workeringest.NewService(executions, workeringest.FilterFunc(
		func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
			return event, nil
		},
	))
	pump := workeringest.NewPump(executions, ingester, workeringest.NewHTTPAttemptSource(client))
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db),
		Worker: client, Pump: pump, Lifetime: t.Context(),
		AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
	})
	if err != nil {
		t.Fatalf("create real lead starter: %v", err)
	}
	run, _, err := executions.CreateRun(t.Context(), runID, storedFeature.ID)
	if err != nil {
		t.Fatalf("create interrupted run: %v", err)
	}
	sessionID := remoteLeadSessionID(runID)
	if _, _, err := executions.CreateSession(
		t.Context(), sessionID, runID, remoteLeadAgentID, worker.RoleLead,
	); err != nil {
		t.Fatalf("create interrupted session: %v", err)
	}
	if _, err := executions.TransitionSession(
		t.Context(), sessionID, execution.SessionStatusStarting,
		execution.SessionStatusRunning, "codex-thread-test",
	); err != nil {
		t.Fatalf("record provider session before interruption: %v", err)
	}
	attemptID := sessionID + ":turn:1"
	if _, _, err := executions.CreateWorkerAttempt(t.Context(), sessionID, attemptID); err != nil {
		t.Fatalf("create interrupted worker checkpoint: %v", err)
	}

	if err := starter.Recover(
		t.Context(), run, storedFeature, project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover real lead: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	workerStub.mu.Lock()
	putCalls := workerStub.putCalls
	workerStub.mu.Unlock()
	if putCalls != 0 {
		t.Fatalf("recovery launched the worker %d times, want 0", putCalls)
	}
	checkpoint, err := executions.GetWorkerAttempt(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("get recovered worker checkpoint: %v", err)
	}
	if checkpoint.AttemptID != attemptID || checkpoint.LastEventSequence != 3 {
		t.Fatalf("recovery replaced or failed to advance checkpoint %+v", checkpoint)
	}
}

func TestRemoteLeadRequiresReviewWhenWorkerStateCannotBeConfirmed(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	pump := &unexpectedRemoteLeadPump{}
	reported := make(chan error, 1)
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db),
		Worker: unavailableRemoteLeadWorker{}, Pump: pump,
		Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
		ReportError: func(err error) { reported <- err },
	})
	if err != nil {
		t.Fatalf("create real lead starter: %v", err)
	}
	runID := "run_remote_unavailable"
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
	); err != nil {
		t.Fatalf("admit unavailable worker run: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	if pump.called {
		t.Fatal("event pump ran without a confirmed worker attempt")
	}
	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "worker connection unavailable") {
			t.Fatalf("unexpected reported diagnostic %v", err)
		}
	default:
		t.Fatal("worker failure was not reported for operators")
	}
	session, err := executions.GetSession(t.Context(), remoteLeadSessionID(runID))
	if err != nil {
		t.Fatalf("get uncertain lead session: %v", err)
	}
	if session.Status != execution.SessionStatusStarting || session.ProviderSessionID != "" {
		t.Fatalf("uncertain launch claimed a provider was running: %+v", session)
	}
	events, err := executions.EventsForSession(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("list uncertainty activity: %v", err)
	}
	if len(events) != 1 || events[0].Type != worker.EventRecoveryAssessment ||
		events[0].Text == "" {
		t.Fatalf("uncertain worker state was not visible: %+v", events)
	}
}

func TestRemoteLeadResumesSameConversationForRepeatedUserReplies(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_replies"
	stub := newConversationalRemoteLeadWorker(
		runID, storedProject.ID, storedFeature.ID, "reply-one", "reply-two",
	)
	handler, err := workerhttp.NewServer(workerhttp.ServerConfig{
		BearerToken: remoteLeadTestToken, Provider: workerhttp.ProviderCodex,
		Capabilities: []workerhttp.Capability{
			workerhttp.CapabilityStart, workerhttp.CapabilityResume,
			workerhttp.CapabilityEventReplay,
		},
		MaxConcurrentAttempts: 1, EventSource: stub,
	}, stub)
	if err != nil {
		t.Fatalf("create worker server: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := workerhttp.NewClient(workerhttp.ClientConfig{
		BaseURL: server.URL, BearerToken: remoteLeadTestToken,
		RequestTimeout: time.Second, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create worker client: %v", err)
	}
	ingester := workeringest.NewService(executions, workeringest.FilterFunc(
		func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
			return event, nil
		},
	))
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Worker: client,
		Pump:     workeringest.NewPump(executions, ingester, workeringest.NewHTTPAttemptSource(client)),
		Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
	})
	if err != nil {
		t.Fatalf("create real lead starter: %v", err)
	}
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
	); err != nil {
		t.Fatalf("start lead conversation: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	sessionID := remoteLeadSessionID(runID)

	firstReply := worker.Command{ID: "reply-one", Type: worker.CommandMessage, Message: "CSV export is enough."}
	command, err := starter.SendCommand(t.Context(), sessionID, firstReply)
	if err != nil {
		t.Fatalf("send first reply: %v", err)
	}
	if command.Status != execution.CommandStatusPending {
		t.Fatalf("new reply status = %q, want pending", command.Status)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	applied, err := executions.GetCommand(t.Context(), firstReply.ID)
	if err != nil || applied.Status != execution.CommandStatusApplied {
		t.Fatalf("first reply was not applied: %+v err=%v", applied, err)
	}
	if retried, err := starter.SendCommand(t.Context(), sessionID, firstReply); err != nil ||
		retried.Status != execution.CommandStatusApplied {
		t.Fatalf("idempotent reply retry failed: %+v err=%v", retried, err)
	}

	secondReply := worker.Command{ID: "reply-two", Type: worker.CommandMessage, Message: "Only administrators need access."}
	if _, err := starter.SendCommand(t.Context(), sessionID, secondReply); err != nil {
		t.Fatalf("send second reply: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	session, err := executions.GetSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("get continued lead session: %v", err)
	}
	if session.ProviderSessionID != "codex-thread-test" || session.Status != execution.SessionStatusWaitingForUser {
		t.Fatalf("reply did not preserve the provider conversation: %+v", session)
	}
	events, err := executions.EventsForSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("list continued conversation: %v", err)
	}
	if len(events) != 8 || events[2].Type != worker.EventUserMessage ||
		events[2].Text != firstReply.Message || events[5].Type != worker.EventUserMessage ||
		events[5].Text != secondReply.Message {
		t.Fatalf("unexpected visible multi-turn conversation %+v", events)
	}
	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != 3 || requests[0].Mode != workerhttp.AttemptModeStart {
		t.Fatalf("unexpected worker launches %+v", requests)
	}
	for index, reply := range []worker.Command{firstReply, secondReply} {
		request := requests[index+1]
		if request.Mode != workerhttp.AttemptModeResume ||
			request.ProviderSessionID != "codex-thread-test" ||
			!strings.Contains(request.Instructions, reply.Message) {
			t.Fatalf("reply %d did not resume the original thread: %+v", index+1, request)
		}
	}
}

func TestRemoteLeadRecoveryReattachesToCommittedReplyAttempt(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_reply_recovery"
	stub := newConversationalRemoteLeadWorker(
		runID, storedProject.ID, storedFeature.ID, "reply-before-restart",
	)
	handler, err := workerhttp.NewServer(workerhttp.ServerConfig{
		BearerToken: remoteLeadTestToken, Provider: workerhttp.ProviderCodex,
		Capabilities: []workerhttp.Capability{
			workerhttp.CapabilityStart, workerhttp.CapabilityResume,
			workerhttp.CapabilityEventReplay,
		},
		MaxConcurrentAttempts: 1, EventSource: stub,
	}, stub)
	if err != nil {
		t.Fatalf("create worker server: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := workerhttp.NewClient(workerhttp.ClientConfig{
		BaseURL: server.URL, BearerToken: remoteLeadTestToken,
		RequestTimeout: time.Second, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("create worker client: %v", err)
	}
	newStarter := func() *RemoteLeadStarter {
		ingester := workeringest.NewService(executions, workeringest.FilterFunc(
			func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
				return event, nil
			},
		))
		starter, createErr := NewRemoteLeadStarter(RemoteLeadConfig{
			Executions: executions, Features: database.NewFeatureStore(db), Worker: client,
			Pump:     workeringest.NewPump(executions, ingester, workeringest.NewHTTPAttemptSource(client)),
			Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
		})
		if createErr != nil {
			t.Fatalf("create real lead starter: %v", createErr)
		}
		return starter
	}
	starter := newStarter()
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
	); err != nil {
		t.Fatalf("start lead conversation: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	sessionID := remoteLeadSessionID(runID)
	_, err = executions.GetSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("load waiting lead session: %v", err)
	}
	checkpoint, err := executions.GetWorkerAttempt(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("load first turn checkpoint: %v", err)
	}
	reply := worker.Command{
		ID: "reply-before-restart", Type: worker.CommandMessage,
		Message: "The export should include all visible columns.",
	}
	if _, admitted, err := executions.BeginWorkerTurn(
		t.Context(), reply, sessionID, checkpoint, replyAttemptID(sessionID, reply.ID),
		"The lead agent is responding to the user's message.",
	); err != nil || !admitted {
		t.Fatalf("commit reply before simulated restart: admitted=%t err=%v", admitted, err)
	}
	interruptedRun, err := executions.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("load interrupted run: %v", err)
	}
	if err := newStarter().Recover(
		t.Context(), interruptedRun, storedFeature, project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover committed reply: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	applied, err := executions.GetCommand(t.Context(), reply.ID)
	if err != nil || applied.Status != execution.CommandStatusApplied {
		t.Fatalf("recovered reply was not marked applied: %+v err=%v", applied, err)
	}
	stub.mu.Lock()
	putCount := len(stub.putRequests)
	stub.mu.Unlock()
	if putCount != 1 {
		t.Fatalf("recovery issued a replacement worker request: put count=%d, want only initial start", putCount)
	}
	events, err := executions.EventsForSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("list recovered conversation: %v", err)
	}
	foundAssessment := false
	for _, event := range events {
		if event.Type == worker.EventRecoveryAssessment &&
			strings.Contains(event.Text, "reattached") {
			foundAssessment = true
		}
	}
	if !foundAssessment {
		t.Fatalf("recovery decision was not visible in session activity: %+v", events)
	}
}

func newRemoteLeadExecution(t *testing.T) (*sql.DB, *execution.Service, project.Project, feature.Feature) {
	t.Helper()
	db, err := database.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	projectService := project.NewService(database.NewProjectStore(db))
	storedProject, err := projectService.Create(t.Context(), "Remote lead test", project.RecoveryPolicyApprovalRequired)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	featureService := feature.NewService(database.NewFeatureStore(db), projectService)
	storedFeature, err := featureService.Create(t.Context(), storedProject.ID, "Clarify export", "Choose the supported formats")
	if err != nil {
		t.Fatalf("create feature: %v", err)
	}
	executions := execution.NewService(database.NewExecutionStore(db))
	return db, executions, storedProject, storedFeature
}

func newCompletedRemoteLeadWorker(runID, projectID, featureID string) *remoteLeadWorkerStub {
	now := time.Date(2026, time.September, 9, 17, 0, 0, 0, time.UTC)
	reference := workerhttp.AttemptReference{
		SessionID: remoteLeadSessionID(runID), AttemptID: remoteLeadSessionID(runID) + ":turn:1",
	}
	assignment := workerhttp.Assignment{
		AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
		Role: workerhttp.RoleLead, WorkspaceID: "project-read-only",
	}
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeStart, Assignment: assignment,
		ProviderSessionID: "codex-thread-test", State: workerhttp.AttemptStateRunning,
		StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 3
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired,
		Summary: "Asked one clarification question.",
	}
	texts := []string{"Inspecting the draft goal", "What should success look like?", terminal.Result.Summary}
	types := []workerhttp.EventType{workerhttp.EventActivity, workerhttp.EventMessage, workerhttp.EventAttemptTerminal}
	events := make([]workerhttp.Event, len(texts))
	for index := range texts {
		events[index] = workerhttp.Event{
			AttemptReference: reference, Sequence: int64(index + 1), Type: types[index],
			Text: texts[index], OccurredAt: now.Add(time.Duration(index) * time.Millisecond),
			Redaction: workerhttp.RedactionMetadata{},
		}
	}
	return &remoteLeadWorkerStub{initial: initial, terminal: terminal, events: events}
}

func newConversationalRemoteLeadWorker(
	runID string,
	projectID string,
	featureID string,
	commandIDs ...string,
) *conversationalRemoteLeadWorker {
	stub := &conversationalRemoteLeadWorker{
		initial:  make(map[workerhttp.AttemptReference]workerhttp.Attempt),
		terminal: make(map[workerhttp.AttemptReference]workerhttp.Attempt),
		events:   make(map[workerhttp.AttemptReference][]workerhttp.Event),
	}
	sessionID := remoteLeadSessionID(runID)
	attemptIDs := []string{sessionID + ":turn:1"}
	for _, commandID := range commandIDs {
		attemptIDs = append(attemptIDs, replyAttemptID(sessionID, commandID))
	}
	now := time.Date(2026, time.September, 9, 18, 0, 0, 0, time.UTC)
	for index, attemptID := range attemptIDs {
		reference := workerhttp.AttemptReference{SessionID: sessionID, AttemptID: attemptID}
		mode := workerhttp.AttemptModeStart
		if index > 0 {
			mode = workerhttp.AttemptModeResume
		}
		initial := workerhttp.Attempt{
			AttemptReference: reference, Mode: mode,
			Assignment: workerhttp.Assignment{
				AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
				Role: workerhttp.RoleLead, WorkspaceID: "project-read-only",
			},
			ProviderSessionID: "codex-thread-test", State: workerhttp.AttemptStateRunning,
			StartedAt: now.Add(time.Duration(index) * time.Minute),
			UpdatedAt: now.Add(time.Duration(index) * time.Minute),
		}
		endedAt := initial.StartedAt.Add(time.Second)
		terminal := initial
		terminal.State = workerhttp.AttemptStateTerminal
		terminal.LatestEventSequence = 2
		terminal.UpdatedAt = endedAt
		terminal.EndedAt = &endedAt
		terminal.Result = &workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired,
			Summary: "Asked the next clarification question.",
		}
		stub.initial[reference] = initial
		stub.terminal[reference] = terminal
		stub.events[reference] = []workerhttp.Event{
			{
				AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage,
				Text: "Clarification response for turn", OccurredAt: initial.StartedAt,
			},
			{
				AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal,
				Text: terminal.Result.Summary, OccurredAt: endedAt,
			},
		}
	}
	return stub
}

func waitForRemoteLeadStatus(
	t *testing.T,
	executions *execution.Service,
	runID string,
	want execution.RunStatus,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		run, err := executions.GetRun(t.Context(), runID)
		if err == nil && run.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not reach %q: run=%+v err=%v", want, run, err)
		}
		time.Sleep(time.Millisecond)
	}
}
