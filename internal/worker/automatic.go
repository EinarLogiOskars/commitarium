package worker

import (
	"context"
	"errors"
	"time"
)

// AutomaticScriptedAdapter makes a ScriptedAdapter progress without an
// external test driver. A delay keeps demo sessions observable and leaves a
// window in which the normal pause, message, and stop controls can be used.
type AutomaticScriptedAdapter struct {
	inner *ScriptedAdapter
	delay time.Duration
}

func NewAutomaticScriptedAdapter(
	inner *ScriptedAdapter,
	delay time.Duration,
) *AutomaticScriptedAdapter {
	return &AutomaticScriptedAdapter{inner: inner, delay: delay}
}

func (a *AutomaticScriptedAdapter) Start(
	ctx context.Context,
	request SessionRequest,
) (Session, error) {
	session, err := a.inner.Start(ctx, request)
	if err != nil {
		return nil, err
	}
	go a.advance(ctx, request.SessionID)
	return session, nil
}

func (a *AutomaticScriptedAdapter) Resume(
	ctx context.Context,
	request ResumeRequest,
) (Session, error) {
	session, err := a.inner.Resume(ctx, request)
	if err != nil {
		return nil, err
	}
	go a.advance(ctx, request.SessionID)
	return session, nil
}

func (a *AutomaticScriptedAdapter) advance(ctx context.Context, sessionID string) {
	for {
		if a.delay > 0 {
			timer := time.NewTimer(a.delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		err := a.inner.Advance(ctx, sessionID)
		if errors.Is(err, ErrSessionFinished) || err != nil {
			return
		}
	}
}
