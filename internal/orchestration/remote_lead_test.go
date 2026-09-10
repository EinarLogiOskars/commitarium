package orchestration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
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

type remoteLeadWorkspaceStub struct {
	prepared                workspace.Workspace
	prepareCalls            int
	publishCalls            int
	verifyCalls             int
	continuationVerifyCalls int
	publicationVerifyCalls  int
	responseVerifyCalls     int
	reviewVerifyCalls       int
	responseReviewedCommit  string
	responseCommit          string
	publishedEventID        string
	publishedPlan           string
	publishErr              error
	verifyErr               error
	publicationVerifyErr    error
	responseVerifyErr       error
	reviewVerifyErr         error
}

func (stub *remoteLeadWorkspaceStub) VerifyImplementationPublication(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	plan string,
	_ string,
	_ string,
	_ string,
	_ int64,
	_ string,
) (workspace.Workspace, error) {
	stub.publicationVerifyCalls++
	stub.publishedEventID = eventID
	stub.publishedPlan = plan
	return stub.prepared, stub.publicationVerifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyImplementationReview(
	context.Context,
	string,
	string,
	string,
	string,
	string,
	string,
	string,
	int64,
	int64,
	string,
	string,
) (workspace.Workspace, error) {
	stub.reviewVerifyCalls++
	return stub.prepared, stub.reviewVerifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyImplementationReviewResponse(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ string,
	_ string,
	_ string,
	reviewedCommit string,
	commit string,
	_ int64,
	_ string,
) (workspace.Workspace, error) {
	stub.responseVerifyCalls++
	stub.responseReviewedCommit = reviewedCommit
	stub.responseCommit = commit
	return stub.prepared, stub.responseVerifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyPublishedPlan(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	plan string,
) (workspace.Workspace, error) {
	stub.verifyCalls++
	stub.publishedEventID = eventID
	stub.publishedPlan = plan
	return stub.prepared, stub.verifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyImplementationContinuation(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	plan string,
) (workspace.Workspace, error) {
	stub.continuationVerifyCalls++
	stub.publishedEventID = eventID
	stub.publishedPlan = plan
	return stub.prepared, stub.verifyErr
}

type conversationalRemoteLeadPump struct {
	executions *execution.Service
	worker     *conversationalRemoteLeadWorker
}

func (pump *conversationalRemoteLeadPump) Run(
	ctx context.Context,
	sessionID string,
) (workeringest.PumpResult, error) {
	checkpoint, err := pump.executions.GetWorkerAttempt(ctx, sessionID)
	if err != nil {
		return workeringest.PumpResult{}, err
	}
	reference := workerhttp.AttemptReference{
		SessionID: checkpoint.SessionID, AttemptID: checkpoint.AttemptID,
	}
	pump.worker.mu.Lock()
	events := append([]workerhttp.Event(nil), pump.worker.events[reference]...)
	attempt := pump.worker.terminal[reference]
	pump.worker.mu.Unlock()
	newEvents := 0
	for _, event := range events {
		if event.Sequence <= checkpoint.LastEventSequence {
			continue
		}
		if _, created, err := pump.executions.RecordWorkerEvent(
			ctx, sessionID, reference.AttemptID, event.Sequence, event.OccurredAt,
			worker.Event{Type: worker.EventType(event.Type), Text: event.Text},
		); err != nil {
			return workeringest.PumpResult{}, err
		} else if created {
			newEvents++
		}
	}
	return workeringest.PumpResult{
		Reference: reference, LastAcceptedSequence: int64(len(events)),
		NewEvents: newEvents, Attempt: attempt, AttemptInspected: true,
	}, nil
}

func (stub *remoteLeadWorkspaceStub) Get(
	context.Context,
	string,
	string,
) (workspace.Workspace, error) {
	return stub.prepared, nil
}

func (stub *remoteLeadWorkspaceStub) Prepare(
	context.Context,
	string,
	string,
) (workspace.Workspace, bool, error) {
	stub.prepareCalls++
	return stub.prepared, false, nil
}

func (stub *remoteLeadWorkspaceStub) PublishPlan(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	plan string,
) (workspace.Workspace, bool, error) {
	stub.publishCalls++
	stub.publishedEventID = eventID
	stub.publishedPlan = plan
	return stub.prepared, stub.publishErr == nil, stub.publishErr
}

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
		Executions: executions, Features: database.NewFeatureStore(db),
		Goals: workflow.NewService(database.NewWorkflowStore(db)), Worker: client,
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
		Goals:  workflow.NewService(database.NewWorkflowStore(db)),
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
		Goals:  workflow.NewService(database.NewWorkflowStore(db)),
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
		Executions: executions, Features: database.NewFeatureStore(db),
		Goals: workflow.NewService(database.NewWorkflowStore(db)), Worker: client,
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

	actor := workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}
	accepted, err := starter.AcceptGoal(
		t.Context(), sessionID,
		"Export reports as CSV for administrators, including all visible columns.",
		actor, "accept-goal-1",
	)
	if err != nil {
		t.Fatalf("accept clarified goal: %v", err)
	}
	retried, err := starter.AcceptGoal(
		t.Context(), sessionID,
		"Export reports as CSV for administrators, including all visible columns.",
		actor, "accept-goal-1",
	)
	if err != nil || retried != accepted {
		t.Fatalf("retry accepted goal: event=%+v err=%v", retried, err)
	}
	acceptedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil {
		t.Fatalf("get accepted feature: %v", err)
	}
	if acceptedFeature.AcceptedGoal == "" || acceptedFeature.GoalAcceptedAt == nil ||
		acceptedFeature.State != feature.StateDraft {
		t.Fatalf("unexpected accepted feature %+v", acceptedFeature)
	}
	workflowEvents, err := database.NewWorkflowStore(db).ListEvents(t.Context(), storedFeature.ID)
	if err != nil || len(workflowEvents) != 1 || workflowEvents[0] != accepted {
		t.Fatalf("unexpected accepted-goal history %+v err=%v", workflowEvents, err)
	}
	if _, err := starter.SendCommand(t.Context(), sessionID, worker.Command{
		ID: "reply-after-acceptance", Type: worker.CommandMessage,
		Message: "Actually, change the scope.",
	}); !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("expected accepted goal to close clarification, got %v", err)
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
			Executions: executions, Features: database.NewFeatureStore(db),
			Goals: workflow.NewService(database.NewWorkflowStore(db)), Worker: client,
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
		feature.StateDraft,
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

func TestRemoteLeadStartsPlanningInManagedWorkspace(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_planning"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningCorrectionAttempts(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationReviewAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationCorrectionAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedSecondImplementationReviewAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	checkoutAt := now.Add(time.Second)
	prAt := checkoutAt.Add(time.Second)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_managed_feature", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "planning-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID: "0123456789abcdef0123456789abcdef01234567",
		Status:       workspace.StatusBranchReady, CheckoutRelativePath: "wsp_managed_feature",
		CheckoutCreatedAt: &checkoutAt, PullRequestNumber: 7,
		PullRequestURL:        "http://forgejo.test/commitarium/planning-test/pulls/7",
		PullRequestRecordedAt: &prAt,
	}}
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
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
	if _, err := starter.AcceptGoal(
		t.Context(), remoteLeadSessionID(runID),
		"Export the visible report columns as CSV.",
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"},
		"accept-planning-goal",
	); err != nil {
		t.Fatalf("accept goal: %v", err)
	}

	startedRun, admitted, err := starter.StartPlanning(
		t.Context(), runID, "start-planning",
	)
	if err != nil || !admitted || startedRun.Status != execution.RunStatusRunning {
		t.Fatalf("start planning: run=%+v admitted=%t err=%v", startedRun, admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	plannedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil || plannedFeature.State != feature.StatePlanning {
		t.Fatalf("feature did not enter planning: %+v err=%v", plannedFeature, err)
	}
	if workspaceStub.prepareCalls != 1 {
		t.Fatalf("expected one workspace reconciliation, got %d", workspaceStub.prepareCalls)
	}
	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected clarification and planning turns, got %+v", requests)
	}
	planningRequest := requests[1]
	if planningRequest.Mode != workerhttp.AttemptModeResume ||
		planningRequest.ProviderSessionID != "codex-thread-test" ||
		planningRequest.Assignment.WorkspaceID != workspaceStub.prepared.ID ||
		planningRequest.Assignment.Role != workerhttp.RoleLead ||
		!strings.Contains(planningRequest.Instructions, plannedFeature.AcceptedGoal) ||
		!strings.Contains(planningRequest.Instructions, "Do not modify files") {
		t.Fatalf("unexpected planning request %+v", planningRequest)
	}
	events, err := executions.EventsForSession(t.Context(), remoteLeadSessionID(runID))
	if err != nil {
		t.Fatalf("list lead planning activity: %v", err)
	}
	if events[len(events)-2].Type != worker.EventMessage ||
		events[len(events)-2].Text != "Proposed implementation plan" {
		t.Fatalf("planning proposal was not visible: %+v", events)
	}
	retriedRun, admitted, err := starter.StartPlanning(
		t.Context(), runID, "start-planning",
	)
	if err != nil || admitted || retriedRun.ID != runID {
		t.Fatalf("retry planning: run=%+v admitted=%t err=%v", retriedRun, admitted, err)
	}
	stub.mu.Lock()
	requestCount := len(stub.putRequests)
	stub.mu.Unlock()
	if requestCount != 2 {
		t.Fatalf("planning retry launched another worker turn: %d requests", requestCount)
	}

	reviewRun, admitted, err := starter.StartPlanningReview(
		t.Context(), runID, "start-reviewer",
	)
	if err != nil || !admitted || reviewRun.Status != execution.RunStatusRunning {
		t.Fatalf("start reviewer: run=%+v admitted=%t err=%v", reviewRun, admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	sessions, err := executions.SessionsForRun(t.Context(), runID)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("expected lead and reviewer sessions: %+v err=%v", sessions, err)
	}
	reviewer, err := executions.GetSession(t.Context(), remoteReviewerSessionID(runID))
	if err != nil || reviewer.Role != worker.RoleReviewer ||
		reviewer.Status != execution.SessionStatusWaitingForUser ||
		reviewer.ProviderSessionID != "codex-review-thread-test" {
		t.Fatalf("unexpected reviewer session %+v err=%v", reviewer, err)
	}
	stub.mu.Lock()
	requests = append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != 3 || requests[2].Mode != workerhttp.AttemptModeStart ||
		requests[2].Assignment.Role != workerhttp.RoleReviewer ||
		!strings.Contains(requests[2].Instructions, "Proposed implementation plan") ||
		!strings.Contains(requests[2].Instructions, "Do not modify files") {
		t.Fatalf("unexpected reviewer request %+v", requests)
	}
	messages, err := executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != 2 ||
		messages[0].Role != worker.RoleLead || messages[0].Event.Text != "Proposed implementation plan" ||
		messages[1].Role != worker.RoleReviewer || messages[1].Event.Text != "The plan needs stronger test coverage." {
		t.Fatalf("unexpected shared planning history %+v err=%v", messages, err)
	}
	reviewRun, admitted, err = starter.StartPlanningReview(
		t.Context(), runID, "start-reviewer",
	)
	if err != nil || admitted || reviewRun.ID != runID {
		t.Fatalf("retry reviewer: run=%+v admitted=%t err=%v", reviewRun, admitted, err)
	}
	stub.mu.Lock()
	requestCount = len(stub.putRequests)
	stub.mu.Unlock()
	if requestCount != 3 {
		t.Fatalf("reviewer retry launched another worker turn: %d requests", requestCount)
	}

	workspaceStub.publishErr = errors.New("Forgejo temporarily unavailable")
	roundRun, admitted, err := starter.StartPlanningRound(
		t.Context(), runID, "planning-round-1",
	)
	if err != nil || !admitted || roundRun.Status != execution.RunStatusRunning {
		t.Fatalf("start planning round: run=%+v admitted=%t err=%v", roundRun, admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	messages, err = executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != 5 ||
		messages[2].Role != worker.RoleLead || messages[2].Event.Type != worker.EventMessage ||
		messages[3].Role != worker.RoleReviewer ||
		messages[4].Role != worker.RoleLead || messages[4].Event.Type != worker.EventPlanSubmitted ||
		messages[4].Event.Text != "Complete final implementation plan" {
		t.Fatalf("unexpected completed planning round %+v err=%v", messages, err)
	}
	completedRound, err := executions.GetRun(t.Context(), runID)
	if err != nil || completedRound.Reason != planningPublicationReviewReason {
		t.Fatalf("unexpected planning decision run %+v err=%v", completedRound, err)
	}
	if workspaceStub.publishCalls != 1 || workspaceStub.publishedEventID != messages[4].Event.ID ||
		workspaceStub.publishedPlan != "Complete final implementation plan" {
		t.Fatalf("submitted plan was not attempted once: %+v", workspaceStub)
	}
	stub.mu.Lock()
	requests = append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != 6 || requests[3].Mode != workerhttp.AttemptModeResume ||
		requests[3].ProviderSessionID != "codex-thread-test" ||
		requests[3].Assignment.Role != workerhttp.RoleLead ||
		requests[3].OutputContract != workerhttp.OutputContractPlanningLead ||
		!strings.Contains(requests[3].Instructions, "The plan needs stronger test coverage.") ||
		requests[4].Mode != workerhttp.AttemptModeResume ||
		requests[4].ProviderSessionID != "codex-review-thread-test" ||
		requests[4].Assignment.Role != workerhttp.RoleReviewer ||
		requests[4].OutputContract != "" ||
		!strings.Contains(requests[4].Instructions, "deployment question remains") ||
		requests[5].Assignment.Role != workerhttp.RoleLead ||
		requests[5].OutputContract != workerhttp.OutputContractPlanningLead ||
		!strings.Contains(requests[5].Instructions, "resolves my remaining concern") {
		t.Fatalf("unexpected correction-round requests %+v", requests)
	}
	workspaceStub.publishErr = nil
	roundRun, admitted, err = starter.StartPlanningRound(
		t.Context(), runID, "planning-round-1",
	)
	if err != nil || !admitted || roundRun.Status != execution.RunStatusRunning {
		t.Fatalf("retry plan publication: run=%+v admitted=%t err=%v", roundRun, admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	waitForRemoteLeadIdle(t, starter, runID)
	completedRound, err = executions.GetRun(t.Context(), runID)
	if err != nil || completedRound.Reason != planningPlanPublishedReason {
		t.Fatalf("retried publication did not complete: run=%+v err=%v", completedRound, err)
	}
	leadEvents, err := executions.EventsForSession(t.Context(), remoteLeadSessionID(runID))
	if err != nil || leadEvents[len(leadEvents)-1].ID != planPublicationEventID(messages[4].Event) {
		t.Fatalf("plan publication was not visible in lead activity: %+v err=%v", leadEvents, err)
	}
	roundRun, admitted, err = starter.StartPlanningRound(
		t.Context(), runID, "planning-round-1",
	)
	if err != nil || admitted || roundRun.ID != runID {
		t.Fatalf("completed publication retry: run=%+v admitted=%t err=%v", roundRun, admitted, err)
	}
	stub.mu.Lock()
	requestCount = len(stub.putRequests)
	stub.mu.Unlock()
	if requestCount != 6 {
		t.Fatalf("planning round retry launched duplicate turns: %d requests", requestCount)
	}
	if workspaceStub.publishCalls != 2 {
		t.Fatalf("publication retry count = %d, want one failed and one successful attempt", workspaceStub.publishCalls)
	}

	workspaceStub.publicationVerifyErr = errors.New("Forgejo comment lookup unavailable")
	implementationRun, admitted, err := starter.StartImplementation(
		t.Context(), runID, "start-implementation-1",
	)
	if err != nil || !admitted || implementationRun.Status != execution.RunStatusRunning {
		t.Fatalf("start implementation: run=%+v admitted=%t err=%v", implementationRun, admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	implementedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil || implementedFeature.State != feature.StateImplementing {
		t.Fatalf("feature did not enter implementation: %+v err=%v", implementedFeature, err)
	}
	if workspaceStub.verifyCalls != 1 || workspaceStub.publishedEventID != messages[4].Event.ID ||
		workspaceStub.publishedPlan != messages[4].Event.Text {
		t.Fatalf("implementation did not verify the exact published plan: %+v", workspaceStub)
	}
	stub.mu.Lock()
	requests = append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != 7 || requests[6].Mode != workerhttp.AttemptModeResume ||
		requests[6].ProviderSessionID != "codex-thread-test" ||
		requests[6].Assignment.Role != workerhttp.RoleLead ||
		requests[6].Assignment.WorkspaceID != workspaceStub.prepared.ID ||
		!strings.Contains(requests[6].Instructions, implementedFeature.AcceptedGoal) ||
		!strings.Contains(requests[6].Instructions, messages[4].Event.Text) ||
		!strings.Contains(requests[6].Instructions, "push that exact HEAD") ||
		requests[6].OutputContract != workerhttp.OutputContractImplementationLead ||
		!strings.Contains(requests[6].Instructions, "Git HEAD") {
		t.Fatalf("unexpected implementation request %+v", requests[6])
	}
	failedVerification, err := executions.GetRun(t.Context(), runID)
	if err != nil || failedVerification.Reason == implementationPublishedReason ||
		workspaceStub.publicationVerifyCalls != 1 {
		t.Fatalf("unexpected failed publication verification: run=%+v err=%v", failedVerification, err)
	}
	stub.mu.Lock()
	requestCount = len(stub.putRequests)
	stub.mu.Unlock()
	workspaceStub.publicationVerifyErr = nil
	workspaceStub.responseVerifyErr = errors.New("Forgejo response lookup unavailable")
	implementationRun, admitted, err = starter.StartImplementation(
		t.Context(), runID, "retry-implementation-verification-1",
	)
	if err != nil || !admitted || implementationRun.Status != execution.RunStatusRunning {
		t.Fatalf("retry implementation verification: run=%+v admitted=%t err=%v", implementationRun, admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	waitForRemoteLeadIdle(t, starter, runID)
	interruptedCorrection, err := executions.GetRun(t.Context(), runID)
	if err != nil || interruptedCorrection.Reason == implementationApprovedReason ||
		workspaceStub.publicationVerifyCalls != 2 || workspaceStub.responseVerifyCalls != 1 ||
		workspaceStub.reviewVerifyCalls != 1 {
		t.Fatalf("unexpected interrupted correction checkpoint: run=%+v workspace=%+v err=%v", interruptedCorrection, workspaceStub, err)
	}
	reviewedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil || reviewedFeature.State != feature.StateReviewing {
		t.Fatalf("feature did not enter review: feature=%+v err=%v", reviewedFeature, err)
	}
	stub.mu.Lock()
	requests = append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != requestCount+2 {
		t.Fatalf("expected review and correction requests before recovery, got %+v", requests[requestCount:])
	}
	firstReviewRequest := requests[requestCount]
	correctionRequest := requests[requestCount+1]
	if firstReviewRequest.Mode != workerhttp.AttemptModeResume ||
		firstReviewRequest.ProviderSessionID != "codex-review-thread-test" ||
		firstReviewRequest.Assignment.Role != workerhttp.RoleReviewer ||
		firstReviewRequest.OutputContract != workerhttp.OutputContractImplementationReview ||
		!strings.Contains(firstReviewRequest.Instructions, "Exact implementation commit") ||
		!strings.Contains(firstReviewRequest.Instructions, "formal Forgejo review") {
		t.Fatalf("unexpected first implementation review request %+v", firstReviewRequest)
	}
	if correctionRequest.Mode != workerhttp.AttemptModeResume ||
		correctionRequest.ProviderSessionID != "codex-thread-test" ||
		correctionRequest.Assignment.Role != workerhttp.RoleLead ||
		correctionRequest.OutputContract != workerhttp.OutputContractImplementationLead ||
		!strings.Contains(correctionRequest.Instructions, "## Review response") ||
		!strings.Contains(correctionRequest.Instructions, "one additional failure-path test") ||
		!strings.Contains(correctionRequest.Instructions, "Exact reviewed commit") {
		t.Fatalf("unexpected correction request %+v", correctionRequest)
	}

	// Simulate a coordinator restart after the correction finished but before
	// its Forgejo audit entry could be verified. Recovery rechecks the existing
	// result and starts review round two without replacing the lead attempt.
	workspaceStub.responseVerifyErr = nil
	if _, err := executions.TransitionRun(
		t.Context(), runID, execution.RunStatusWaitingForUser,
		execution.RunStatusRunning, "Simulated coordinator interruption after correction.",
	); err != nil {
		t.Fatalf("prepare completed-correction recovery: %v", err)
	}
	correctionRestart, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
	})
	if err != nil {
		t.Fatalf("create completed-correction recovery starter: %v", err)
	}
	interruptedCorrection, err = executions.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("load completed-correction recovery run: %v", err)
	}
	if err := correctionRestart.Recover(
		t.Context(), interruptedCorrection, reviewedFeature, project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover completed implementation correction: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	waitForRemoteLeadIdle(t, correctionRestart, runID)
	completedReview, err := executions.GetRun(t.Context(), runID)
	if err != nil || completedReview.Reason != implementationApprovedReason ||
		workspaceStub.responseVerifyCalls != 2 || workspaceStub.reviewVerifyCalls != 2 ||
		workspaceStub.responseReviewedCommit != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		workspaceStub.responseCommit != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("unexpected corrective review checkpoint: run=%+v workspace=%+v err=%v", completedReview, workspaceStub, err)
	}
	stub.mu.Lock()
	requests = append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != requestCount+3 {
		t.Fatalf("correction recovery launched unexpected worker turns: %+v", requests[requestCount:])
	}
	secondReviewRequest := requests[requestCount+2]
	if secondReviewRequest.Mode != workerhttp.AttemptModeResume ||
		secondReviewRequest.ProviderSessionID != "codex-review-thread-test" ||
		secondReviewRequest.Assignment.Role != workerhttp.RoleReviewer ||
		secondReviewRequest.OutputContract != workerhttp.OutputContractImplementationReview ||
		!strings.Contains(secondReviewRequest.Instructions, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") ||
		!strings.Contains(secondReviewRequest.Instructions, "Published the corrected implementation") {
		t.Fatalf("unexpected second implementation review request %+v", secondReviewRequest)
	}

	// Recreate a restart after review round two completed but before its
	// verification checkpoint was trusted. Recovery re-verifies the same review
	// without launching another worker turn.
	if _, err := executions.TransitionRun(
		t.Context(), runID, execution.RunStatusWaitingForUser,
		execution.RunStatusRunning, "Simulated coordinator interruption after review.",
	); err != nil {
		t.Fatalf("prepare completed-review recovery: %v", err)
	}
	restarted, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
	})
	if err != nil {
		t.Fatalf("create completed-review recovery starter: %v", err)
	}
	interrupted, err := executions.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("load completed-review recovery run: %v", err)
	}
	if err := restarted.Recover(
		t.Context(), interrupted, reviewedFeature, project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover completed implementation review: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	if workspaceStub.reviewVerifyCalls != 3 {
		t.Fatalf("completed review recovery verification count = %d", workspaceStub.reviewVerifyCalls)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.putRequests) != len(requests) {
		t.Fatalf("completed review recovery launched a replacement worker attempt")
	}
}

func TestRemotePlanningLoopStopsAtTenMessages(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_planning_limit"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	for turn := 2; turn <= 5; turn++ {
		addCompletedPlanningCorrectionAttempt(
			stub, remoteLeadSessionID(runID), storedProject.ID, storedFeature.ID,
			workerhttp.RoleLead, "codex-thread-test", turn,
			workerhttp.EventMessage, fmt.Sprintf("Lead planning response %d", turn),
		)
		addCompletedPlanningCorrectionAttempt(
			stub, remoteReviewerSessionID(runID), storedProject.ID, storedFeature.ID,
			workerhttp.RoleReviewer, "codex-review-thread-test", turn,
			workerhttp.EventMessage, fmt.Sprintf("Reviewer planning response %d", turn),
		)
	}
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.September, 9, 23, 0, 0, 0, time.UTC)
	prAt := checkoutAt.Add(time.Second)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_managed_feature", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "planning-limit-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID: "0123456789abcdef0123456789abcdef01234567",
		Status:       workspace.StatusBranchReady, CheckoutRelativePath: "wsp_managed_feature",
		CheckoutCreatedAt: &checkoutAt, PullRequestNumber: 8,
		PullRequestURL: "http://forgejo.test/pulls/8", PullRequestRecordedAt: &prAt,
	}}
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
	})
	if err != nil {
		t.Fatalf("create planning starter: %v", err)
	}
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
	); err != nil {
		t.Fatalf("start lead: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	if _, err := starter.AcceptGoal(
		t.Context(), remoteLeadSessionID(runID), "Export the report as CSV.",
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, "accept-limit-goal",
	); err != nil {
		t.Fatalf("accept goal: %v", err)
	}
	if _, _, err := starter.StartPlanning(t.Context(), runID, "start-limit-planning"); err != nil {
		t.Fatalf("start planning: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	if _, _, err := starter.StartPlanningReview(t.Context(), runID, "start-limit-reviewer"); err != nil {
		t.Fatalf("start reviewer: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	if _, admitted, err := starter.StartPlanningRound(
		t.Context(), runID, "start-limited-loop",
	); err != nil || !admitted {
		t.Fatalf("start planning loop: admitted=%t err=%v", admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	messages, err := executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != maxPlanningMessages ||
		messages[len(messages)-1].Role != worker.RoleReviewer {
		t.Fatalf("unexpected bounded planning history: len=%d err=%v messages=%+v", len(messages), err, messages)
	}
	run, err := executions.GetRun(t.Context(), runID)
	if err != nil || run.Reason != planningLimitReason {
		t.Fatalf("unexpected bounded planning run %+v err=%v", run, err)
	}
	stub.mu.Lock()
	requestCount := len(stub.putRequests)
	stub.mu.Unlock()
	if requestCount != 11 {
		t.Fatalf("expected clarification plus ten planning turns, got %d requests", requestCount)
	}
	if _, admitted, err := starter.StartPlanningRound(
		t.Context(), runID, "start-limited-loop",
	); err != nil || admitted {
		t.Fatalf("bounded loop retry: admitted=%t err=%v", admitted, err)
	}
}

func TestRemoteLeadRecoveryReattachesToPlanningAttempt(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_planning_recovery"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	newStarter := func() *RemoteLeadStarter {
		starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
			Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
			Worker:   stub,
			Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
			Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
		})
		if err != nil {
			t.Fatalf("create real lead starter: %v", err)
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
	if _, err := starter.AcceptGoal(
		t.Context(), remoteLeadSessionID(runID), "Export the report as CSV.",
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, "accept-recovery-goal",
	); err != nil {
		t.Fatalf("accept goal: %v", err)
	}
	if _, err := workflowService.TransitionFeature(
		t.Context(), storedFeature.ID, feature.StatePlanning,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID},
		"start-planning-recovery",
	); err != nil {
		t.Fatalf("enter planning: %v", err)
	}
	sessionID := remoteLeadSessionID(runID)
	checkpoint, err := executions.GetWorkerAttempt(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("load clarification checkpoint: %v", err)
	}
	if admitted, err := executions.BeginAutonomousTurn(
		t.Context(), sessionID, checkpoint, planningAttemptID(sessionID),
		feature.StatePlanning,
		"The lead agent is inspecting the managed workspace and preparing a plan.",
	); err != nil || !admitted {
		t.Fatalf("admit interrupted planning turn: admitted=%t err=%v", admitted, err)
	}
	interruptedRun, err := executions.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("load interrupted planning run: %v", err)
	}
	if err := newStarter().Recover(
		t.Context(), interruptedRun, storedFeature,
		project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover planning attempt: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	recovered, err := executions.GetRun(t.Context(), runID)
	if err != nil || recovered.Reason != "The lead planning proposal is ready for reviewer consultation." {
		t.Fatalf("unexpected recovered planning run %+v err=%v", recovered, err)
	}
	stub.mu.Lock()
	putCount := len(stub.putRequests)
	stub.mu.Unlock()
	if putCount != 1 {
		t.Fatalf("recovery launched a replacement planning turn: put count=%d", putCount)
	}
	events, err := executions.EventsForSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("list recovered planning activity: %v", err)
	}
	foundAssessment := false
	foundPlan := false
	for _, event := range events {
		foundAssessment = foundAssessment || event.Type == worker.EventRecoveryAssessment
		foundPlan = foundPlan || event.Type == worker.EventMessage && event.Text == "Proposed implementation plan"
	}
	if !foundAssessment || !foundPlan {
		t.Fatalf("recovered planning was not fully observable: %+v", events)
	}
}

func TestRemoteReviewerRecoveryReattachesAndPublishesItsResponse(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_reviewer_recovery"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningCorrectionAttempts(stub, runID, storedProject.ID, storedFeature.ID)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.September, 9, 22, 0, 0, 0, time.UTC)
	prAt := checkoutAt.Add(time.Second)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_managed_feature", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "planning-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID: "0123456789abcdef0123456789abcdef01234567",
		Status:       workspace.StatusBranchReady, CheckoutRelativePath: "wsp_managed_feature",
		CheckoutCreatedAt: &checkoutAt, PullRequestNumber: 7,
		PullRequestURL: "http://forgejo.test/pulls/7", PullRequestRecordedAt: &prAt,
	}}
	newStarter := func(withWorkspace bool) *RemoteLeadStarter {
		config := RemoteLeadConfig{
			Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
			Planning: workflowService, Worker: stub,
			Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
			Lifetime: t.Context(), AgentProfileID: "codex-default", WorkspaceID: "project-read-only",
		}
		if withWorkspace {
			config.Workspaces = workspaceStub
		}
		starter, err := NewRemoteLeadStarter(config)
		if err != nil {
			t.Fatalf("create remote starter: %v", err)
		}
		return starter
	}
	starter := newStarter(true)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
	); err != nil {
		t.Fatalf("start lead: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	if _, err := starter.AcceptGoal(
		t.Context(), remoteLeadSessionID(runID), "Export the report as CSV.",
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, "accept-reviewer-recovery",
	); err != nil {
		t.Fatalf("accept goal: %v", err)
	}
	if _, _, err := starter.StartPlanning(t.Context(), runID, "start-planning"); err != nil {
		t.Fatalf("start lead planning: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	leadMessage, err := starter.messageForAttempt(
		t.Context(), remoteLeadSessionID(runID), planningAttemptID(remoteLeadSessionID(runID)),
	)
	if err != nil {
		t.Fatalf("find lead proposal: %v", err)
	}
	if _, _, err := executions.LinkPlanningMessage(t.Context(), runID, leadMessage.ID); err != nil {
		t.Fatalf("link lead proposal: %v", err)
	}
	reviewerID := remoteReviewerSessionID(runID)
	if admitted, err := executions.BeginNewSessionTurn(
		t.Context(), reviewerID, runID, remoteReviewerAgentID, worker.RoleReviewer,
		planningAttemptID(reviewerID), "The reviewer is inspecting the lead's planning proposal.",
	); err != nil || !admitted {
		t.Fatalf("admit interrupted reviewer: admitted=%t err=%v", admitted, err)
	}
	interruptedRun, err := executions.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("load interrupted run: %v", err)
	}
	if err := newStarter(false).Recover(
		t.Context(), interruptedRun, storedFeature, project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover reviewer: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	messages, err := executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != 2 || messages[1].Role != worker.RoleReviewer {
		t.Fatalf("reviewer response was not published after recovery: %+v err=%v", messages, err)
	}
	stub.mu.Lock()
	putCount := len(stub.putRequests)
	stub.mu.Unlock()
	if putCount != 2 {
		t.Fatalf("reviewer recovery launched a replacement attempt: %d PUTs", putCount)
	}

	// Simulate a stop after the reviewer response and waiting session were
	// durable, but before the run itself returned to waiting.
	completedRun, err := executions.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("load completed reviewer run: %v", err)
	}
	interruptedCompletion, err := executions.TransitionRun(
		t.Context(), runID, completedRun.Status, execution.RunStatusRunning,
		"Reviewer completion was interrupted.",
	)
	if err != nil {
		t.Fatalf("simulate interrupted run completion: %v", err)
	}
	if err := newStarter(false).Recover(
		t.Context(), interruptedCompletion, storedFeature, project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("finish interrupted reviewer completion: %v", err)
	}
	restoredRun, err := executions.GetRun(t.Context(), runID)
	if err != nil || restoredRun.Status != execution.RunStatusWaitingForUser ||
		restoredRun.Reason != "The reviewer's first planning response is ready." {
		t.Fatalf("unexpected restored reviewer completion %+v err=%v", restoredRun, err)
	}

	// Admit the lead revision but simulate the coordinator stopping before it
	// contacts the worker. Recovery must reattach to that exact attempt, then
	// route the durable revision to one new reviewer attempt without replaying
	// the lead mutation.
	leadID := remoteLeadSessionID(runID)
	leadCheckpoint, err := executions.GetWorkerAttempt(t.Context(), leadID)
	if err != nil {
		t.Fatalf("load lead checkpoint before correction recovery: %v", err)
	}
	if admitted, err := executions.BeginAutonomousTurn(
		t.Context(), leadID, leadCheckpoint, planningTurnAttemptID(leadID, 2),
		feature.StatePlanning,
		"The lead is revising the plan from the reviewer's response.",
	); err != nil || !admitted {
		t.Fatalf("admit interrupted lead revision: admitted=%t err=%v", admitted, err)
	}
	interruptedRevision, err := executions.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("load interrupted lead revision: %v", err)
	}
	if err := newStarter(true).Recover(
		t.Context(), interruptedRevision, storedFeature,
		project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover lead revision: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	messages, err = executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != 5 ||
		messages[2].Event.Text != "I addressed the test concern but one deployment question remains." ||
		messages[4].Event.Type != worker.EventPlanSubmitted ||
		messages[4].Event.Text != "Complete final implementation plan" {
		t.Fatalf("recovered planning round was incomplete: %+v err=%v", messages, err)
	}
	stub.mu.Lock()
	putCount = len(stub.putRequests)
	stub.mu.Unlock()
	if putCount != 4 {
		t.Fatalf("recovery should resume the loop without replaying the lead turn: %d PUTs", putCount)
	}
	if workspaceStub.publishCalls != 1 {
		t.Fatalf("recovered planning loop did not publish exactly once: %d", workspaceStub.publishCalls)
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

func addCompletedPlanningAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: planningAttemptID(sessionID),
	}
	now := time.Date(2026, time.September, 9, 20, 30, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleLead, WorkspaceID: "wsp_managed_feature",
		},
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
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Prepared a plan proposal.",
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage,
			Text: "I will inspect the repository before proposing the plan.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: workerhttp.EventMessage,
			Text: "Proposed implementation plan", OccurredAt: now.Add(time.Millisecond),
		},
		{
			AttemptReference: reference, Sequence: 3, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt,
		},
	}
}

func addCompletedReviewerPlanningAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
) {
	sessionID := remoteReviewerSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: planningAttemptID(sessionID),
	}
	now := time.Date(2026, time.September, 9, 20, 35, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeStart,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleReviewer, WorkspaceID: "wsp_managed_feature",
		},
		ProviderSessionID: "codex-review-thread-test", State: workerhttp.AttemptStateRunning,
		StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 2
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Reviewed the first planning proposal.",
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage,
			Text: "The plan needs stronger test coverage.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt,
		},
	}
}

func addCompletedPlanningCorrectionAttempts(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
) {
	addCompletedPlanningCorrectionAttempt(
		stub, remoteLeadSessionID(runID), projectID, featureID,
		workerhttp.RoleLead, "codex-thread-test", 2,
		workerhttp.EventMessage, "I addressed the test concern but one deployment question remains.",
	)
	addCompletedPlanningCorrectionAttempt(
		stub, remoteReviewerSessionID(runID), projectID, featureID,
		workerhttp.RoleReviewer, "codex-review-thread-test", 2,
		workerhttp.EventMessage, "The deployment answer resolves my remaining concern.",
	)
	addCompletedPlanningCorrectionAttempt(
		stub, remoteLeadSessionID(runID), projectID, featureID,
		workerhttp.RoleLead, "codex-thread-test", 3,
		workerhttp.EventPlanSubmitted, "Complete final implementation plan",
	)
}

func addCompletedPlanningCorrectionAttempt(
	stub *conversationalRemoteLeadWorker,
	sessionID string,
	projectID string,
	featureID string,
	role workerhttp.Role,
	providerSessionID string,
	turn int,
	eventType workerhttp.EventType,
	response string,
) {
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: planningTurnAttemptID(sessionID, turn),
	}
	now := time.Date(2026, time.September, 9, 20, 40, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: role, WorkspaceID: "wsp_managed_feature",
		},
		ProviderSessionID: providerSessionID, State: workerhttp.AttemptStateRunning,
		StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 3
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Completed a planning correction turn.",
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage,
			Text: "I will reconcile the repository state before responding.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: eventType,
			Text: response, OccurredAt: now.Add(time.Millisecond),
		},
		{
			AttemptReference: reference, Sequence: 3, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt,
		},
	}
}

func addCompletedImplementationAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: implementationAttemptID(sessionID, 1),
	}
	now := time.Date(2026, time.September, 10, 3, 30, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleLead, WorkspaceID: "wsp_managed_feature",
		},
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
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Implemented the agreed plan and passed focused tests.",
		Publication: &workerhttp.ImplementationPublication{
			CommitID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PullRequestNumber: 7,
		},
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventActivity,
			Text: "Inspected Git state before editing.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: workerhttp.EventMessage,
			Text: "Implemented the agreed files and ran focused tests.", OccurredAt: now.Add(time.Millisecond),
		},
		{
			AttemptReference: reference, Sequence: 3, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt,
		},
	}
}

func addCompletedImplementationReviewAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
) {
	sessionID := remoteReviewerSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: implementationReviewAttemptID(sessionID, 1),
	}
	now := time.Date(2026, time.September, 10, 3, 40, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleReviewer, WorkspaceID: "wsp_managed_feature",
		},
		ProviderSessionID: "codex-review-thread-test", State: workerhttp.AttemptStateRunning,
		StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 3
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionChangesRequested,
		Summary: "The implementation needs one additional failure-path test.",
		Review: &workerhttp.ReviewPublication{
			CommitID:          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			PullRequestNumber: 7, ReviewID: 11,
		},
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventActivity,
			Text: "Inspected the exact implementation commit.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: workerhttp.EventMessage,
			Text: terminal.Result.Summary, OccurredAt: now.Add(time.Millisecond),
		},
		{
			AttemptReference: reference, Sequence: 3, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt,
		},
	}
}

func addCompletedImplementationCorrectionAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: implementationCorrectionAttemptID(sessionID, 1),
	}
	now := time.Date(2026, time.September, 10, 3, 42, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleLead, WorkspaceID: "wsp_managed_feature",
		},
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
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Published the corrected implementation and added the missing failure-path test.",
		Publication: &workerhttp.ImplementationPublication{
			CommitID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PullRequestNumber: 7,
		},
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventActivity,
			Text: "Reconciled the exact reviewed commit and formal review before editing.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: workerhttp.EventMessage,
			Text: terminal.Result.Summary, OccurredAt: now.Add(time.Millisecond),
		},
		{
			AttemptReference: reference, Sequence: 3, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt,
		},
	}
}

func addCompletedSecondImplementationReviewAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
) {
	sessionID := remoteReviewerSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: implementationReviewAttemptID(sessionID, 2),
	}
	now := time.Date(2026, time.September, 10, 3, 44, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleReviewer, WorkspaceID: "wsp_managed_feature",
		},
		ProviderSessionID: "codex-review-thread-test", State: workerhttp.AttemptStateRunning,
		StartedAt: now, UpdatedAt: now,
	}
	endedAt := now.Add(time.Second)
	terminal := initial
	terminal.State = workerhttp.AttemptStateTerminal
	terminal.LatestEventSequence = 3
	terminal.UpdatedAt = endedAt
	terminal.EndedAt = &endedAt
	terminal.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "The corrected implementation addresses the finding and passes the relevant tests.",
		Review: &workerhttp.ReviewPublication{
			CommitID:          "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			PullRequestNumber: 7, ReviewID: 12,
		},
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventActivity,
			Text: "Inspected the corrected commit and reran the focused tests.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: workerhttp.EventMessage,
			Text: terminal.Result.Summary, OccurredAt: now.Add(time.Millisecond),
		},
		{
			AttemptReference: reference, Sequence: 3, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt,
		},
	}
}

func addCompletedImplementationContinuationAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
	turn int,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: implementationAttemptID(sessionID, turn),
	}
	now := time.Date(2026, time.September, 10, 4, turn, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex-default", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleLead, WorkspaceID: "wsp_managed_feature",
		},
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
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Published the corrected implementation and passed focused tests.",
		Publication: &workerhttp.ImplementationPublication{
			CommitID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", PullRequestNumber: 7,
		},
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventActivity,
			Text: "Reconciled existing user and agent changes before editing.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: workerhttp.EventMessage,
			Text: "Preserved the manual edit and completed focused tests.", OccurredAt: now.Add(time.Millisecond),
		},
		{
			AttemptReference: reference, Sequence: 3, Type: workerhttp.EventAttemptTerminal,
			Text: terminal.Result.Summary, OccurredAt: endedAt,
		},
	}
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

func waitForRemoteLeadIdle(
	t *testing.T,
	starter *RemoteLeadStarter,
	runID string,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if starter.claim(runID) {
			starter.release(runID)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %q remained claimed after reaching its durable waiting state", runID)
		}
		time.Sleep(time.Millisecond)
	}
}
