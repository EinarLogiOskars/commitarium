package execution

import (
	"context"
	"testing"
)

func TestRecoveryRunsOutWhenTheSameFailureComesBackAfterAFreshConversation(t *testing.T) {
	blocker := Blocker{RunID: "run_1", Kind: BlockerOther, Fingerprint: BlockerFingerprint(BlockerOther, "", "planning cannot start")}
	tried := []Recovery{
		{RunID: "run_1", Fingerprint: blocker.Fingerprint, How: RecoveryContinue},
		{RunID: "run_1", Fingerprint: blocker.Fingerprint, How: RecoveryFreshConversation},
	}
	if !RecoveryExhausted(blocker, tried) {
		t.Fatal("the same failure after a fresh conversation did not run out")
	}
	if RecoveryExhausted(blocker, tried[:1]) {
		t.Fatal("one continue ran out")
	}
	// A fresh conversation on another failure does not count against this one.
	other := []Recovery{{RunID: "run_1", Fingerprint: "other", How: RecoveryFreshConversation}}
	if RecoveryExhausted(blocker, other) {
		t.Fatal("a fresh conversation on another failure ran out")
	}
}

type blockerStoreStub struct {
	Store
	blockers   map[string]Blocker
	recoveries []Recovery
}

func (stub *blockerStoreStub) RecordRunBlocker(_ context.Context, blocker Blocker) error {
	stub.blockers[blocker.RunID] = blocker
	return nil
}

func (stub *blockerStoreStub) RunBlocker(_ context.Context, runID string) (Blocker, bool, error) {
	blocker, found := stub.blockers[runID]
	return blocker, found, nil
}

func (stub *blockerStoreStub) RecordRunRecovery(_ context.Context, recovery Recovery) error {
	stub.recoveries = append(stub.recoveries, recovery)
	return nil
}

func (stub *blockerStoreStub) RunRecoveries(context.Context, string) ([]Recovery, error) {
	return stub.recoveries, nil
}

// The service is what the workflow and the API are given, so blockers and
// recoveries reach the store through it.
func TestServiceKeepsBlockersInItsStore(t *testing.T) {
	store := &blockerStoreStub{blockers: map[string]Blocker{}}
	var service BlockerStore = NewService(store)
	if err := service.RecordRunBlocker(t.Context(), Blocker{RunID: "run_1", Kind: BlockerAgent, Fingerprint: "f"}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordRunRecovery(t.Context(), Recovery{RunID: "run_1", Fingerprint: "f", How: RecoveryContinue}); err != nil {
		t.Fatal(err)
	}
	blocker, found, err := service.RunBlocker(t.Context(), "run_1")
	recoveries, _ := service.RunRecoveries(t.Context(), "run_1")
	if err != nil || !found || blocker.Fingerprint != "f" || len(recoveries) != 1 {
		t.Fatalf("blocker=%+v found=%v err=%v recoveries=%v", blocker, found, err, recoveries)
	}
}
