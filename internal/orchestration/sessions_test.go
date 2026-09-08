package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestActiveSessionsRegistersLooksUpAndRemovesSession(t *testing.T) {
	registry := NewActiveSessions()
	session := &stubWorkerSession{}
	remove, err := registry.Register("ses_test", session)
	if err != nil {
		t.Fatalf("register session: %v", err)
	}
	actual, found := registry.Get("ses_test")
	if !found || actual != session {
		t.Fatalf("expected active session %p, got %v, %t", session, actual, found)
	}
	if _, err := registry.Register("ses_test", &stubWorkerSession{}); !errors.Is(err, ErrSessionAlreadyActive) {
		t.Fatalf("expected error %v, got %v", ErrSessionAlreadyActive, err)
	}

	remove()
	remove()
	if _, found := registry.Get("ses_test"); found {
		t.Error("expected session to be removed")
	}
}

func TestActiveSessionsOldCleanupCannotRemoveNewRegistration(t *testing.T) {
	registry := NewActiveSessions()
	firstRemove, err := registry.Register("ses_test", &stubWorkerSession{})
	if err != nil {
		t.Fatalf("register first session: %v", err)
	}
	firstRemove()
	second := &stubWorkerSession{}
	secondRemove, err := registry.Register("ses_test", second)
	if err != nil {
		t.Fatalf("register second session: %v", err)
	}
	t.Cleanup(secondRemove)

	firstRemove()
	actual, found := registry.Get("ses_test")
	if !found || actual != second {
		t.Fatalf("expected newer session %p, got %v, %t", second, actual, found)
	}
}

type stubWorkerSession struct{}

func (*stubWorkerSession) Events() <-chan worker.Event {
	events := make(chan worker.Event)
	close(events)
	return events
}

func (*stubWorkerSession) Send(_ context.Context, _ worker.Command) error {
	return nil
}

func (*stubWorkerSession) Wait(_ context.Context) (worker.Result, error) {
	return worker.Result{}, nil
}
