package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

type interventionResumeExecutionStub struct {
	RemoteLeadExecution
	intervention execution.Intervention
	pauseCalled  bool
}

func (stub *interventionResumeExecutionStub) GetLatestIntervention(
	context.Context,
	string,
) (execution.Intervention, error) {
	return stub.intervention, nil
}

func (stub *interventionResumeExecutionStub) ApplyRunPause(
	context.Context,
	string,
	string,
	execution.RunPauseAction,
) (execution.Run, bool, error) {
	stub.pauseCalled = true
	return execution.Run{}, false, nil
}

func TestRemoteLeadResumeCannotBypassUnansweredIntervention(t *testing.T) {
	executions := &interventionResumeExecutionStub{intervention: execution.Intervention{
		ID: "int_pending", Status: execution.InterventionStatusQueued,
	}}
	starter := &RemoteLeadStarter{executions: executions}

	if _, _, err := starter.Resume(t.Context(), "run_test", "resume_test"); !errors.Is(err, ErrInterventionPending) {
		t.Fatalf("expected pending intervention error, got %v", err)
	}
	if executions.pauseCalled {
		t.Fatal("resume changed pause state before the intervention was answered")
	}
}
