package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

func TestExecutionStoreRotatesProviderSessionWithCompareAndSwap(t *testing.T) {
	db, store := newTestExecutionStore(t)
	_, session := createExecutionRecords(t, db, store)
	rotation := execution.ProviderSessionRotation{
		SessionID: session.ID, ExpectedProviderSessionID: session.ProviderSessionID,
		ProviderSessionID: "fresh_provider_session", OccurredAt: session.UpdatedAt.Add(time.Second),
	}
	rotated, err := store.RotateProviderSession(t.Context(), rotation)
	if err != nil || rotated.ProviderSessionID != rotation.ProviderSessionID ||
		rotated.Status != execution.SessionStatusRunning {
		t.Fatalf("rotate provider session: session=%+v error=%v", rotated, err)
	}
	if _, err := store.RotateProviderSession(t.Context(), rotation); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("stale provider-session rotation error=%v", err)
	}
	stored, err := store.GetSession(t.Context(), session.ID)
	if err != nil || stored.ProviderSessionID != rotation.ProviderSessionID {
		t.Fatalf("stored provider session=%+v error=%v", stored, err)
	}
}
