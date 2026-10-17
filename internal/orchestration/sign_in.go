package orchestration

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
)

// An agent whose provider login fails past the automatic retries needs the
// user to sign in again (LOGIN_RECOVERY_PLAN.md). Everything that waits on
// it resumes when a sign-in finishes in the app, when any turn on the agent
// succeeds, or when the coordinator's own try of the waiting work does: a
// login fixed outside the app, or one that recovered by itself, resumes too.

// signInCheckEvery is how often the coordinator looks for work to try again.
const signInCheckEvery = time.Minute

// signInTryAfter is how long the coordinator waits between tries of the work
// waiting on an agent: often at first, rarely once the sign-in is overdue.
func signInTryAfter(waiting time.Duration) time.Duration {
	if waiting < time.Hour {
		return 5 * time.Minute
	}
	return 30 * time.Minute
}

type remoteLeadSignIns interface {
	MarkAgentNeedsSignIn(ctx context.Context, agentID, lastError string) error
	ClearAgentSignIn(ctx context.Context, agentID string) (bool, error)
	TouchAgentSignIn(ctx context.Context, agentID string) error
	AgentSignIns(ctx context.Context) ([]execution.AgentSignIn, error)
}

// agentNeedsSignIn reports whether the agent is known to need a sign-in: a
// login failure then waits at once instead of retrying.
func (starter *RemoteLeadStarter) agentNeedsSignIn(ctx context.Context, agentID string) bool {
	signIns, ok := starter.executions.(remoteLeadSignIns)
	if !ok || agentID == "" {
		return false
	}
	known, err := signIns.AgentSignIns(ctx)
	if err != nil {
		starter.reportError(err)
		return false
	}
	for _, signIn := range known {
		if signIn.AgentID == agentID {
			return true
		}
	}
	return false
}

// markNeedsSignIn records that the agent needs the user to sign in again.
func (starter *RemoteLeadStarter) markNeedsSignIn(ctx context.Context, agentID, cause string) {
	if signIns, ok := starter.executions.(remoteLeadSignIns); ok && agentID != "" {
		if err := signIns.MarkAgentNeedsSignIn(ctx, agentID, cause); err != nil {
			starter.reportError(err)
		}
	}
}

// AgentNeedsSignIn reports whether the agent is known to need a sign-in.
func (starter *RemoteLeadStarter) AgentNeedsSignIn(ctx context.Context, agentID string) bool {
	return starter.agentNeedsSignIn(ctx, agentID)
}

// MarkAgentNeedsSignIn records that the agent needs the user to sign in.
func (starter *RemoteLeadStarter) MarkAgentNeedsSignIn(ctx context.Context, agentID, cause string) error {
	starter.markNeedsSignIn(ctx, agentID, cause)
	return nil
}

// AgentAnswered notes a turn on the agent that worked, here or in the
// assistant: if the agent was waiting for a sign-in, its login works again,
// so everything waiting on it resumes.
func (starter *RemoteLeadStarter) AgentAnswered(ctx context.Context, agentID string) {
	starter.agentAnswered(ctx, agentID)
}

func (starter *RemoteLeadStarter) agentAnswered(ctx context.Context, agentID string) {
	if !starter.agentNeedsSignIn(ctx, agentID) {
		return
	}
	go func() {
		if _, err := starter.ResumeAfterLogin(starter.lifetime, agentID); err != nil {
			starter.reportError(err)
		}
	}()
}

// RunSignInChecker tries the work waiting on each agent that needs a sign-in
// now and then, until ctx ends: one run at a time, the longest waiting. If
// it works, agentAnswered resumes the rest; if not, it waits again at once.
func (starter *RemoteLeadStarter) RunSignInChecker(ctx context.Context) {
	ticker := time.NewTicker(signInCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := starter.TrySignedOutWork(ctx, time.Now().UTC()); err != nil {
			starter.reportError(err)
		}
	}
}

// TrySignedOutWork tries, for each agent that needs a sign-in and is due, the
// run that has waited longest on it. It returns how many runs it tried.
func (starter *RemoteLeadStarter) TrySignedOutWork(ctx context.Context, now time.Time) (int, error) {
	signIns, ok := starter.executions.(remoteLeadSignIns)
	waits, waitsOK := starter.executions.(RemoteLeadLoginWaits)
	if !ok || !waitsOK {
		return 0, nil
	}
	known, err := signIns.AgentSignIns(ctx)
	if err != nil {
		return 0, err
	}
	tried := 0
	var failures []error
	for _, signIn := range known {
		if now.Sub(signIn.LastTriedAt) < signInTryAfter(now.Sub(signIn.Since)) {
			continue
		}
		if err := signIns.TouchAgentSignIn(ctx, signIn.AgentID); err != nil {
			failures = append(failures, err)
			continue
		}
		runIDs, err := waits.RunsAwaitingLogin(ctx, signIn.AgentID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, runID := range runIDs {
			resumed, err := starter.resumeLoginWait(ctx, waits, runID, "try")
			if err != nil {
				failures = append(failures, err)
			}
			if resumed {
				tried++
				break
			}
		}
		if starter.signInTry != nil {
			starter.signInTry(signIn.AgentID)
		}
	}
	return tried, errors.Join(failures...)
}

// resumeLoginWait continues one run that waits for a sign-in. A run that
// moved on loses its wait; a paused one keeps it for when it is unpaused.
func (starter *RemoteLeadStarter) resumeLoginWait(
	ctx context.Context, waits RemoteLeadLoginWaits, runID, why string,
) (bool, error) {
	actionID := runID + ":login-" + why + ":" + strconv.FormatInt(time.Now().UnixNano(), 10)
	_, admitted, err := starter.RecoverBlockerWithDirective(
		ctx, runID, actionID, RecoveryDirective{Action: RecoveryActionContinue},
	)
	if errors.Is(err, ErrRecoveryNotAllowed) {
		if run, getErr := starter.executions.GetRun(ctx, runID); getErr == nil && run.Paused {
			return false, nil
		}
		return false, waits.ClearLoginWait(ctx, runID)
	}
	return admitted, err
}

// FollowSignIns lets others waiting on an agent's sign-in follow it: cleared
// runs when the agent's login works again, try when the coordinator tries
// the work waiting on it. Set it before the coordinator starts work.
func (starter *RemoteLeadStarter) FollowSignIns(cleared, try func(agentID string)) {
	starter.signInCleared, starter.signInTry = cleared, try
}
