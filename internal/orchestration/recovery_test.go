package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type recoveryDiscoveryStub struct {
	runs         []execution.Run
	sessions     []execution.Session
	transitioned execution.Run
	events       []worker.Event
}

func (s recoveryDiscoveryStub) RecoverableRuns(context.Context) ([]execution.Run, error) {
	return s.runs, nil
}

func (s recoveryDiscoveryStub) ActiveSessionsForRun(context.Context, string) ([]execution.Session, error) {
	return s.sessions, nil
}

func (s recoveryDiscoveryStub) TransitionRun(
	context.Context,
	string,
	execution.RunStatus,
	execution.RunStatus,
	string,
) (execution.Run, error) {
	return s.transitioned, nil
}

func (s recoveryDiscoveryStub) RecordSessionEventWithID(
	context.Context,
	string,
	string,
	worker.Event,
) (execution.Event, error) {
	return execution.Event{}, nil
}

type recoveryFeatureFinderStub struct{ stored feature.Feature }

func (s recoveryFeatureFinderStub) GetByID(context.Context, string) (feature.Feature, error) {
	return s.stored, nil
}

type recoveryProjectFinderStub struct{ stored project.Project }

func (s recoveryProjectFinderStub) GetByID(context.Context, string) (project.Project, error) {
	return s.stored, nil
}

type runRecovererStub struct {
	calls  int
	policy project.RecoveryPolicy
}

func (s *runRecovererStub) Recover(
	_ context.Context,
	_ execution.Run,
	_ feature.Feature,
	policy project.RecoveryPolicy,
) error {
	s.calls++
	s.policy = policy
	return nil
}

func TestRecovererAdmitsRunInterruptedBetweenSessions(t *testing.T) {
	runs := &runRecovererStub{}
	recoverer := NewRecoverer(
		recoveryDiscoveryStub{runs: []execution.Run{{ID: "run_test", FeatureID: "fea_test"}}},
		recoveryFeatureFinderStub{stored: feature.Feature{ID: "fea_test", ProjectID: "prj_test"}},
		recoveryProjectFinderStub{stored: project.Project{
			ID: "prj_test", RecoveryPolicy: project.RecoveryPolicyAutomatic,
		}},
		runs,
	)

	count, err := recoverer.RecoverAll(t.Context())
	if err != nil {
		t.Fatalf("recover runs: %v", err)
	}
	if count != 1 || runs.calls != 1 || runs.policy != project.RecoveryPolicyAutomatic {
		t.Fatalf("unexpected recovery count=%d calls=%d policy=%q", count, runs.calls, runs.policy)
	}
}

func TestRecovererRejectsContradictoryMultipleActiveSessions(t *testing.T) {
	runs := &runRecovererStub{}
	recoverer := NewRecoverer(
		recoveryDiscoveryStub{
			runs:     []execution.Run{{ID: "run_test", FeatureID: "fea_test"}},
			sessions: []execution.Session{{ID: "ses_one"}, {ID: "ses_two"}},
		},
		recoveryFeatureFinderStub{},
		recoveryProjectFinderStub{},
		runs,
	)

	count, err := recoverer.RecoverAll(t.Context())
	if err == nil || !strings.Contains(err.Error(), "expected at most one") {
		t.Fatalf("expected contradictory active-session error, got %v", err)
	}
	if count != 0 || runs.calls != 0 {
		t.Fatalf("contradictory run was admitted: count=%d calls=%d", count, runs.calls)
	}
}
