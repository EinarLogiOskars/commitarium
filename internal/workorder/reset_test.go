package workorder

import (
	"context"
	"errors"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

type resetStoreStub struct {
	claimed  Deletion
	err      error
	finished bool
}

func (stub *resetStoreStub) BeginReturnToReady(context.Context, string, string) (Deletion, error) {
	return stub.claimed, stub.err
}

func (stub *resetStoreStub) FinishReturnToReady(context.Context, string, string) error {
	stub.finished = true
	return nil
}

type transitionStub struct{ states []feature.State }

func (stub *transitionStub) TransitionFeature(_ context.Context, _ string, state feature.State, _ workflow.Actor, _ string) (workflow.Event, error) {
	stub.states = append(stub.states, state)
	return workflow.Event{}, nil
}

func TestReturnToReadyStopsRunsRemovesArtifactsAndKeepsTheBrief(t *testing.T) {
	claimed := testDeletion(feature.StateImplementing)
	claimed.Feature.AcceptedGoal = "Export reports as CSV."
	reset := &resetStoreStub{claimed: claimed}
	forgejo := &forgejoCleanerStub{}
	checkouts := &checkoutCleanerStub{}
	runs := &runTerminatorStub{}
	transitions := &transitionStub{}
	service := NewServiceWithRunTerminator(&deletionStoreStub{}, forgejo, runs, checkouts).WithReset(reset, transitions)

	ready, err := service.ReturnToReady(t.Context(), claimed.Feature.ProjectID, claimed.Feature.ID, "key-1")
	if err != nil {
		t.Fatalf("return to Ready: %v", err)
	}
	if runs.featureID != claimed.Feature.ID || runs.key != "key-1" {
		t.Fatalf("runs were not stopped first: %+v", runs)
	}
	if forgejo.closed != 1 || forgejo.deleted != 1 || len(checkouts.removed) != 1 || !reset.finished {
		t.Fatalf("artifacts were not removed: forgejo=%+v checkouts=%+v finished=%t", forgejo, checkouts, reset.finished)
	}
	if len(transitions.states) != 1 || transitions.states[0] != feature.StateReady ||
		ready.State != feature.StateReady || ready.AcceptedGoal != "" {
		t.Fatalf("work order did not return to Ready: %+v transitions=%v", ready, transitions.states)
	}

	reset.err = ErrNotStarted
	if _, err := service.ReturnToReady(t.Context(), claimed.Feature.ProjectID, claimed.Feature.ID, "key-2"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("draft work order error = %v", err)
	}
	if _, err := NewService(&deletionStoreStub{}, forgejo).ReturnToReady(t.Context(), "p", "f", "k"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unconfigured reset error = %v", err)
	}
}
