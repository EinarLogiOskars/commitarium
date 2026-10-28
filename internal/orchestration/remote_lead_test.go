package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const remoteLeadTestToken = "remote-lead-test-token"

type previewToolchainStub struct{ manifest toolchain.Manifest }

func (stub previewToolchainStub) Get(context.Context, string) (toolchain.Manifest, error) {
	return stub.manifest, nil
}

func TestPreviewRunInstructionsShareConfigAndConventions(t *testing.T) {
	starter := &RemoteLeadStarter{toolchains: previewToolchainStub{manifest: toolchain.Manifest{
		Run: &toolchain.RunConfig{Open: toolchain.RunOpen{Service: "web", Port: 5173}},
	}}}
	instructions, err := starter.previewRunInstructions(t.Context(), "prj_test")
	if err != nil || !strings.Contains(instructions, `"service":"web"`) ||
		!strings.Contains(instructions, "bind 0.0.0.0") ||
		!strings.Contains(instructions, "relative-path development proxy") ||
		!strings.Contains(instructions, "Build contexts, Dockerfiles, and bind mounts must resolve inside the repository") ||
		!strings.Contains(instructions, "must not also set image") ||
		!strings.Contains(instructions, "Every environment variable must have an explicit value") ||
		!strings.Contains(instructions, "Do not use Compose interpolation") {
		t.Fatalf("preview instructions=%q error=%v", instructions, err)
	}
}

type remoteLeadWorkerStub struct {
	mu         sync.Mutex
	putCalls   int
	putRequest workerhttp.PutAttemptRequest
	initial    workerhttp.Attempt
	terminal   workerhttp.Attempt
	events     []workerhttp.Event
}

type unavailableRemoteLeadWorker struct{}

type recoveryArtifactStub struct {
	artifact workflow.FeatureArtifact
	err      error
}

func (stub recoveryArtifactStub) GetFeatureArtifact(context.Context, string, featureartifact.Kind) (workflow.FeatureArtifact, error) {
	return stub.artifact, stub.err
}

func (recoveryArtifactStub) UpsertGoalDraft(context.Context, string, featureartifact.GoalDraft, workflow.Actor, string) (workflow.FeatureArtifact, error) {
	return workflow.FeatureArtifact{}, errors.New("unexpected goal draft write")
}

func (recoveryArtifactStub) UpsertImplementationPlan(context.Context, string, featureartifact.ImplementationPlan, workflow.Actor, string) (workflow.FeatureArtifact, error) {
	return workflow.FeatureArtifact{}, errors.New("unexpected plan write")
}

type conversationalRemoteLeadWorker struct {
	mu sync.Mutex
	// planChangeRequests makes that many unscripted plan approvals ask for
	// changes before later ones approve.
	planChangeRequests int
	putRequests        []workerhttp.PutAttemptRequest
	initial            map[workerhttp.AttemptReference]workerhttp.Attempt
	terminal           map[workerhttp.AttemptReference]workerhttp.Attempt
	events             map[workerhttp.AttemptReference][]workerhttp.Event
	// unsent attempts are unknown to the worker until they are PUT, like a
	// turn that never reached a worker.
	unsent map[workerhttp.AttemptReference]bool
	// onPut runs when a turn is sent, before it is observed, like work the
	// agent does during its turn.
	onPut func(workerhttp.AttemptReference)
}

func (stub *conversationalRemoteLeadWorker) PutAttempt(
	_ context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	attempt, ok := stub.initial[identity.AttemptReference]
	if !ok && strings.Contains(identity.AttemptID, ":approval:") {
		attempt = stub.addPlanApproval(identity.AttemptReference, request)
		ok = true
	}
	if !ok {
		return workerhttp.Attempt{}, false, errors.New("unexpected attempt identity")
	}
	delete(stub.unsent, identity.AttemptReference)
	stub.putRequests = append(stub.putRequests, request)
	if stub.onPut != nil {
		stub.onPut(identity.AttemptReference)
	}
	return attempt, true, nil
}

// addPlanApproval lets tests that do not script the reviewer's plan approval
// get an approving reviewer. The caller holds stub.mu.
func (stub *conversationalRemoteLeadWorker) addPlanApproval(
	reference workerhttp.AttemptReference,
	request workerhttp.PutAttemptRequest,
) workerhttp.Attempt {
	now := time.Date(2026, time.September, 9, 20, 50, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: request.Mode, Assignment: request.Assignment,
		ProviderSessionID: request.ProviderSessionID, State: workerhttp.AttemptStateRunning,
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
		Summary: "The final plan matches what we agreed.",
	}
	if stub.planChangeRequests > 0 {
		stub.planChangeRequests--
		terminal.Result.Disposition = workerhttp.DispositionChangesRequested
		terminal.Result.Summary = "The final plan dropped the failure-path test we agreed on."
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage, Text: terminal.Result.Summary, OccurredAt: now},
		{AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal, Text: terminal.Result.Summary, OccurredAt: endedAt},
	}
	return initial
}

func (stub *conversationalRemoteLeadWorker) GetAttempt(
	_ context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	attempt, ok := stub.terminal[reference]
	if !ok || stub.unsent[reference] {
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

func (*conversationalRemoteLeadWorker) Supersede(
	context.Context,
	workerhttp.MutationIdentity,
	workerhttp.SupersedeRequest,
) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{}, errors.New("supersede is not expected")
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

func (unavailableRemoteLeadWorker) Supersede(
	context.Context,
	workerhttp.MutationIdentity,
	workerhttp.SupersedeRequest,
) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{}, errors.New("worker state unavailable")
}

type unexpectedRemoteLeadPump struct{ called bool }

type remoteLeadWorkspaceStub struct {
	// mainHead, when set, is a default-branch head the session has not
	// merged yet.
	mainHead                      string
	mergeReadyClears              int
	prepared                      workspace.Workspace
	previousBase                  string
	clarificationCalls            int
	clarificationProjectID        string
	clarificationFeatureID        string
	publishCalls                  int
	revisedPublishCalls           int
	verifyCalls                   int
	revisedVerifyCalls            int
	continuationVerifyCalls       int
	publicationVerifyCalls        int
	revisedPublicationVerifyCalls int
	responseVerifyCalls           int
	replanningCalls               int
	replanningEventID             string
	replanningPlan                string
	reviewVerifyCalls             int
	reviewVerifyAttempts          []string
	readinessVerifyCalls          int
	mergeReadyCalls               int
	mergeCalls                    int
	mergeApprovalNotes            []string
	responseReviewedCommit        string
	responseChangesNothing        bool
	responseCommit                string
	publishedEventID              string
	publishedPlan                 string
	publishedBaseline             string
	publishErr                    error
	verifyErr                     error
	publicationVerifyErr          error
	responseVerifyErr             error
	reviewVerifyErr               error
	readinessVerifyErr            error
	// mergeErrs are returned, in order, by the next merges instead of merging.
	mergeErrs     []error
	mergedCommits []string
	// stillBehind keeps mainHead after a verified update onto main, as if the
	// update did not bring main in.
	stillBehind bool
	// mainMoves moves mainHead on after each verified update onto main, as if
	// other work merged meanwhile.
	mainMoves              bool
	baseUpdateVerifyCalls  int
	baseUpdateVerifyCommit string
	baseUpdateVerifyErr    error
	// failReviewVerifyCall makes that review verification fail once.
	failReviewVerifyCall int
	// reviewRefusals, publicationRefusals and responseRefusals refuse those
	// verifications by call number.
	reviewRefusals      map[int]error
	publicationRefusals map[int]error
	responseRefusals    map[int]error
	baseUpdateRefusals  map[int]error
}

func (stub *remoteLeadWorkspaceStub) PrepareReplanningBaseline(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	plan string,
) (workspace.Workspace, string, error) {
	stub.replanningCalls++
	stub.replanningEventID = eventID
	stub.replanningPlan = plan
	return stub.prepared, stub.prepared.BaseCommitID, nil
}

func (stub *remoteLeadWorkspaceStub) RecordMergeReady(
	_ context.Context,
	_ string,
	_ string,
	commitID string,
	pullRequestNumber int64,
) (workspace.Workspace, error) {
	stub.mergeReadyCalls++
	if stub.prepared.ApprovedCommitID != "" && stub.prepared.ApprovedCommitID != commitID {
		// Like the store, a pinned merge target is never silently replaced.
		return workspace.Workspace{}, workspace.ErrConflict
	}
	stub.prepared.ApprovedCommitID = commitID
	readyAt := time.Now().UTC()
	stub.prepared.MergeReadyAt = &readyAt
	stub.prepared.PullRequestNumber = pullRequestNumber
	return stub.prepared, nil
}

func (stub *remoteLeadWorkspaceStub) ClearMergeReady(_ context.Context, _ string, _ string) error {
	stub.mergeReadyClears++
	stub.prepared.ApprovedCommitID = ""
	stub.prepared.MergeReadyAt = nil
	return nil
}

func (stub *remoteLeadWorkspaceStub) SessionCatchUp(
	_ context.Context,
	_ string,
	_ string,
) (string, bool, error) {
	return stub.mainHead, stub.mainHead != "", nil
}

func (stub *remoteLeadWorkspaceStub) MergeApproved(
	_ context.Context,
	_ string,
	_ string,
	approvalNote ...string,
) (workspace.Workspace, bool, error) {
	stub.mergeCalls++
	stub.mergeApprovalNotes = append(stub.mergeApprovalNotes, approvalNote...)
	if len(stub.mergeErrs) > 0 {
		err := stub.mergeErrs[0]
		stub.mergeErrs = stub.mergeErrs[1:]
		return workspace.Workspace{}, false, err
	}
	if stub.prepared.MergeCommitID == "" {
		stub.mergedCommits = append(stub.mergedCommits, stub.prepared.ApprovedCommitID)
		stub.prepared.MergeCommitID = "dddddddddddddddddddddddddddddddddddddddd"
		mergedAt := time.Now().UTC()
		stub.prepared.MergedAt = &mergedAt
	}
	return stub.prepared, true, nil
}

func (stub *remoteLeadWorkspaceStub) PrepareForClarification(
	_ context.Context,
	projectID string,
	featureID string,
) (workspace.Workspace, bool, error) {
	stub.clarificationCalls++
	stub.clarificationProjectID = projectID
	stub.clarificationFeatureID = featureID
	if stub.prepared.ID == "" {
		stub.prepared.ID = "wsp_managed_feature"
	}
	if !stub.prepared.CheckoutReady() {
		createdAt := time.Now().UTC()
		stub.prepared.CheckoutRelativePath = stub.prepared.ID
		stub.prepared.CheckoutCreatedAt = &createdAt
	}
	return stub.prepared, false, nil
}

func (stub *remoteLeadWorkspaceStub) PrepareForStart(
	ctx context.Context,
	projectID string,
	featureID string,
) (workspace.Workspace, string, error) {
	prepared, _, err := stub.PrepareForClarification(ctx, projectID, featureID)
	return prepared, stub.previousBase, err
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
	if refusal := stub.publicationRefusals[stub.publicationVerifyCalls]; refusal != nil {
		return workspace.Workspace{}, refusal
	}
	return stub.prepared, stub.publicationVerifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyRevisedImplementationPublication(
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
	baselineCommitID string,
) (workspace.Workspace, error) {
	stub.revisedPublicationVerifyCalls++
	stub.publishedEventID = eventID
	stub.publishedPlan = plan
	stub.publishedBaseline = baselineCommitID
	return stub.prepared, stub.publicationVerifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyImplementationReview(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ string,
	attemptID string,
	_ string,
	_ string,
	_ int64,
	_ int64,
	_ string,
	_ string,
) (workspace.Workspace, error) {
	stub.reviewVerifyCalls++
	stub.reviewVerifyAttempts = append(stub.reviewVerifyAttempts, attemptID)
	if stub.reviewVerifyCalls == stub.failReviewVerifyCall {
		return workspace.Workspace{}, errors.New("the reviewer's review could not be confirmed")
	}
	if refusal := stub.reviewRefusals[stub.reviewVerifyCalls]; refusal != nil {
		return workspace.Workspace{}, refusal
	}
	return stub.prepared, stub.reviewVerifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyImplementationBaseUpdate(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ string,
	_ string,
	_ string,
	commitID string,
	_ int64,
	_ string,
) (workspace.Workspace, error) {
	stub.baseUpdateVerifyCalls++
	stub.baseUpdateVerifyCommit = commitID
	if refusal := stub.baseUpdateRefusals[stub.baseUpdateVerifyCalls]; refusal != nil {
		return workspace.Workspace{}, refusal
	}
	if stub.baseUpdateVerifyErr != nil {
		return workspace.Workspace{}, stub.baseUpdateVerifyErr
	}
	if stub.prepared.ApprovedCommitID == "" || stub.prepared.ApprovedCommitID == commitID {
		return workspace.Workspace{}, errors.New("base update does not follow an approved commit")
	}
	switch {
	case stub.mainMoves:
		stub.mainHead = fmt.Sprintf("%040x", stub.baseUpdateVerifyCalls)
	case !stub.stillBehind:
		stub.mainHead = ""
	}
	return stub.prepared, nil
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
	changesNothing bool,
) (workspace.Workspace, error) {
	stub.responseVerifyCalls++
	stub.responseChangesNothing = changesNothing
	stub.responseReviewedCommit = reviewedCommit
	if refusal := stub.responseRefusals[stub.responseVerifyCalls]; refusal != nil {
		return workspace.Workspace{}, refusal
	}
	stub.responseCommit = commit
	return stub.prepared, stub.responseVerifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyImplementationMergeReadiness(
	context.Context,
	string,
	string,
	string,
	string,
	string,
	string,
	string,
	int64,
	string,
) (workspace.Workspace, error) {
	stub.readinessVerifyCalls++
	return stub.prepared, stub.readinessVerifyErr
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

func (stub *remoteLeadWorkspaceStub) VerifyRevisedPublishedPlan(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	plan string,
	baselineCommitID string,
) (workspace.Workspace, error) {
	stub.revisedVerifyCalls++
	stub.publishedEventID = eventID
	stub.publishedPlan = plan
	stub.publishedBaseline = baselineCommitID
	return stub.prepared, stub.verifyErr
}

func (stub *remoteLeadWorkspaceStub) VerifyImplementationContinuation(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	plan string,
	baselineCommitID string,
) (workspace.Workspace, error) {
	stub.continuationVerifyCalls++
	stub.publishedEventID = eventID
	stub.publishedPlan = plan
	stub.publishedBaseline = baselineCommitID
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
	if stub.publishErr == nil && !stub.prepared.PullRequestReady() {
		createdAt := time.Now().UTC()
		stub.prepared.Status = workspace.StatusBranchReady
		stub.prepared.BranchCreatedAt = &createdAt
		stub.prepared.PullRequestNumber = 7
		stub.prepared.PullRequestURL = "http://forgejo.test/commitarium/planning-test/pulls/7"
		stub.prepared.PullRequestRecordedAt = &createdAt
	}
	return stub.prepared, stub.publishErr == nil, stub.publishErr
}

func (stub *remoteLeadWorkspaceStub) PublishRevisedPlan(
	_ context.Context,
	_ string,
	_ string,
	eventID string,
	plan string,
	baselineCommitID string,
) (workspace.Workspace, bool, error) {
	stub.revisedPublishCalls++
	stub.publishedEventID = eventID
	stub.publishedPlan = plan
	stub.publishedBaseline = baselineCommitID
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

func (stub *remoteLeadWorkerStub) Supersede(
	context.Context,
	workerhttp.MutationIdentity,
	workerhttp.SupersedeRequest,
) (workerhttp.Attempt, error) {
	return workerhttp.Attempt{}, errors.New("supersede is not expected")
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
	})
	if err != nil {
		t.Fatalf("create real lead starter: %v", err)
	}
	run, _, err := executions.CreateRun(
		t.Context(), runID, storedFeature.ID,
		project.DefaultDialogueRoundLimit, project.DefaultDialogueRoundLimit,
		project.DefaultAgentProviders(),
		project.DefaultMergePolicy(),
	)
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

func TestRemoteLeadRecoveryRebuildsSubmittedPlanArtifactFromExactAttempt(t *testing.T) {
	db, _, _, storedFeature := newRemoteLeadExecution(t)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	reference := workerhttp.AttemptReference{SessionID: "ses_plan", AttemptID: "att_plan"}
	workerStub := &conversationalRemoteLeadWorker{
		terminal: map[workerhttp.AttemptReference]workerhttp.Attempt{
			reference: {
				AttemptReference: reference,
				Result: &workerhttp.TerminalResult{
					Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
					Summary: "Submitted the agreed plan.",
					ImplementationPlan: &featureartifact.ImplementationPlan{
						Title: "Deliver export", Subtitle: "Two durable slices.",
						Steps: []featureartifact.ImplementationPlanStep{
							{
								ID: "contract", Title: "Define contract", Subtitle: "Add the API shape.",
								DetailsMarkdown: "Define the export API.", Verification: []string{"go test ./internal/export"},
								CommitSubject: "Add export contract",
							},
						},
					},
				},
			},
		},
	}
	starter := &RemoteLeadStarter{artifacts: workflowService, worker: workerStub}
	run := execution.Run{ID: "run_plan", FeatureID: storedFeature.ID, PlanVersion: 2}
	event := execution.Event{SessionID: reference.SessionID, WorkerAttemptID: reference.AttemptID}

	if err := starter.ensureSubmittedPlanArtifact(t.Context(), run, event); err != nil {
		t.Fatalf("recover submitted plan artifact: %v", err)
	}
	artifact, err := workflowService.GetFeatureArtifact(
		t.Context(), storedFeature.ID, featureartifact.KindImplementationPlan,
	)
	if err != nil {
		t.Fatalf("get recovered plan artifact: %v", err)
	}
	plan := featureartifact.ImplementationPlan{}
	if err := json.Unmarshal([]byte(artifact.Document), &plan); err != nil {
		t.Fatalf("decode recovered plan: %v", err)
	}
	if plan.PlanVersion != 2 || len(plan.Steps) != 1 || plan.Steps[0].Position != 1 ||
		plan.Steps[0].Status != featureartifact.StepPending {
		t.Fatalf("unexpected recovered plan %+v", plan)
	}
	if err := starter.ensureSubmittedPlanArtifact(t.Context(), run, event); err != nil {
		t.Fatalf("retry recovered plan artifact: %v", err)
	}
	retried, err := workflowService.GetFeatureArtifact(
		t.Context(), storedFeature.ID, featureartifact.KindImplementationPlan,
	)
	if err != nil || retried.Revision != artifact.Revision {
		t.Fatalf("retry created a new revision: before=%+v after=%+v err=%v", artifact, retried, err)
	}
}

func TestRemoteLeadRequiresReviewWhenWorkerStateCannotBeConfirmed(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	pump := &unexpectedRemoteLeadPump{}
	reported := make(chan error, 1)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db),
		Goals: workflowService, Planning: workflowService, Briefs: workflowService,
		Workspaces: &remoteLeadWorkspaceStub{}, Worker: unavailableRemoteLeadWorker{}, Pump: pump,
		Lifetime:    t.Context(),
		ReportError: func(err error) { reported <- err },
	})
	if err != nil {
		t.Fatalf("create real lead starter: %v", err)
	}
	runID := "run_remote_unavailable"
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits,
		storedProject.AgentProviders,
		storedProject.MergePolicy,
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

func TestRemoteLeadStartsPlanningInManagedWorkspace(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_remote_planning"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningCorrectionAttempts(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationReviewAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationCorrectionAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 1,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"Published the corrected implementation and added the missing failure-path test.",
	)
	addCompletedLaterImplementationReviewAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 2,
		workerhttp.DispositionChangesRequested,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 12,
		"The failure path now works, but its returned error needs a clearer message.",
	)
	addCompletedImplementationCorrectionAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 2,
		"cccccccccccccccccccccccccccccccccccccccc",
		"Clarified the returned error and passed the focused test suite.",
	)
	addCompletedLaterImplementationReviewAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 3,
		workerhttp.DispositionSucceeded,
		"cccccccccccccccccccccccccccccccccccccccc", 13,
		"The second correction addresses the remaining finding and passes the relevant tests.",
	)
	addCompletedImplementationReadinessAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 3,
		workerhttp.DispositionChangesRequested,
		"I am concerned that the approval does not address the deployment evidence.",
	)
	addCompletedLaterImplementationReviewAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 4,
		workerhttp.DispositionSucceeded,
		"cccccccccccccccccccccccccccccccccccccccc", 14,
		"The deployment evidence is present and the exact commit is ready to merge.",
	)
	addCompletedImplementationReadinessAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 4,
		workerhttp.DispositionSucceeded,
		"I agree after reconciling the deployment evidence; this commit is ready to merge.",
	)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC)
	checkoutAt := now.Add(time.Second)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_managed_feature", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "planning-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_managed_feature",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create real lead starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	startedRun, admitted, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits,
		storedProject.AgentProviders,
		storedProject.MergePolicy,
	)
	if err != nil || !admitted || startedRun.Status != execution.RunStatusRunning {
		t.Fatalf("start ready work order: run=%+v admitted=%t err=%v", startedRun, admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)

	plannedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil || plannedFeature.State != feature.StatePlanning {
		t.Fatalf("feature did not enter planning: %+v err=%v", plannedFeature, err)
	}
	if workspaceStub.clarificationCalls != 1 {
		t.Fatalf("expected start to prepare the checkout once, got %d", workspaceStub.clarificationCalls)
	}
	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("expected only the planning turn, got %+v", requests)
	}
	planningRequest := requests[0]
	if planningRequest.Mode != workerhttp.AttemptModeStart ||
		!strings.Contains(planningRequest.Instructions, "confirm that the areas it names still match the code") ||
		planningRequest.Assignment.WorkspaceID != workspaceStub.prepared.ID ||
		planningRequest.Assignment.Role != workerhttp.RoleLead ||
		!strings.Contains(planningRequest.Instructions, plannedFeature.AcceptedGoal) ||
		!strings.Contains(planningRequest.Instructions, "do not modify files") ||
		strings.Contains(planningRequest.Instructions, "Draft pull request: #") {
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
	retriedRun, admitted, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders, storedProject.MergePolicy,
	)
	if err != nil || admitted || retriedRun.ID != runID {
		t.Fatalf("retry start: run=%+v admitted=%t err=%v", retriedRun, admitted, err)
	}
	stub.mu.Lock()
	requestCount := len(stub.putRequests)
	stub.mu.Unlock()
	if requestCount != 1 {
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
	if len(requests) != 2 || requests[1].Mode != workerhttp.AttemptModeStart ||
		requests[1].Assignment.Role != workerhttp.RoleReviewer ||
		!strings.Contains(requests[1].Instructions, "Proposed implementation plan") ||
		!strings.Contains(requests[1].Instructions, "do not modify files") ||
		strings.Contains(requests[1].Instructions, "Draft pull request: #") {
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
	if requestCount != 2 {
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
	if len(requests) != 6 || requests[2].Mode != workerhttp.AttemptModeResume ||
		requests[2].ProviderSessionID != "codex-thread-test" ||
		requests[2].Assignment.Role != workerhttp.RoleLead ||
		requests[2].OutputContract != workerhttp.OutputContractPlanningLead ||
		!strings.Contains(requests[2].Instructions, "The plan needs stronger test coverage.") ||
		strings.Contains(requests[2].Instructions, "Draft pull request: #") ||
		requests[3].Mode != workerhttp.AttemptModeResume ||
		requests[3].ProviderSessionID != "codex-review-thread-test" ||
		requests[3].Assignment.Role != workerhttp.RoleReviewer ||
		requests[3].OutputContract != "" ||
		!strings.Contains(requests[3].Instructions, "deployment question remains") ||
		strings.Contains(requests[3].Instructions, "Draft pull request: #") ||
		requests[4].Assignment.Role != workerhttp.RoleLead ||
		requests[4].OutputContract != workerhttp.OutputContractPlanningLead ||
		!strings.Contains(requests[4].Instructions, "resolves my remaining concern") ||
		strings.Contains(requests[4].Instructions, "Draft pull request: #") ||
		requests[5].Assignment.Role != workerhttp.RoleReviewer ||
		requests[5].OutputContract != workerhttp.OutputContractPlanApproval ||
		!strings.Contains(requests[5].Instructions, "Complete final implementation plan") {
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
		strings.Contains(requests[6].Instructions, implementedFeature.AcceptedGoal) ||
		strings.Contains(requests[6].Instructions, messages[4].Event.Text) ||
		!strings.Contains(requests[6].Instructions, "commitarium-artifact plan show") ||
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
	implementationRun, admitted, err = starter.RecoverBlocker(
		t.Context(), runID, "retry-implementation-verification-1",
	)
	if err != nil || !admitted || implementationRun.Status != execution.RunStatusRunning {
		t.Fatalf("recover implementation verification: run=%+v admitted=%t err=%v", implementationRun, admitted, err)
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
	workspaceStub.readinessVerifyErr = errors.New("Forgejo readiness lookup unavailable")
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
		Lifetime: t.Context(),
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
	interruptedReadiness, err := executions.GetRun(t.Context(), runID)
	if err != nil || interruptedReadiness.Reason == implementationApprovedReason ||
		workspaceStub.responseVerifyCalls != 3 || workspaceStub.reviewVerifyCalls != 3 ||
		workspaceStub.readinessVerifyCalls != 1 {
		t.Fatalf("unexpected interrupted readiness checkpoint: run=%+v workspace=%+v err=%v", interruptedReadiness, workspaceStub, err)
	}
	if _, err := executions.TransitionRun(
		t.Context(), runID, execution.RunStatusWaitingForUser,
		execution.RunStatusRunning, "Simulated coordinator interruption after lead readiness.",
	); err != nil {
		t.Fatalf("prepare completed-readiness recovery: %v", err)
	}
	workspaceStub.readinessVerifyErr = nil
	readinessRestart, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create completed-readiness recovery starter: %v", err)
	}
	interruptedReadiness, err = executions.GetRun(t.Context(), runID)
	if err != nil {
		t.Fatalf("load completed-readiness recovery run: %v", err)
	}
	if err := readinessRestart.Recover(
		t.Context(), interruptedReadiness, reviewedFeature, project.RecoveryPolicyApprovalRequired,
	); err != nil {
		t.Fatalf("recover completed lead readiness: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	waitForRemoteLeadIdle(t, readinessRestart, runID)
	completedReview, err := executions.GetRun(t.Context(), runID)
	if err != nil || completedReview.Reason != implementationApprovedReason ||
		workspaceStub.responseVerifyCalls != 3 || workspaceStub.reviewVerifyCalls != 4 ||
		workspaceStub.readinessVerifyCalls != 3 ||
		workspaceStub.responseReviewedCommit != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ||
		workspaceStub.responseCommit != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("unexpected corrective review checkpoint: run=%+v workspace=%+v err=%v", completedReview, workspaceStub, err)
	}
	approvedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil || approvedFeature.State != feature.StateReadyToMerge {
		t.Fatalf("approved feature did not become ready to merge: feature=%+v err=%v", approvedFeature, err)
	}
	stub.mu.Lock()
	requests = append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != requestCount+8 {
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
	if !strings.Contains(secondReviewRequest.Instructions, "review what changed") ||
		!strings.Contains(secondReviewRequest.Instructions, "git diff ") ||
		strings.Contains(secondReviewRequest.Instructions, "diff from the planning baseline") {
		t.Fatalf("second review did not limit itself to the correction:\n%s", secondReviewRequest.Instructions)
	}
	secondCorrectionRequest := requests[requestCount+3]
	if secondCorrectionRequest.Mode != workerhttp.AttemptModeResume ||
		secondCorrectionRequest.ProviderSessionID != "codex-thread-test" ||
		secondCorrectionRequest.Assignment.Role != workerhttp.RoleLead ||
		secondCorrectionRequest.OutputContract != workerhttp.OutputContractImplementationLead ||
		!strings.Contains(secondCorrectionRequest.Instructions, "clearer message") ||
		!strings.Contains(secondCorrectionRequest.Instructions, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Fatalf("unexpected second correction request %+v", secondCorrectionRequest)
	}
	thirdReviewRequest := requests[requestCount+4]
	if thirdReviewRequest.Mode != workerhttp.AttemptModeResume ||
		thirdReviewRequest.ProviderSessionID != "codex-review-thread-test" ||
		thirdReviewRequest.Assignment.Role != workerhttp.RoleReviewer ||
		thirdReviewRequest.OutputContract != workerhttp.OutputContractImplementationReview ||
		!strings.Contains(thirdReviewRequest.Instructions, "cccccccccccccccccccccccccccccccccccccccc") ||
		!strings.Contains(thirdReviewRequest.Instructions, "Clarified the returned error") {
		t.Fatalf("unexpected third implementation review request %+v", thirdReviewRequest)
	}
	readinessRequest := requests[requestCount+5]
	if readinessRequest.Mode != workerhttp.AttemptModeResume ||
		readinessRequest.ProviderSessionID != "codex-thread-test" ||
		readinessRequest.Assignment.Role != workerhttp.RoleLead ||
		readinessRequest.OutputContract != workerhttp.OutputContractImplementationReadiness ||
		!strings.Contains(readinessRequest.Instructions, "cccccccccccccccccccccccccccccccccccccccc") ||
		!strings.Contains(readinessRequest.Instructions, "ready to merge") {
		t.Fatalf("unexpected lead merge-readiness request %+v", readinessRequest)
	}
	fourthReviewRequest := requests[requestCount+6]
	if fourthReviewRequest.OutputContract != workerhttp.OutputContractImplementationReview ||
		fourthReviewRequest.Assignment.Role != workerhttp.RoleReviewer ||
		!strings.Contains(fourthReviewRequest.Instructions, "deployment evidence") ||
		!strings.Contains(fourthReviewRequest.Instructions, "cccccccccccccccccccccccccccccccccccccccc") {
		t.Fatalf("unexpected review after lead concern %+v", fourthReviewRequest)
	}
	finalReadinessRequest := requests[requestCount+7]
	if finalReadinessRequest.OutputContract != workerhttp.OutputContractImplementationReadiness ||
		finalReadinessRequest.Assignment.Role != workerhttp.RoleLead ||
		!strings.Contains(finalReadinessRequest.Instructions, "deployment evidence") ||
		!strings.Contains(finalReadinessRequest.Instructions, "cccccccccccccccccccccccccccccccccccccccc") {
		t.Fatalf("unexpected final lead merge-readiness request %+v", finalReadinessRequest)
	}

	// Recreate a recovery blocker after mutual approval and the feature transition
	// were durable, but before the run's user gate was trusted. The explicit
	// recovery action restores the gate without re-verifying or launching another
	// worker turn.
	if _, err := executions.TransitionRun(
		t.Context(), runID, execution.RunStatusWaitingForUser,
		execution.RunStatusWaitingForUser,
		"The coordinator could not safely confirm the real Codex turn's state.",
		execution.RunWaitKindBlocker,
	); err != nil {
		t.Fatalf("prepare completed-review recovery: %v", err)
	}
	restarted, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create completed-review recovery starter: %v", err)
	}
	leftPaused, admitted, err := restarted.RecoverBlockerWithDirective(
		t.Context(), runID, "leave-completed-review-paused",
		RecoveryDirective{Action: RecoveryActionLeavePaused},
	)
	if err != nil || admitted || leftPaused.WaitKind != execution.RunWaitKindBlocker {
		t.Fatalf("leave-paused recovery changed the blocker: run=%+v admitted=%t err=%v", leftPaused, admitted, err)
	}
	rechecking, admitted, err := restarted.RecoverBlocker(
		t.Context(), runID, "recover-completed-review",
	)
	if err != nil || !admitted || rechecking.ID != runID {
		t.Fatalf("recover completed implementation review: run=%+v admitted=%t err=%v", rechecking, admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	recovered, err := executions.GetRun(t.Context(), runID)
	if err != nil || recovered.WaitKind != execution.RunWaitKindMergeGate {
		t.Fatalf("explicit recovery did not restore merge gate: run=%+v err=%v", recovered, err)
	}
	if workspaceStub.readinessVerifyCalls != 3 {
		t.Fatalf("ready-to-merge recovery repeated lead verification: count=%d", workspaceStub.readinessVerifyCalls)
	}
	stub.mu.Lock()
	requestCountAfterRecovery := len(stub.putRequests)
	stub.mu.Unlock()
	if requestCountAfterRecovery != len(requests) {
		t.Fatalf("completed review recovery launched a replacement worker attempt")
	}

	mergedRun, mergedNow, err := restarted.Merge(t.Context(), runID, "user-approved-merge")
	if err != nil || !mergedNow || mergedRun.Status != execution.RunStatusSucceeded ||
		workspaceStub.mergeCalls != 1 {
		t.Fatalf("merge approved run: run=%+v merged_now=%t calls=%d err=%v", mergedRun, mergedNow, workspaceStub.mergeCalls, err)
	}
	completedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil || completedFeature.State != feature.StateCompleted {
		t.Fatalf("merged feature was not completed: feature=%+v err=%v", completedFeature, err)
	}
	retriedMerge, mergedNow, err := restarted.Merge(t.Context(), runID, "user-approved-merge")
	if err != nil || mergedNow || retriedMerge.Status != execution.RunStatusSucceeded ||
		workspaceStub.mergeCalls != 1 {
		t.Fatalf("merge retry was not idempotent: run=%+v merged_now=%t calls=%d err=%v", retriedMerge, mergedNow, workspaceStub.mergeCalls, err)
	}
}

func TestRemoteLeadRecoveryAutomaticallyMergesApprovedRevision(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_automatic_merge_recovery"
	run, _, err := executions.CreateRun(
		t.Context(), runID, storedFeature.ID,
		project.DefaultDialogueRoundLimit, project.DefaultDialogueRoundLimit,
		project.DefaultAgentProviders(), project.MergePolicyAutoAfterGates,
	)
	if err != nil {
		t.Fatalf("create automatic-merge run: %v", err)
	}

	leadID := remoteLeadSessionID(runID)
	if _, _, err := executions.CreateSession(
		t.Context(), leadID, runID, remoteLeadAgentID, worker.RoleLead,
	); err != nil {
		t.Fatalf("create approved lead session: %v", err)
	}
	if _, err := executions.TransitionSession(
		t.Context(), leadID, execution.SessionStatusStarting,
		execution.SessionStatusRunning, "codex-lead-thread",
	); err != nil {
		t.Fatalf("start approved lead session: %v", err)
	}
	readinessAttemptID := implementationReadinessAttemptID(leadID, 1)
	if _, _, err := executions.CreateWorkerAttempt(
		t.Context(), leadID, readinessAttemptID,
	); err != nil {
		t.Fatalf("record lead readiness attempt: %v", err)
	}
	if _, err := executions.RecordSessionEventWithID(
		t.Context(), implementationReadinessVerificationEventID(readinessAttemptID), leadID,
		worker.Event{Type: worker.EventActivity, Text: "Verified the lead's green light."},
	); err != nil {
		t.Fatalf("record verified lead readiness: %v", err)
	}
	if _, err := executions.TransitionSession(
		t.Context(), leadID, execution.SessionStatusRunning,
		execution.SessionStatusWaitingForUser, "codex-lead-thread",
	); err != nil {
		t.Fatalf("park approved lead session: %v", err)
	}

	reviewerID := remoteReviewerSessionID(runID)
	if _, _, err := executions.CreateSession(
		t.Context(), reviewerID, runID, remoteReviewerAgentID, worker.RoleReviewer,
	); err != nil {
		t.Fatalf("create approved reviewer session: %v", err)
	}
	if _, err := executions.TransitionSession(
		t.Context(), reviewerID, execution.SessionStatusStarting,
		execution.SessionStatusRunning, "codex-reviewer-thread",
	); err != nil {
		t.Fatalf("start approved reviewer session: %v", err)
	}
	if _, err := executions.TransitionSession(
		t.Context(), reviewerID, execution.SessionStatusRunning,
		execution.SessionStatusWaitingForUser, "codex-reviewer-thread",
	); err != nil {
		t.Fatalf("park approved reviewer session: %v", err)
	}

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	actor := workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID}
	for index, state := range []feature.State{
		feature.StatePlanning, feature.StateImplementing,
		feature.StateReviewing, feature.StateReadyToMerge,
	} {
		if _, err := workflowService.TransitionFeature(
			t.Context(), storedFeature.ID, state, actor,
			fmt.Sprintf("automatic-merge-setup-%d", index),
		); err != nil {
			t.Fatalf("advance automatic-merge feature to %q: %v", state, err)
		}
	}
	readyFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil {
		t.Fatalf("load merge-ready feature: %v", err)
	}
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ApprovedCommitID:  "cccccccccccccccccccccccccccccccccccccccc",
		PullRequestNumber: 7,
	}}
	pump := &unexpectedRemoteLeadPump{}
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db),
		Goals: workflowService, Planning: workflowService, Workspaces: workspaceStub,
		Worker: unavailableRemoteLeadWorker{}, Pump: pump,
		Validation: validation.NewService(database.NewValidationStore(db)),
		Lifetime:   t.Context(),
	})
	if err != nil {
		t.Fatalf("create automatic-merge recovery starter: %v", err)
	}
	if err := starter.Recover(
		t.Context(), run, readyFeature, storedProject.RecoveryPolicy,
	); err != nil {
		t.Fatalf("recover automatic merge: %v", err)
	}

	mergedRun, err := executions.GetRun(t.Context(), runID)
	if err != nil || mergedRun.Status != execution.RunStatusSucceeded ||
		workspaceStub.mergeCalls != 1 || pump.called {
		t.Fatalf("automatic merge did not complete exactly once: run=%+v workspace=%+v pump=%+v err=%v", mergedRun, workspaceStub, pump, err)
	}
	completedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil || completedFeature.State != feature.StateCompleted {
		t.Fatalf("automatic merge did not complete feature: feature=%+v err=%v", completedFeature, err)
	}
}

func TestRemoteLeadRunToCompletionAdvancesFromPlanningToMergeGate(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	storedProject, err := database.NewProjectStore(db).UpdateAutonomyPolicy(
		t.Context(), storedProject.ID, project.AutonomyPolicyRunToCompletion,
	)
	if err != nil {
		t.Fatalf("enable run-to-completion: %v", err)
	}
	runID := "run_autonomous_workflow"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningCorrectionAttempts(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationReviewAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationCorrectionAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 1,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"Published the corrected implementation and added the missing failure-path test.",
	)
	addCompletedLaterImplementationReviewAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 2,
		workerhttp.DispositionSucceeded,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 12,
		"The correction addresses every finding and passes the relevant tests.",
	)
	addCompletedImplementationReadinessAttempt(
		stub, runID, storedProject.ID, storedFeature.ID, 2,
		workerhttp.DispositionSucceeded,
		"I agree with the reviewer; this exact commit is ready to merge.",
	)

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.September, 12, 11, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_autonomous", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "autonomous-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_autonomous",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:       &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Validation: validation.NewService(database.NewValidationStore(db)),
		Lifetime:   t.Context(),
	})
	if err != nil {
		t.Fatalf("create autonomous starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start goal clarification: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var completedRun execution.Run
	for time.Now().Before(deadline) {
		completedRun, err = executions.GetRun(t.Context(), runID)
		if err != nil {
			t.Fatalf("load autonomous run: %v", err)
		}
		currentFeature, featureErr := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
		if featureErr != nil {
			t.Fatalf("load autonomous feature: %v", featureErr)
		}
		if currentFeature.State == feature.StateReadyToMerge &&
			completedRun.Status == execution.RunStatusWaitingForUser {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if completedRun.Status != execution.RunStatusWaitingForUser ||
		completedRun.WaitKind != execution.RunWaitKindMergeGate || completedRun.Paused {
		t.Fatalf("autonomous workflow did not stop at merge policy gate: %+v", completedRun)
	}
	if completedRun.AutonomyPolicy != project.AutonomyPolicyRunToCompletion {
		t.Fatalf("run did not retain autonomy snapshot: %+v", completedRun)
	}
	stub.mu.Lock()
	requestCount := len(stub.putRequests)
	stub.mu.Unlock()
	if requestCount != 11 {
		t.Fatalf("expected complete autonomous conversation with 11 turns, got %d", requestCount)
	}
	if workspaceStub.publishCalls != 1 || workspaceStub.publicationVerifyCalls != 1 ||
		workspaceStub.reviewVerifyCalls != 2 || workspaceStub.responseVerifyCalls != 1 ||
		workspaceStub.readinessVerifyCalls != 1 || workspaceStub.mergeCalls != 0 {
		t.Fatalf("unexpected autonomous publication/review calls: %+v", workspaceStub)
	}
}

func TestRemoteLeadWithoutReviewerPlansImplementsAndMergesAlone(t *testing.T) {
	t.Run("submits in the first turn", func(t *testing.T) { testRemoteLeadWithoutReviewer(t, false) })
	t.Run("asked again after replying", func(t *testing.T) { testRemoteLeadWithoutReviewer(t, true) })
}

// testRemoteLeadWithoutReviewer runs a work order without a reviewer from
// Start to merge. When repliesFirst, the lead's first planning turn replies
// instead of submitting, and the coordinator asks it again.
func testRemoteLeadWithoutReviewer(t *testing.T, repliesFirst bool) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	storedProject, err := database.NewProjectStore(db).UpdateAutonomyPolicy(
		t.Context(), storedProject.ID, project.AutonomyPolicyRunToCompletion,
	)
	if err != nil {
		t.Fatalf("enable run-to-completion: %v", err)
	}
	runID := "run_without_reviewer"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	leadSessionID := remoteLeadSessionID(runID)
	planning := workerhttp.AttemptReference{SessionID: leadSessionID, AttemptID: planningAttemptID(leadSessionID)}
	turns := 2
	if repliesFirst {
		addCompletedPlanningCorrectionAttempt(
			stub, leadSessionID, storedProject.ID, storedFeature.ID,
			workerhttp.RoleLead, "codex-thread-test", 2,
			workerhttp.EventPlanSubmitted, "Complete final implementation plan",
		)
		turns = 3
	} else {
		stub.events[planning][1].Type = workerhttp.EventPlanSubmitted
		stub.events[planning][1].Text = "Complete final implementation plan"
	}
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.October, 11, 11, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_without_reviewer", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "without-reviewer-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_without_reviewer",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	features := database.NewFeatureStore(db)
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: features, Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:       &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Validation: validation.NewService(database.NewValidationStore(db)),
		Lifetime:   t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	chosen, err := features.GetByID(t.Context(), storedFeature.ID)
	if err != nil {
		t.Fatalf("load ready feature: %v", err)
	}
	chosen.Steps = project.WorkSteps{SkipReviewer: true}
	if err := features.UpdateReadySettings(t.Context(), chosen); err != nil {
		t.Fatalf("choose steps: %v", err)
	}
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var gated execution.Run
	for time.Now().Before(deadline) {
		gated, err = executions.GetRun(t.Context(), runID)
		if err != nil {
			t.Fatalf("load run: %v", err)
		}
		current, featureErr := features.GetByID(t.Context(), storedFeature.ID)
		if featureErr != nil {
			t.Fatalf("load feature: %v", featureErr)
		}
		if current.State == feature.StateReadyToMerge && gated.Status == execution.RunStatusWaitingForUser {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if gated.Status != execution.RunStatusWaitingForUser || gated.WaitKind != execution.RunWaitKindMergeGate ||
		!gated.Steps.SkipReviewer {
		t.Fatalf("run without a reviewer did not reach the merge gate: %+v", gated)
	}
	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != turns {
		t.Fatalf("expected %d lead turns, got %d", turns, len(requests))
	}
	if repliesFirst && (requests[1].OutputContract != workerhttp.OutputContractPlanningLead ||
		!strings.Contains(requests[1].Instructions, "submit_plan")) {
		t.Fatalf("the lead was not asked again to submit its plan: %+v", requests[1])
	}
	if requests[0].OutputContract != workerhttp.OutputContractPlanningLead ||
		!strings.Contains(requests[0].Instructions, "no reviewer") {
		t.Fatalf("first planning turn did not ask the lead to submit alone: %+v", requests[0])
	}
	for _, request := range requests {
		if request.Assignment.Role != workerhttp.RoleLead {
			t.Fatalf("a run without a reviewer started a %s turn", request.Assignment.Role)
		}
	}
	if _, err := executions.GetSession(t.Context(), remoteReviewerSessionID(runID)); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("a run without a reviewer created a reviewer session: %v", err)
	}
	if workspaceStub.publishCalls != 1 || workspaceStub.publicationVerifyCalls != 1 ||
		workspaceStub.reviewVerifyCalls != 0 || workspaceStub.mergeCalls != 0 {
		t.Fatalf("unexpected publication calls: %+v", workspaceStub)
	}

	if _, _, err := starter.Merge(t.Context(), runID, "merge-without-reviewer"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if workspaceStub.mergeCalls != 1 || len(workspaceStub.mergeApprovalNotes) != 1 ||
		!strings.Contains(workspaceStub.mergeApprovalNotes[0], "no code review") {
		t.Fatalf("merge did not carry the coordinator's approval note: %+v", workspaceStub.mergeApprovalNotes)
	}
}

func TestRemoteLeadQuickChangeImplementsTheGoalWithoutAPlan(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_quick_change"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_quick_change", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "quick-change-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_quick_change",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	features := database.NewFeatureStore(db)
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: features, Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:       &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Validation: validation.NewService(database.NewValidationStore(db)),
		Lifetime:   t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	chosen, err := features.GetByID(t.Context(), storedFeature.ID)
	if err != nil {
		t.Fatalf("load ready feature: %v", err)
	}
	chosen.Steps = project.WorkSteps{SkipPlan: true, SkipReviewer: true}
	if err := features.UpdateReadySettings(t.Context(), chosen); err != nil {
		t.Fatalf("choose steps: %v", err)
	}
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var gated execution.Run
	for time.Now().Before(deadline) {
		gated, err = executions.GetRun(t.Context(), runID)
		if err != nil {
			t.Fatalf("load run: %v", err)
		}
		if gated.Status == execution.RunStatusWaitingForUser {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	current, err := features.GetByID(t.Context(), storedFeature.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if current.State != feature.StateReadyToMerge || gated.WaitKind != execution.RunWaitKindMergeGate {
		t.Fatalf("quick change did not reach the merge gate: feature=%s run=%+v", current.State, gated)
	}
	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != 1 {
		t.Fatalf("expected a single lead turn, got %d", len(requests))
	}
	first := requests[0]
	if first.Mode != workerhttp.AttemptModeStart || first.ProviderSessionID != "" ||
		first.OutputContract != workerhttp.OutputContractImplementationLead ||
		!strings.Contains(first.Instructions, "Accepted goal:") ||
		strings.Contains(first.Instructions, "commitarium-artifact plan") {
		t.Fatalf("quick change did not start by implementing the goal: %+v", first)
	}
	if workspaceStub.publishCalls != 1 || workspaceStub.publishedPlan != noDocumentedPlan ||
		workspaceStub.publicationVerifyCalls != 1 {
		t.Fatalf("quick change did not open its pull request without a plan: %+v", workspaceStub)
	}
	if _, _, err := starter.Merge(t.Context(), runID, "merge-quick-change"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if workspaceStub.mergeCalls != 1 || len(workspaceStub.mergeApprovalNotes) != 1 {
		t.Fatalf("quick change did not merge with the coordinator's approval: %+v", workspaceStub)
	}
}

func TestRemotePlanningLoopUsesRunLimitSnapshot(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	limits := project.DialogueLimits{PlanningRounds: 3, ImplementationReviewRounds: 4}
	storedProject, err := database.NewProjectStore(db).UpdateDialogueLimits(
		t.Context(), storedProject.ID, limits,
	)
	if err != nil {
		t.Fatalf("set project dialogue limits: %v", err)
	}
	runID := "run_remote_planning_limit"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	for turn := 2; turn <= limits.PlanningRounds; turn++ {
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
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create planning starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits,
		storedProject.AgentProviders,
		storedProject.MergePolicy,
	); err != nil {
		t.Fatalf("start lead: %v", err)
	}
	if _, err := database.NewProjectStore(db).UpdateDialogueLimits(
		t.Context(), storedProject.ID,
		project.DialogueLimits{PlanningRounds: 1, ImplementationReviewRounds: 1},
	); err != nil {
		t.Fatalf("change project limits after run admission: %v", err)
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
	if err != nil || len(messages) != limits.PlanningRounds*2 ||
		messages[len(messages)-1].Role != worker.RoleReviewer {
		t.Fatalf("unexpected bounded planning history: len=%d err=%v messages=%+v", len(messages), err, messages)
	}
	run, err := executions.GetRun(t.Context(), runID)
	if err != nil || run.Reason != planningLimitReason(limits.PlanningRounds) ||
		run.PlanningRoundLimit != limits.PlanningRounds ||
		run.ImplementationReviewRoundLimit != limits.ImplementationReviewRounds {
		t.Fatalf("unexpected bounded planning run %+v err=%v", run, err)
	}
	stub.mu.Lock()
	requestCount := len(stub.putRequests)
	stub.mu.Unlock()
	if requestCount != 6 {
		t.Fatalf("expected six planning turns, got %d requests", requestCount)
	}
	extraRound := limits.PlanningRounds + 1
	addCompletedPlanningCorrectionAttempt(
		stub, remoteLeadSessionID(runID), storedProject.ID, storedFeature.ID,
		workerhttp.RoleLead, "codex-thread-test", extraRound,
		workerhttp.EventMessage, "Lead planning response after user continuation",
	)
	addCompletedPlanningCorrectionAttempt(
		stub, remoteReviewerSessionID(runID), storedProject.ID, storedFeature.ID,
		workerhttp.RoleReviewer, "codex-review-thread-test", extraRound,
		workerhttp.EventMessage, "Reviewer response after user continuation",
	)
	if _, admitted, err := starter.StartPlanningRound(
		t.Context(), runID, "continue-after-round-cap",
	); err != nil || !admitted {
		t.Fatalf("continue bounded loop: admitted=%t err=%v", admitted, err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	messages, err = executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != extraRound*2 {
		t.Fatalf("extra planning round history: len=%d err=%v messages=%+v", len(messages), err, messages)
	}
	run, err = executions.GetRun(t.Context(), runID)
	if err != nil || run.PlanningRoundLimit != extraRound ||
		run.WaitKind != execution.RunWaitKindRoundCap {
		t.Fatalf("extra planning round did not establish a new cap: run=%+v err=%v", run, err)
	}
	if _, admitted, err := starter.StartPlanningRound(
		t.Context(), runID, "continue-after-round-cap",
	); err != nil || admitted {
		t.Fatalf("continued-round retry: admitted=%t err=%v", admitted, err)
	}
}

// A pause during the planning discussion stops the run after the lead's
// response. Continue planning then starts the reviewer's answer to it, in the
// reviewer's conversation, and the discussion goes on as a normal round.
func TestContinuePlanningAfterAPausedLeadResponseStartsTheReviewer(t *testing.T) {
	for _, policy := range []project.AutonomyPolicy{
		project.AutonomyPolicyReviewEachPhase, project.AutonomyPolicyRunToCompletion,
	} {
		t.Run(string(policy), func(t *testing.T) {
			testContinuePlanningAfterAPausedLeadResponse(t, policy)
		})
	}
}

func testContinuePlanningAfterAPausedLeadResponse(t *testing.T, policy project.AutonomyPolicy) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	projects := database.NewProjectStore(db)
	limits := project.DialogueLimits{PlanningRounds: 3, ImplementationReviewRounds: 4}
	storedProject, err := projects.UpdateDialogueLimits(t.Context(), storedProject.ID, limits)
	if err != nil {
		t.Fatalf("set project dialogue limits: %v", err)
	}
	if storedProject, err = projects.UpdateAutonomyPolicy(t.Context(), storedProject.ID, policy); err != nil {
		t.Fatalf("set autonomy policy: %v", err)
	}
	runID := "run_continue_after_lead"
	leadID, reviewerID := remoteLeadSessionID(runID), remoteReviewerSessionID(runID)
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	for turn := 2; turn <= limits.PlanningRounds; turn++ {
		addCompletedPlanningCorrectionAttempt(
			stub, leadID, storedProject.ID, storedFeature.ID,
			workerhttp.RoleLead, "codex-thread-test", turn,
			workerhttp.EventMessage, fmt.Sprintf("Lead planning response %d", turn),
		)
		addCompletedPlanningCorrectionAttempt(
			stub, reviewerID, storedProject.ID, storedFeature.ID,
			workerhttp.RoleReviewer, "codex-review-thread-test", turn,
			workerhttp.EventMessage, fmt.Sprintf("Reviewer planning response %d", turn),
		)
	}
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.September, 9, 23, 0, 0, 0, time.UTC)
	prAt := checkoutAt.Add(time.Second)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_managed_feature", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "continue-after-lead-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID: "0123456789abcdef0123456789abcdef01234567",
		Status:       workspace.StatusBranchReady, CheckoutRelativePath: "wsp_managed_feature",
		CheckoutCreatedAt: &checkoutAt, PullRequestNumber: 8,
		PullRequestURL: "http://forgejo.test/pulls/8", PullRequestRecordedAt: &prAt,
	}}
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create planning starter: %v", err)
	}
	// The user pauses while the lead writes its second planning response.
	leadResponse := workerhttp.AttemptReference{SessionID: leadID, AttemptID: planningTurnAttemptID(leadID, 2)}
	stub.onPut = func(reference workerhttp.AttemptReference) {
		if reference == leadResponse {
			if _, _, err := starter.Pause(context.WithoutCancel(t.Context()), runID, "pause-during-lead"); err != nil {
				t.Errorf("pause during the lead's response: %v", err)
			}
		}
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start lead: %v", err)
	}
	if policy != project.AutonomyPolicyRunToCompletion {
		waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
		waitForRemoteLeadIdle(t, starter, runID)
		if _, _, err := starter.StartPlanningReview(t.Context(), runID, "start-reviewer"); err != nil {
			t.Fatalf("start reviewer: %v", err)
		}
		waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
		waitForRemoteLeadIdle(t, starter, runID)
		if _, admitted, err := starter.StartPlanningRound(t.Context(), runID, "start-loop"); err != nil || !admitted {
			t.Fatalf("start planning loop: admitted=%t err=%v", admitted, err)
		}
	}

	// The run stops after the lead's response, with the lead speaking last.
	deadline := time.Now().Add(2 * time.Second)
	var run execution.Run
	for {
		run, err = executions.GetRun(t.Context(), runID)
		if err == nil && run.Status == execution.RunStatusWaitingForUser && run.Paused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not stop at the pause: run=%+v err=%v", run, err)
		}
		time.Sleep(time.Millisecond)
	}
	waitForRemoteLeadIdle(t, starter, runID)
	messages, err := executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != 3 || messages[2].Role != worker.RoleLead {
		t.Fatalf("expected the lead to speak last: len=%d err=%v messages=%+v", len(messages), err, messages)
	}
	stub.mu.Lock()
	requestsBefore := len(stub.putRequests)
	stub.mu.Unlock()
	if _, _, err := starter.Resume(t.Context(), runID, "resume-after-lead"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if policy != project.AutonomyPolicyRunToCompletion {
		run, err = executions.GetRun(t.Context(), runID)
		if err != nil || run.Paused || run.Status != execution.RunStatusWaitingForUser ||
			run.WaitKind != execution.RunWaitKindPhaseCheckpoint {
			t.Fatalf("expected a resumed phase checkpoint: run=%+v err=%v", run, err)
		}
		if _, admitted, err := starter.StartPlanningRound(t.Context(), runID, "continue-after-lead"); err != nil || !admitted {
			t.Fatalf("continue planning after the lead spoke last: admitted=%t err=%v", admitted, err)
		}
	}

	// The reviewer answers in its own conversation, then the lead, until the cap.
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != requestsBefore+3 {
		t.Fatalf("expected the reviewer, lead and reviewer turns after continuing, got %d new requests", len(requests)-requestsBefore)
	}
	next := requests[requestsBefore]
	if next.Assignment.Role != workerhttp.RoleReviewer || next.Mode != workerhttp.AttemptModeResume ||
		next.ProviderSessionID != "codex-review-thread-test" ||
		!strings.Contains(next.Instructions, "Lead planning response 2") {
		t.Fatalf("expected the reviewer to answer the lead in its conversation: %+v", next)
	}
	if requests[requestsBefore+1].Assignment.Role != workerhttp.RoleLead ||
		requests[requestsBefore+2].Assignment.Role != workerhttp.RoleReviewer {
		t.Fatalf("the discussion did not continue in turn: %+v", requests[requestsBefore:])
	}
	messages, err = executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != limits.PlanningRounds*2 ||
		messages[len(messages)-1].Role != worker.RoleReviewer {
		t.Fatalf("unexpected planning history: len=%d err=%v messages=%+v", len(messages), err, messages)
	}
	run, err = executions.GetRun(t.Context(), runID)
	if err != nil || run.WaitKind != execution.RunWaitKindRoundCap ||
		run.Reason != planningLimitReason(limits.PlanningRounds) {
		t.Fatalf("expected the planning round cap: run=%+v err=%v", run, err)
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
			Workspaces: &remoteLeadWorkspaceStub{}, Worker: stub,
			Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
			Lifetime: t.Context(),
		})
		if err != nil {
			t.Fatalf("create real lead starter: %v", err)
		}
		return starter
	}
	// Recreate what Start records before it launches the lead's first turn,
	// as if the coordinator stopped right after admitting it.
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	sessionID := remoteLeadSessionID(runID)
	if _, err := workflowService.AcceptGoalAtStart(
		t.Context(), storedFeature.ID, sessionID, "Export the report as CSV.",
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, runID+":start-goal",
	); err != nil {
		t.Fatalf("accept goal at start: %v", err)
	}
	if _, _, err := executions.CreateRunWithModels(
		t.Context(), runID, storedFeature.ID, storedProject.DialogueLimits.PlanningRounds,
		storedProject.DialogueLimits.ImplementationReviewRounds, storedProject.AgentProviders,
		project.AgentModels{}, storedProject.MergePolicy, project.DefaultAutonomyPolicy(),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := workflowService.TransitionFeature(
		t.Context(), storedFeature.ID, feature.StatePlanning,
		workflow.Actor{Kind: workflow.ActorKindCoordinator, ID: coordinatorActorID}, runID+":start-planning",
	); err != nil {
		t.Fatalf("enter planning: %v", err)
	}
	if _, _, err := executions.CreateSession(
		t.Context(), sessionID, runID, agentID(storedProject.AgentProviders.LeadAgent, worker.RoleLead), worker.RoleLead,
	); err != nil {
		t.Fatalf("create lead session: %v", err)
	}
	if _, _, err := executions.CreateWorkerAttempt(t.Context(), sessionID, planningAttemptID(sessionID)); err != nil {
		t.Fatalf("admit interrupted planning turn: %v", err)
	}
	storedFeature, err := database.NewFeatureStore(db).GetByID(t.Context(), storedFeature.ID)
	if err != nil {
		t.Fatalf("load planning feature: %v", err)
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
	if putCount != 0 {
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
			Planning: workflowService, Briefs: workflowService, Worker: stub,
			Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
			Lifetime: t.Context(),
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
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits,
		storedProject.AgentProviders,
		storedProject.MergePolicy,
	); err != nil {
		t.Fatalf("start lead: %v", err)
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
	if putCount != 1 {
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
	// The lead's proposal, the reviewer's response, the lead's submission, and
	// the plan approval.
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
	storedProject, err := projectService.Create(
		t.Context(),
		"Remote lead test",
		project.RecoveryPolicyApprovalRequired,
		project.DefaultDialogueLimits(),
		project.DefaultAgentProviders(),
		project.DefaultMergePolicy(),
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	featureService := feature.NewService(database.NewFeatureStore(db), projectService)
	storedFeature, err := featureService.Create(
		t.Context(), storedProject.ID, "Clarify export", "Choose the supported formats",
		feature.SettingsOverrides{},
	)
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
		AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
		Role: workerhttp.RoleLead, WorkspaceID: "wsp_managed_feature",
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
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Asked one clarification question.", GoalDraft: &featureartifact.GoalDraft{
			Goal:          "Build the feature after confirming success criteria.",
			OpenQuestions: []string{"What should success look like?"},
		},
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
				AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
				Role: workerhttp.RoleLead, WorkspaceID: "wsp_managed_feature",
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
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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
	round int,
	commitID string,
	summary string,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: implementationCorrectionAttemptID(sessionID, round),
	}
	now := time.Date(2026, time.September, 10, 3, 40+round*2, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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
		Summary: summary,
		Publication: &workerhttp.ImplementationPublication{
			CommitID: commitID, PullRequestNumber: 7,
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

func addCompletedLaterImplementationReviewAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
	round int,
	disposition workerhttp.Disposition,
	commitID string,
	reviewID int64,
	summary string,
) {
	sessionID := remoteReviewerSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: implementationReviewAttemptID(sessionID, round),
	}
	now := time.Date(2026, time.September, 10, 3, 41+round*2, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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
		Outcome: workerhttp.OutcomeCompleted, Disposition: disposition,
		Summary: summary,
		Review: &workerhttp.ReviewPublication{
			CommitID: commitID, PullRequestNumber: 7, ReviewID: reviewID,
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

func addCompletedImplementationReadinessAttempt(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
	round int,
	disposition workerhttp.Disposition,
	summary string,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{
		SessionID: sessionID, AttemptID: implementationReadinessAttemptID(sessionID, round),
	}
	now := time.Date(2026, time.September, 10, 3, 50+round, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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
		Outcome: workerhttp.OutcomeCompleted, Disposition: disposition, Summary: summary,
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{
			AttemptReference: reference, Sequence: 1, Type: workerhttp.EventActivity,
			Text: "Reconciled the exact approved commit and formal review.", OccurredAt: now,
		},
		{
			AttemptReference: reference, Sequence: 2, Type: workerhttp.EventMessage,
			Text: summary, OccurredAt: now.Add(time.Millisecond),
		},
		{
			AttemptReference: reference, Sequence: 3, Type: workerhttp.EventAttemptTerminal,
			Text: summary, OccurredAt: endedAt,
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
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
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

func TestDialogueRoundLimitsAndReviewRouting(t *testing.T) {
	tests := []struct {
		name        string
		disposition workerhttp.Disposition
		want        implementationReviewAction
	}{
		{name: "requested changes start correction", disposition: workerhttp.DispositionChangesRequested, want: implementationReviewActionCorrect},
		{name: "approval starts lead acknowledgement", disposition: workerhttp.DispositionSucceeded, want: implementationReviewActionAcknowledge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextImplementationReviewAction(test.disposition); got != test.want {
				t.Fatalf("review action = %q, want %q", got, test.want)
			}
		})
	}
	if dialogueRoundLimitReached(0, 1000) {
		t.Fatal("zero round limit did not remain unlimited")
	}
	if planningRoundLimitReached(0, 1000) {
		t.Fatal("zero planning-round limit did not remain unlimited")
	}
	if planningRoundLimitReached(3, 5) || !planningRoundLimitReached(3, 6) {
		t.Fatal("three planning rounds did not mean six messages")
	}
	if planningRoundLimitReached(6, 11) || !planningRoundLimitReached(6, 12) {
		t.Fatal("planning did not treat six rounds as twelve messages")
	}
	if implementationReviewRoundLimitReached(0, 1000) {
		t.Fatal("zero implementation-review limit did not remain unlimited")
	}
	if implementationReviewRoundLimitReached(3, 2) || !implementationReviewRoundLimitReached(3, 3) {
		t.Fatal("implementation review did not use complete rounds")
	}
	if !strings.Contains(implementationReviewLimitReason(4), "4 review rounds") {
		t.Fatalf("review-limit reason does not reflect configured limit: %q", implementationReviewLimitReason(4))
	}
}

func TestImplementationReviewInstructionsUseForgejoApprovalEvent(t *testing.T) {
	instructions := implementationReviewInstructions(
		workspace.Workspace{}, "summary", "commit", "attempt", "", false,
	)
	if !strings.Contains(instructions, "submit one review with event APPROVED.") {
		t.Fatalf("review instructions do not use Forgejo's APPROVED event: %q", instructions)
	}
	if strings.Contains(instructions, "submit one review with event APPROVE.") {
		t.Fatalf("review instructions use unsupported APPROVE event: %q", instructions)
	}
}

func TestIndependentAcceptanceInstructionsKeepAuthoringBlindAndReviewLocal(t *testing.T) {
	authoring := acceptanceTestsInstructions(
		workspace.Workspace{RepositoryOwner: "owner", RepositoryName: "repo", BaseBranch: "main", BaseCommitID: strings.Repeat("a", 40)},
	)
	for _, required := range []string{"Do not fetch", "do not push", "private checkout", "exact local test commit ID"} {
		if !strings.Contains(authoring, required) {
			t.Fatalf("acceptance authoring instructions omit %q: %q", required, authoring)
		}
	}
	review := implementationReviewInstructions(
		workspace.Workspace{}, "summary",
		strings.Repeat("b", 40), "attempt", "", false, strings.Repeat("c", 40),
	)
	for _, required := range []string{"Reset this disposable private checkout", "merge that exact commit locally", "acceptance start <test-id>", "acceptance not-applicable <test-id> <note>", "Never push your private test commit"} {
		if !strings.Contains(review, required) {
			t.Fatalf("acceptance review instructions omit %q: %q", required, review)
		}
	}
}

func TestAcceptanceResultsMustFinishBeforeReviewCanAdvance(t *testing.T) {
	commitID := strings.Repeat("b", 40)
	tests := featureartifact.AcceptanceTests{
		PlanVersion: 1, TestCommitID: strings.Repeat("a", 40), ImplementationCommitID: commitID,
		Tests: []featureartifact.AcceptanceTest{{
			ID: "exports-csv", Position: 1, Title: "Exports visible rows", Status: featureartifact.AcceptanceTestPending,
		}},
	}
	if err := validateAcceptanceReviewResults(tests, 1, commitID, workerhttp.DispositionSucceeded); err == nil {
		t.Fatal("pending acceptance test allowed an approved review")
	}
	tests.Tests[0].Status = featureartifact.AcceptanceTestFailed
	tests.Tests[0].Note = "hidden rows were exported"
	if err := validateAcceptanceReviewResults(tests, 1, commitID, workerhttp.DispositionSucceeded); err == nil {
		t.Fatal("failed acceptance test allowed an approved review")
	}
	if err := validateAcceptanceReviewResults(tests, 1, commitID, workerhttp.DispositionChangesRequested); err != nil {
		t.Fatalf("failed acceptance test did not support requested changes: %v", err)
	}
	tests.Tests[0].Status = featureartifact.AcceptanceTestNotApplicable
	if err := validateAcceptanceReviewResults(tests, 1, commitID, workerhttp.DispositionSucceeded); err != nil {
		t.Fatalf("justified not-applicable test blocked approval: %v", err)
	}
}

func TestDurableAcceptanceTestsReconcileIncompleteProviderResult(t *testing.T) {
	testCommitID := strings.Repeat("a", 40)
	tests := featureartifact.AcceptanceTests{
		PlanVersion:  1,
		TestCommitID: testCommitID,
		Tests: []featureartifact.AcceptanceTest{{
			ID: "exports-csv", Position: 1, Title: "Exports visible rows",
			Status: featureartifact.AcceptanceTestPending,
		}},
	}
	document, err := json.Marshal(tests)
	if err != nil {
		t.Fatalf("encode acceptance tests: %v", err)
	}
	starter := &RemoteLeadStarter{artifacts: recoveryArtifactStub{artifact: workflow.FeatureArtifact{
		FeatureID: "fea_recovery", Kind: featureartifact.KindAcceptanceTests,
		Revision: 1, Document: string(document),
		Actor:     workflow.Actor{Kind: workflow.ActorKindAgent, ID: "reviewer"},
		UpdatedAt: time.Now().UTC(),
	}}}
	reviewer := execution.Session{ID: "run_recovery:reviewer", ProviderSessionID: "claude_session"}
	checkpoint := execution.WorkerAttemptCheckpoint{
		SessionID: reviewer.ID, AttemptID: reviewer.ID + ":acceptance:1:turn:1", LastEventSequence: 25,
	}
	attempt := workerhttp.Attempt{
		AttemptReference:  workerhttp.AttemptReference{SessionID: reviewer.ID, AttemptID: checkpoint.AttemptID},
		ProviderSessionID: reviewer.ProviderSessionID, State: workerhttp.AttemptStateTerminal,
		LatestEventSequence: checkpoint.LastEventSequence,
		Result: &workerhttp.TerminalResult{
			Outcome: workerhttp.OutcomeFailed,
			Summary: "provider process ended without a structured result",
			Error:   &workerhttp.ProtocolError{Code: workerhttp.ErrorIncompleteResult, Message: "missing structured_output", Retryable: true},
		},
	}
	got, found, err := starter.durableAcceptanceTests(
		t.Context(), execution.Run{FeatureID: "fea_recovery", PlanVersion: 1}, reviewer, checkpoint, attempt,
	)
	if err != nil || !found || got.TestCommitID != testCommitID {
		t.Fatalf("reconciled acceptance tests=%+v found=%t error=%v", got, found, err)
	}

	starter.artifacts = recoveryArtifactStub{err: workflow.ErrArtifactNotFound}
	if _, found, err := starter.durableAcceptanceTests(
		t.Context(), execution.Run{FeatureID: "fea_recovery", PlanVersion: 1}, reviewer, checkpoint, attempt,
	); err != nil || found {
		t.Fatalf("private commit without manifest found=%t error=%v", found, err)
	}
}

func TestRecoverySuccessorPreservesWorkflowLaneAndClassifiesResumeFailure(t *testing.T) {
	sessionID := "run_recovery:reviewer"
	first := acceptanceTestsAttemptForVersion(sessionID, 2)
	second, err := recoverySuccessorAttemptID(sessionID, first)
	if err != nil || second != workflowAttemptForVersion(sessionID, "acceptance", 2, 2) {
		t.Fatalf("acceptance successor=%q error=%v", second, err)
	}
	third, err := recoverySuccessorAttemptID(sessionID, second)
	if err != nil || third != workflowAttemptForVersion(sessionID, "acceptance", 2, 3) {
		t.Fatalf("fresh fallback successor=%q error=%v", third, err)
	}
	failed := workerhttp.Attempt{State: workerhttp.AttemptStateTerminal, Result: &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeFailed,
		Error:   &workerhttp.ProtocolError{Code: workerhttp.ErrorProviderSessionMissing, Message: "conversation missing", Retryable: true},
	}}
	if !resumeLaunchUnavailable(failed) {
		t.Fatal("missing resumable conversation did not permit a fresh fallback")
	}
	failed.Result.Error.Retryable = false
	if resumeLaunchUnavailable(failed) {
		t.Fatal("non-retryable launch failure permitted a fresh fallback")
	}
}

func TestRecoveryPublicationInstructionsUseSuccessorIdentity(t *testing.T) {
	lead := "run_recovery:lead"
	reviewer := "run_recovery:reviewer"
	tests := []struct {
		name      string
		sessionID string
		attemptID string
		marker    string
		heading   string
	}{
		{
			name: "implementation", sessionID: lead,
			attemptID: implementationAttemptForVersion(lead, 1, 3),
			marker: workspace.ImplementationPublicationInitial.Marker(
				implementationAttemptForVersion(lead, 1, 3),
			),
			heading: "## Implementation summary",
		},
		{
			name: "correction", sessionID: lead,
			attemptID: implementationCorrectionAttemptForVersion(lead, 2, 4),
			marker: workspace.ImplementationPublicationReviewResponse.Marker(
				implementationCorrectionAttemptForVersion(lead, 2, 4),
			),
			heading: "## Review response",
		},
		{
			name: "readiness", sessionID: lead,
			attemptID: implementationReadinessAttemptForVersion(lead, 2, 4),
			marker: workspace.ImplementationPublicationMergeReadiness.Marker(
				implementationReadinessAttemptForVersion(lead, 2, 4),
			),
			heading: "## Merge readiness",
		},
		{
			name: "update", sessionID: lead,
			attemptID: baseUpdateAttemptForVersion(lead, 2, 3),
			marker: workspace.ImplementationPublicationBaseUpdate.Marker(
				baseUpdateAttemptForVersion(lead, 2, 3),
			),
			heading: "## Base update",
		},
		{
			name: "review", sessionID: reviewer,
			attemptID: implementationReviewAttemptForVersion(reviewer, 2, 4),
			marker: implementationReviewMarker(
				implementationReviewAttemptForVersion(reviewer, 2, 4),
			),
			heading: "## Review",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instructions := recoveryPublicationInstructions(test.sessionID, test.attemptID)
			if !strings.Contains(instructions, test.marker) ||
				!strings.Contains(instructions, test.heading) ||
				!strings.Contains(instructions, "rather than any marker from an earlier attempt") {
				t.Fatalf("unexpected recovery publication instructions %q", instructions)
			}
		})
	}
	if instructions := recoveryPublicationInstructions(lead, planningAttemptID(lead)); instructions != "" {
		t.Fatalf("planning recovery unexpectedly received publication instructions %q", instructions)
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

func TestReviewerMustApproveTheFinalPlanBeforeImplementation(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	storedProject, err := database.NewProjectStore(db).UpdateAutonomyPolicy(
		t.Context(), storedProject.ID, project.AutonomyPolicyRunToCompletion,
	)
	if err != nil {
		t.Fatalf("enable run-to-completion: %v", err)
	}
	runID := "run_plan_approval"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	stub.planChangeRequests = 1
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedReviewerPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningCorrectionAttempts(stub, runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningCorrectionAttempt(
		stub, remoteLeadSessionID(runID), storedProject.ID, storedFeature.ID,
		workerhttp.RoleLead, "codex-thread-test", 4,
		workerhttp.EventPlanSubmitted, "Complete final implementation plan with the failure-path test",
	)
	addCompletedImplementationAttempt(stub, runID, storedProject.ID, storedFeature.ID)

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.September, 12, 11, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_plan_approval", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "approval-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_plan_approval",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:       &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Validation: validation.NewService(database.NewValidationStore(db)),
		Lifetime:   t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start goal clarification: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && workspaceStub.publicationVerifyCalls == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if workspaceStub.publishCalls != 1 ||
		workspaceStub.publishedPlan != "Complete final implementation plan with the failure-path test" {
		t.Fatalf("only the approved resubmission should be published: %+v", workspaceStub)
	}
	messages, err := executions.PlanningMessagesForRun(t.Context(), runID)
	if err != nil || len(messages) != 7 ||
		messages[5].Role != worker.RoleReviewer ||
		messages[5].Event.Text != "The final plan dropped the failure-path test we agreed on." ||
		messages[6].Event.Type != worker.EventPlanSubmitted {
		t.Fatalf("requested changes did not return the plan to the lead: %+v err=%v", messages, err)
	}
	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	approvals := 0
	for index, request := range requests {
		if request.OutputContract != workerhttp.OutputContractPlanApproval {
			continue
		}
		approvals++
		if index+1 >= len(requests) {
			t.Fatalf("plan approval %d was the last request", approvals)
		}
		if approvals == 1 && requests[index+1].OutputContract != workerhttp.OutputContractPlanningLead {
			t.Fatalf("requested changes did not go back to the lead: %+v", requests[index+1])
		}
		if approvals == 2 && requests[index+1].OutputContract != workerhttp.OutputContractImplementationLead {
			t.Fatalf("approval did not start implementation: %+v", requests[index+1])
		}
	}
	if approvals != 2 {
		t.Fatalf("expected two plan approvals, got %d", approvals)
	}
}

// readyRemoteLeadFeature gives a draft work order a handoff brief and makes
// it Ready, as the project assistant and the user do before Start.
func readyRemoteLeadFeature(t *testing.T, workflowService *workflow.Service, featureID string) {
	t.Helper()
	if _, err := workflowService.PutHandoffBrief(
		t.Context(), featureID, 0,
		featureartifact.HandoffBrief{
			Goal:  "Export the visible report columns as CSV.",
			Areas: []string{"report export"}, Considerations: []string{}, OpenQuestions: []string{},
		},
		workflow.Actor{Kind: workflow.ActorKindAgent, ID: "ast_test"}, featureID+":brief",
	); err != nil {
		t.Fatalf("write handoff brief: %v", err)
	}
	if _, err := workflowService.TransitionFeature(
		t.Context(), featureID, feature.StateReady,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, featureID+":ready",
	); err != nil {
		t.Fatalf("make work order ready: %v", err)
	}
}

func TestStartRequiresAReadyWorkOrder(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: &remoteLeadWorkspaceStub{},
		Worker: unavailableRemoteLeadWorker{}, Pump: &unexpectedRemoteLeadPump{},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	if _, _, err := starter.Start(
		t.Context(), "run_draft", storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders, storedProject.MergePolicy,
	); !errors.Is(err, ErrFeatureNotReady) {
		t.Fatalf("start a draft: %v", err)
	}
	if _, err := executions.GetRun(t.Context(), "run_draft"); !errors.Is(err, execution.ErrNotFound) {
		t.Fatalf("a draft start created a run: %v", err)
	}
}

// copyScriptedAttempt scripts the same attempt under another identity.
func copyScriptedAttempt(
	stub *conversationalRemoteLeadWorker,
	from workerhttp.AttemptReference,
	to workerhttp.AttemptReference,
) {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	initial, terminal := stub.initial[from], stub.terminal[from]
	initial.AttemptReference, terminal.AttemptReference = to, to
	events := make([]workerhttp.Event, 0, len(stub.events[from]))
	for _, event := range stub.events[from] {
		event.AttemptReference = to
		events = append(events, event)
	}
	stub.initial[to], stub.terminal[to], stub.events[to] = initial, terminal, events
}

// startFailingPlanningRun starts a run whose first planning turn fails with
// the given error; its successor turn completes.
func startFailingPlanningRun(
	t *testing.T,
	runID string,
	code workerhttp.ErrorCode,
) (*RemoteLeadStarter, *execution.Service, *conversationalRemoteLeadWorker) {
	t.Helper()
	return startFailingPlanningRunWithSteps(t, runID, code, project.WorkSteps{})
}

// startFailingPlanningRunWithSteps is startFailingPlanningRun for a work
// order with the given steps.
func startFailingPlanningRunWithSteps(
	t *testing.T,
	runID string,
	code workerhttp.ErrorCode,
	steps project.WorkSteps,
) (*RemoteLeadStarter, *execution.Service, *conversationalRemoteLeadWorker) {
	t.Helper()
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedPlanningAttempt(stub, runID, storedProject.ID, storedFeature.ID)
	leadID := remoteLeadSessionID(runID)
	failedRef := workerhttp.AttemptReference{SessionID: leadID, AttemptID: planningAttemptID(leadID)}
	successorID, err := recoverySuccessorAttemptID(leadID, failedRef.AttemptID)
	if err != nil {
		t.Fatalf("successor identity: %v", err)
	}
	copyScriptedAttempt(stub, failedRef, workerhttp.AttemptReference{SessionID: leadID, AttemptID: successorID})
	stub.mu.Lock()
	failed := stub.terminal[failedRef]
	failed.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeFailed,
		Summary: "Failed to authenticate: OAuth session expired",
		Error:   &workerhttp.ProtocolError{Code: code, Message: "Failed to authenticate: OAuth session expired"},
	}
	stub.terminal[failedRef] = failed
	stub.mu.Unlock()

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.September, 9, 22, 0, 0, 0, time.UTC)
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
		Workspaces: &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
			ID: "wsp_managed_feature", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
			RepositoryOwner: "commitarium", RepositoryName: "planning-test",
			BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
			BaseCommitID: "0123456789abcdef0123456789abcdef01234567",
			Status:       workspace.StatusBranchReady, CheckoutRelativePath: "wsp_managed_feature",
			CheckoutCreatedAt: &checkoutAt,
		}},
	})
	if err != nil {
		t.Fatalf("create remote starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if steps != (project.WorkSteps{}) {
		features := database.NewFeatureStore(db)
		chosen, err := features.GetByID(t.Context(), storedFeature.ID)
		if err != nil {
			t.Fatalf("load ready feature: %v", err)
		}
		chosen.Steps = steps
		if err := features.UpdateReadySettings(t.Context(), chosen); err != nil {
			t.Fatalf("choose steps: %v", err)
		}
	}
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders, storedProject.MergePolicy,
	); err != nil {
		t.Fatalf("start lead: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	return starter, executions, stub
}

func waitForRetriedTurn(t *testing.T, executions *execution.Service, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, err := executions.GetRun(t.Context(), runID)
		if err == nil && current.Status == execution.RunStatusWaitingForUser &&
			current.WaitKind != execution.RunWaitKindBlocker {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("retried turn did not complete: %+v err=%v", current, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRemoteFailedTurnPausesAndContinueRetriesItInTheSameConversation(t *testing.T) {
	runID := "run_remote_failed_turn"
	starter, executions, stub := startFailingPlanningRun(t, runID, workerhttp.ErrorInternal)
	paused, err := executions.GetRun(t.Context(), runID)
	if err != nil || paused.WaitKind != execution.RunWaitKindBlocker ||
		!strings.Contains(paused.Reason, "OAuth session expired") {
		t.Fatalf("failed turn did not pause the run as a blocker: %+v err=%v", paused, err)
	}
	lead, err := executions.GetSession(t.Context(), remoteLeadSessionID(runID))
	if err != nil || lead.Status.IsTerminal() {
		t.Fatalf("failed turn ended its session: %+v err=%v", lead, err)
	}
	if wait, err := executions.LoginWaitForRun(t.Context(), runID); err != nil || wait.AgentID != "" {
		t.Fatalf("an ordinary failure recorded a login wait: %+v err=%v", wait, err)
	}

	if _, admitted, err := starter.RecoverBlockerWithDirective(
		t.Context(), runID, "continue-after-failure", RecoveryDirective{Action: RecoveryActionContinue},
	); err != nil || !admitted {
		t.Fatalf("continue failed turn: admitted=%t err=%v", admitted, err)
	}
	waitForRetriedTurn(t, executions, runID)
	stub.mu.Lock()
	defer stub.mu.Unlock()
	retry := stub.putRequests[len(stub.putRequests)-1]
	if retry.Mode != workerhttp.AttemptModeResume || retry.ProviderSessionID != "codex-thread-test" ||
		!strings.Contains(retry.Instructions, "OAuth session expired") {
		t.Fatalf("retry did not resume the same conversation with the failure context: %+v", retry)
	}
}

// An expired login retries like an interruption first, since a refresh that
// failed once can work on the next turn; only then does the run wait for the
// agent's sign-in, and a sign-in resumes it.
func TestRemoteExpiredLoginWaitsForTheAgentAndResumesAfterLogin(t *testing.T) {
	const expired = "Failed to authenticate: OAuth session expired"
	run := startInterruptedPlanningRun(t, "run_remote_expired_login", func(run *interruptedRun) {
		// The turn after the sign-in completes the planning.
		leadID := remoteLeadSessionID(run.runID)
		afterLogin, err := recoverySuccessorAttemptID(leadID, run.attempts[len(run.attempts)-1].AttemptID)
		if err != nil {
			t.Fatalf("successor identity: %v", err)
		}
		copyScriptedAttempt(run.stub, run.attempts[0], workerhttp.AttemptReference{SessionID: leadID, AttemptID: afterLogin})
		for _, attempt := range run.attempts {
			run.fail(attempt, workerhttp.ErrorProviderLoginRequired, expired)
		}
	})
	waitForRunReason(t, run.executions, run.runID, "did not accept the agent's login")
	if wait, err := run.executions.LoginWaitForRun(t.Context(), run.runID); err != nil || wait.AgentID != "" {
		t.Fatalf("a login failure waited for a sign-in before retrying: %+v err=%v", wait, err)
	}
	for retry := 1; retry <= len(retryBackoff); retry++ {
		if resumed, err := run.starter.ResumeDueRetries(t.Context(), later()); err != nil || resumed != 1 {
			t.Fatalf("retry %d: %d err=%v", retry, resumed, err)
		}
		if retry < len(retryBackoff) {
			waitForRunReason(t, run.executions, run.runID, fmt.Sprintf("(%d of 3)", retry+1))
		}
	}
	paused := waitForRunReason(t, run.executions, run.runID, "must log in again")
	if paused.WaitKind != execution.RunWaitKindBlocker {
		t.Fatalf("expired login did not pause for login: %+v", paused)
	}
	wait, err := run.executions.LoginWaitForRun(t.Context(), run.runID)
	if err != nil || wait.AgentID != "codex" || wait.Role != worker.RoleLead {
		t.Fatalf("login wait = %+v err=%v", wait, err)
	}
	if signIns, err := run.executions.AgentSignIns(t.Context()); err != nil || len(signIns) != 1 ||
		signIns[0].AgentID != "codex" || signIns[0].LastError == "" {
		t.Fatalf("the agent was not marked as needing a sign-in: %+v err=%v", signIns, err)
	}
	if resumed, err := run.starter.ResumeAfterLogin(t.Context(), "claude"); err != nil || resumed != 0 {
		t.Fatalf("another agent's login resumed the run: %d err=%v", resumed, err)
	}
	if resumed, err := run.starter.ResumeAfterLogin(t.Context(), "codex"); err != nil || resumed != 1 {
		t.Fatalf("resume after login: %d err=%v", resumed, err)
	}
	waitForRetriedTurn(t, run.executions, run.runID)
	if wait, err := run.executions.LoginWaitForRun(t.Context(), run.runID); err != nil || wait.AgentID != "" {
		t.Fatalf("login wait was not cleared: %+v err=%v", wait, err)
	}
	if signIns, err := run.executions.AgentSignIns(t.Context()); err != nil || len(signIns) != 0 {
		t.Fatalf("the sign-in was not cleared: %+v err=%v", signIns, err)
	}
}

func TestValidationPassedClearsThePendingReason(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	runID := "run_validation_passed"
	if _, _, err := executions.CreateRunWithModels(
		t.Context(), runID, storedFeature.ID, storedProject.DialogueLimits.PlanningRounds,
		storedProject.DialogueLimits.ImplementationReviewRounds, storedProject.AgentProviders,
		project.AgentModels{}, storedProject.MergePolicy, project.DefaultAutonomyPolicy(),
	); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := executions.TransitionRun(
		t.Context(), runID, execution.RunStatusRunning, execution.RunStatusWaitingForUser,
		validationPendingReason, execution.RunWaitKindMergeGate,
	); err != nil {
		t.Fatalf("wait for validation: %v", err)
	}
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: database.NewFeatureStore(db), Goals: workflowService,
		Workspaces: &remoteLeadWorkspaceStub{}, Worker: unavailableRemoteLeadWorker{},
		Pump: &unexpectedRemoteLeadPump{}, Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	if err := starter.ValidationPassed(t.Context(), runID); err != nil {
		t.Fatalf("validation passed: %v", err)
	}
	run, err := executions.GetRun(t.Context(), runID)
	if err != nil || run.Reason != validationPassedReason || run.WaitKind != execution.RunWaitKindMergeGate {
		t.Fatalf("run still reports pending validation: %+v err=%v", run, err)
	}
}

// addCompletedSessionTurn scripts one session turn that replies and, for the
// first turn, names the session.
func addCompletedSessionTurn(
	stub *conversationalRemoteLeadWorker,
	runID string,
	projectID string,
	featureID string,
	turn int,
	reply string,
	title string,
) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{SessionID: sessionID, AttemptID: sessionTurnAttemptID(sessionID, turn)}
	now := time.Date(2026, time.October, 11, 18, turn, 0, 0, time.UTC)
	mode := workerhttp.AttemptModeResume
	if turn == 1 {
		mode = workerhttp.AttemptModeStart
	}
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: mode,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleLead, WorkspaceID: "wsp_session",
		},
		ProviderSessionID: "codex-session-thread", State: workerhttp.AttemptStateRunning,
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
		Summary: reply, SessionTitle: title,
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage, Text: reply, OccurredAt: now},
		{AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal, Text: reply, OccurredAt: endedAt},
	}
}

// addCompletedSessionFinish scripts the finish turn that publishes the
// session's commit.
func addCompletedSessionFinish(stub *conversationalRemoteLeadWorker, runID, projectID, featureID string, turn int, commitID string) {
	sessionID := remoteLeadSessionID(runID)
	reference := workerhttp.AttemptReference{SessionID: sessionID, AttemptID: implementationAttemptForVersion(sessionID, 1, turn)}
	now := time.Date(2026, time.October, 11, 19, turn, 0, 0, time.UTC)
	initial := workerhttp.Attempt{
		AttemptReference: reference, Mode: workerhttp.AttemptModeResume,
		Assignment: workerhttp.Assignment{
			AgentProfileID: "codex", ProjectID: projectID, FeatureID: featureID,
			Role: workerhttp.RoleLead, WorkspaceID: "wsp_session",
		},
		ProviderSessionID: "codex-session-thread", State: workerhttp.AttemptStateRunning,
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
		Summary:     "Fixed the Icelandic copy on the front page and footer.",
		Publication: &workerhttp.ImplementationPublication{CommitID: commitID, PullRequestNumber: 7},
	}
	stub.initial[reference] = initial
	stub.terminal[reference] = terminal
	stub.events[reference] = []workerhttp.Event{
		{AttemptReference: reference, Sequence: 1, Type: workerhttp.EventMessage, Text: terminal.Result.Summary, OccurredAt: now},
		{AttemptReference: reference, Sequence: 2, Type: workerhttp.EventAttemptTerminal, Text: terminal.Result.Summary, OccurredAt: endedAt},
	}
}

func TestRemoteLeadRunsASessionMessageByMessage(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	if _, err := db.ExecContext(t.Context(),
		`UPDATE features SET kind = 'session', title = 'Untitled session 1', skip_plan = 1, skip_reviewer = 1 WHERE id = ?`,
		storedFeature.ID,
	); err != nil {
		t.Fatalf("make the work order a session: %v", err)
	}
	runID := "run_session"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	addCompletedSessionTurn(stub, runID, storedProject.ID, storedFeature.ID, 1, "Sure. Which page first?", "Fix the Icelandic copy")
	addCompletedSessionTurn(stub, runID, storedProject.ID, storedFeature.ID, 2, "Updated the front page.", "")
	addCompletedSessionFinish(stub, runID, storedProject.ID, storedFeature.ID, 3, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	addCompletedSessionTurn(stub, runID, storedProject.ID, storedFeature.ID, 4, "Changed the heading too.", "")
	addCompletedSessionFinish(stub, runID, storedProject.ID, storedFeature.ID, 5, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.October, 11, 17, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_session", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "session-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID:         "0123456789abcdef0123456789abcdef01234567",
		Status:               workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_session",
		CheckoutCreatedAt:    &checkoutAt,
	}}
	features := database.NewFeatureStore(db)
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: features, Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	opened, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	)
	if err != nil {
		t.Fatalf("start the session: %v", err)
	}
	if opened.Status != execution.RunStatusWaitingForUser || opened.WaitKind != execution.RunWaitKindClarification ||
		opened.Reason != sessionYourTurnReason {
		t.Fatalf("the session did not wait for the first message: %+v", opened)
	}
	if current, _ := features.GetByID(t.Context(), storedFeature.ID); current.State != feature.StateImplementing {
		t.Fatalf("session state = %q", current.State)
	}
	if workspaceStub.publishCalls != 1 || workspaceStub.publishedPlan != sessionPlanRecord {
		t.Fatalf("the session did not open its pull request: %+v", workspaceStub)
	}
	stub.mu.Lock()
	started := len(stub.putRequests)
	stub.mu.Unlock()
	if started != 0 {
		t.Fatalf("a turn ran before the first message: %d", started)
	}

	send := func(id, message string) {
		t.Helper()
		if _, err := starter.SendSessionMessage(t.Context(), runID, worker.Command{
			ID: id, Type: worker.CommandMessage, Message: message,
		}); err != nil {
			t.Fatalf("send %q: %v", message, err)
		}
		waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	}
	send("msg-1", "Fix the Icelandic wording on the front page.")
	if renamed, _ := features.GetByID(t.Context(), storedFeature.ID); renamed.Title != "Fix the Icelandic copy" {
		t.Fatalf("the first turn did not name the session: %q", renamed.Title)
	}
	workspaceStub.mainHead = "9999999999999999999999999999999999999999"
	send("msg-2", "Now the footer.")
	// A retried message does not start another turn.
	if _, err := starter.SendSessionMessage(t.Context(), runID, worker.Command{
		ID: "msg-2", Type: worker.CommandMessage, Message: "Now the footer.",
	}); err != nil {
		t.Fatalf("retry the second message: %v", err)
	}

	stub.mu.Lock()
	requests := append([]workerhttp.PutAttemptRequest(nil), stub.putRequests...)
	stub.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected two session turns, got %d", len(requests))
	}
	if requests[0].Mode != workerhttp.AttemptModeStart || requests[0].OutputContract != workerhttp.OutputContractSessionOpening ||
		!strings.Contains(requests[0].Instructions, "interactive Commitarium session") ||
		!strings.HasSuffix(requests[0].Instructions, "Fix the Icelandic wording on the front page.") {
		t.Fatalf("unexpected first session turn: %+v", requests[0])
	}
	if requests[1].Mode != workerhttp.AttemptModeResume || requests[1].OutputContract != "" ||
		requests[1].ProviderSessionID != "codex-session-thread" ||
		!strings.Contains(requests[1].Instructions, "has moved to 9999999999999999999999999999999999999999") ||
		!strings.HasSuffix(requests[1].Instructions, "The user's message:\nNow the footer.") {
		t.Fatalf("unexpected later session turn: %+v", requests[1])
	}
	run, err := executions.GetRun(t.Context(), runID)
	if err != nil || run.Reason != sessionYourTurnReason || run.WaitKind != execution.RunWaitKindClarification {
		t.Fatalf("the session is not waiting for the user: %+v err=%v", run, err)
	}

	workspaceStub.mainHead = ""
	finish := func(key string) {
		t.Helper()
		if _, err := starter.FinishSession(t.Context(), runID, key); err != nil {
			t.Fatalf("finish the session: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			current, _ := executions.GetRun(t.Context(), runID)
			if current.Status == execution.RunStatusWaitingForUser && current.WaitKind == execution.RunWaitKindMergeGate {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("the finished session did not reach the merge gate")
	}
	finish("finish-1")
	if current, _ := features.GetByID(t.Context(), storedFeature.ID); current.State != feature.StateReadyToMerge {
		t.Fatalf("finished session state = %q", current.State)
	}
	stub.mu.Lock()
	finishRequest := stub.putRequests[len(stub.putRequests)-1]
	stub.mu.Unlock()
	if finishRequest.OutputContract != workerhttp.OutputContractImplementationLead ||
		!strings.Contains(finishRequest.Instructions, "The user has finished this session") {
		t.Fatalf("unexpected finish turn: %+v", finishRequest)
	}

	// Back to chatting before the merge, then finish again at a new commit.
	send("msg-3", "Also change the heading.")
	if current, _ := features.GetByID(t.Context(), storedFeature.ID); current.State != feature.StateImplementing ||
		workspaceStub.mergeReadyClears != 1 {
		t.Fatalf("the session did not go back to chatting: state=%q clears=%d", current.State, workspaceStub.mergeReadyClears)
	}
	finish("finish-2")
	if workspaceStub.prepared.ApprovedCommitID != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("the second finish pinned %q", workspaceStub.prepared.ApprovedCommitID)
	}
	if _, _, err := starter.Merge(t.Context(), runID, "merge-session"); err != nil {
		t.Fatalf("merge the session: %v", err)
	}
	if workspaceStub.mergeCalls != 1 || len(workspaceStub.mergeApprovalNotes) != 1 {
		t.Fatalf("the session did not merge with the merge gate's approval: %+v", workspaceStub)
	}
}

func TestRemoteLeadResendsASessionTurnThatNeverReachedItsWorker(t *testing.T) {
	db, executions, storedProject, storedFeature := newRemoteLeadExecution(t)
	if _, err := db.ExecContext(t.Context(),
		`UPDATE features SET kind = 'session', title = 'Untitled session 1', skip_plan = 1, skip_reviewer = 1 WHERE id = ?`,
		storedFeature.ID,
	); err != nil {
		t.Fatalf("make the work order a session: %v", err)
	}
	runID := "run_session_down"
	stub := newConversationalRemoteLeadWorker(runID, storedProject.ID, storedFeature.ID)
	workflowService := workflow.NewService(database.NewWorkflowStore(db))
	checkoutAt := time.Date(2026, time.October, 11, 17, 0, 0, 0, time.UTC)
	workspaceStub := &remoteLeadWorkspaceStub{prepared: workspace.Workspace{
		ID: "wsp_session", ProjectID: storedProject.ID, FeatureID: storedFeature.ID,
		RepositoryOwner: "commitarium", RepositoryName: "session-test",
		BaseBranch: "main", Branch: "commitarium/" + storedFeature.ID,
		BaseCommitID: "0123456789abcdef0123456789abcdef01234567", Status: workspace.StatusPreparing,
		CheckoutRelativePath: "wsp_session", CheckoutCreatedAt: &checkoutAt,
	}}
	features := database.NewFeatureStore(db)
	starter, err := NewRemoteLeadStarter(RemoteLeadConfig{
		Executions: executions, Features: features, Goals: workflowService,
		Planning: workflowService, Briefs: workflowService, Workspaces: workspaceStub, Worker: stub,
		Pump:     &conversationalRemoteLeadPump{executions: executions, worker: stub},
		Lifetime: t.Context(),
	})
	if err != nil {
		t.Fatalf("create starter: %v", err)
	}
	readyRemoteLeadFeature(t, workflowService, storedFeature.ID)
	if _, _, err := starter.Start(
		t.Context(), runID, storedProject.ID, storedFeature.ID, storedFeature.Title,
		storedProject.DialogueLimits, storedProject.AgentProviders,
		storedProject.MergePolicy, storedProject.AutonomyPolicy,
	); err != nil {
		t.Fatalf("start the session: %v", err)
	}
	// The worker is down: the first turn cannot reach it.
	if _, err := starter.SendSessionMessage(t.Context(), runID, worker.Command{
		ID: "msg-1", Type: worker.CommandMessage, Message: "Make the accent colour deep red.",
	}); err != nil {
		t.Fatalf("send the first message: %v", err)
	}
	waitForRemoteLeadStatus(t, executions, runID, execution.RunStatusWaitingForUser)
	if run, _ := executions.GetRun(t.Context(), runID); run.WaitKind != execution.RunWaitKindBlocker {
		t.Fatalf("an unreachable worker did not pause the session: %+v", run)
	}

	// The worker is back; Continue sends the same turn again.
	stub.mu.Lock()
	addCompletedSessionTurn(stub, runID, storedProject.ID, storedFeature.ID, 1, "Done: the accent is now deep red.", "Deep red accent")
	stub.unsent = map[workerhttp.AttemptReference]bool{{
		SessionID: remoteLeadSessionID(runID), AttemptID: sessionTurnAttemptID(remoteLeadSessionID(runID), 1),
	}: true}
	stub.mu.Unlock()
	if _, _, err := starter.RecoverBlockerWithDirective(t.Context(), runID, "continue-1", RecoveryDirective{Action: RecoveryActionContinue}); err != nil {
		t.Fatalf("continue the session: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := executions.GetRun(t.Context(), runID)
		if run.Status == execution.RunStatusWaitingForUser && run.WaitKind == execution.RunWaitKindClarification {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if run, _ := executions.GetRun(t.Context(), runID); run.WaitKind != execution.RunWaitKindClarification ||
		run.Reason != sessionYourTurnReason {
		t.Fatalf("the resent turn did not complete: %+v", run)
	}
	if renamed, _ := features.GetByID(t.Context(), storedFeature.ID); renamed.Title != "Deep red accent" {
		t.Fatalf("the resent first turn did not name the session: %q", renamed.Title)
	}
	stub.mu.Lock()
	last := stub.putRequests[len(stub.putRequests)-1]
	stub.mu.Unlock()
	if last.OutputContract != workerhttp.OutputContractSessionOpening ||
		!strings.HasSuffix(last.Instructions, "Make the accent colour deep red.") {
		t.Fatalf("the resent turn lost its message: %+v", last)
	}
}
