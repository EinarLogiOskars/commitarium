package projectdeletion

import (
	"context"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type runExecutionStub struct {
	run           execution.Run
	session       execution.Session
	checkpoint    execution.WorkerAttemptCheckpoint
	checkpointErr error
}

func (stub *runExecutionStub) RunsForFeature(context.Context, string) ([]execution.Run, error) {
	return []execution.Run{stub.run}, nil
}
func (stub *runExecutionStub) SessionsForRun(context.Context, string) ([]execution.Session, error) {
	return []execution.Session{stub.session}, nil
}
func (stub *runExecutionStub) GetRun(context.Context, string) (execution.Run, error) {
	return stub.run, nil
}
func (stub *runExecutionStub) GetSession(context.Context, string) (execution.Session, error) {
	return stub.session, nil
}
func (stub *runExecutionStub) GetWorkerAttempt(context.Context, string) (execution.WorkerAttemptCheckpoint, error) {
	return stub.checkpoint, stub.checkpointErr
}

type emptyLiveSessionRegistryStub struct{}

func (emptyLiveSessionRegistryStub) Get(string) (worker.Session, bool) {
	return nil, false
}
func (stub *runExecutionStub) TransitionRun(_ context.Context, _ string, expected, status execution.RunStatus, reason string, _ ...execution.RunWaitKind) (execution.Run, error) {
	if stub.run.Status != expected {
		return execution.Run{}, execution.ErrStateConflict
	}
	stub.run.Status, stub.run.Reason = status, reason
	return stub.run, nil
}
func (stub *runExecutionStub) TransitionSession(_ context.Context, _ string, expected, status execution.SessionStatus, provider string) (execution.Session, error) {
	if stub.session.Status != expected {
		return execution.Session{}, execution.ErrStateConflict
	}
	stub.session.Status, stub.session.ProviderSessionID = status, provider
	return stub.session, nil
}

type workerStopperStub struct {
	attempt workerhttp.Attempt
	forces  int
	key     string
}

func (stub *workerStopperStub) GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	return stub.attempt, nil
}
func (stub *workerStopperStub) ForceStop(_ context.Context, identity workerhttp.MutationIdentity, _ workerhttp.ForceStopRequest) (workerhttp.Attempt, error) {
	stub.forces++
	stub.key = identity.IdempotencyKey
	stub.attempt.State = workerhttp.AttemptStateTerminal
	return stub.attempt, nil
}

func TestRunStopperForceStopsExactAttemptThenMakesDatabaseTerminal(t *testing.T) {
	now := time.Now().UTC()
	executions := &runExecutionStub{
		run: execution.Run{ID: "run_test", FeatureID: "fea_test", Status: execution.RunStatusRunning,
			StartedAt: now, UpdatedAt: now},
		session: execution.Session{ID: "run_test:lead", RunID: "run_test", AgentID: "codex-lead",
			Role: worker.RoleLead, Status: execution.SessionStatusRunning,
			ProviderSessionID: "provider_test", StartedAt: now, UpdatedAt: now},
		checkpoint: execution.WorkerAttemptCheckpoint{SessionID: "run_test:lead", AttemptID: "att_test"},
	}
	remote := &workerStopperStub{attempt: workerhttp.Attempt{
		AttemptReference: workerhttp.AttemptReference{SessionID: "run_test:lead", AttemptID: "att_test"},
		State:            workerhttp.AttemptStateRunning,
	}}
	if err := NewRunStopper(executions, remote, nil).StopFeatureRuns(t.Context(), "fea_test", "delete-1"); err != nil {
		t.Fatalf("stop feature runs: %v", err)
	}
	if remote.forces != 1 || remote.key == "" || executions.session.Status != execution.SessionStatusStopped ||
		executions.run.Status != execution.RunStatusStopped {
		t.Fatalf("unexpected stop remote=%+v session=%+v run=%+v", remote, executions.session, executions.run)
	}
}

func TestRunStopperStopsStrandedSessionAfterCoordinatorRestart(t *testing.T) {
	now := time.Now().UTC()
	executions := &runExecutionStub{
		run: execution.Run{ID: "run_test", FeatureID: "fea_test", Status: execution.RunStatusRunning,
			StartedAt: now, UpdatedAt: now},
		session: execution.Session{ID: "run_test:lead", RunID: "run_test", AgentID: "simulated-lead",
			Role: worker.RoleLead, Status: execution.SessionStatusRunning,
			StartedAt: now, UpdatedAt: now},
		checkpointErr: execution.ErrNotFound,
	}
	err := NewRunStopper(executions, nil, nil, emptyLiveSessionRegistryStub{}).
		StopFeatureRuns(t.Context(), "fea_test", "delete-1")
	if err != nil {
		t.Fatalf("stop stranded session: %v", err)
	}
	if executions.session.Status != execution.SessionStatusStopped ||
		executions.run.Status != execution.RunStatusStopped {
		t.Fatalf("stranded work remained active: session=%+v run=%+v", executions.session, executions.run)
	}
}
