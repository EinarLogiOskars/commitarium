package execution

import (
	"context"
	"errors"
	"testing"
)

type transitionStore struct {
	Store
	err error
}

func (store transitionStore) TransitionRun(context.Context, RunTransition) (Run, error) {
	return Run{ID: "run_1"}, store.err
}

func TestARunChangeWakesTheWatcher(t *testing.T) {
	for name, test := range map[string]struct {
		err  error
		want int
	}{"changed": {nil, 1}, "refused": {errors.New("conflict"), 0}} {
		service := NewService(transitionStore{err: test.err})
		heard := 0
		service.OnRunChange(func() { heard++ })
		_, _ = service.TransitionRun(t.Context(), "run_1", RunStatusRunning, RunStatusWaitingForUser, "Blocked.")
		if heard != test.want {
			t.Errorf("%s: heard %d changes, want %d", name, heard, test.want)
		}
	}
}
