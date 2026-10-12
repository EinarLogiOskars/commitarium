package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// An agent that reaches its provider usage limit pauses the turn instead of
// failing it, and the coordinator continues the run when the limit resets.

// limitRetryInterval is how often a run waiting on a limit with no known
// reset time tries again.
const limitRetryInterval = 15 * time.Minute

type remoteLeadLimitWaits interface {
	RecordLimitWait(ctx context.Context, runID, agentID string, role worker.Role, resetsAt *time.Time) error
	ClearLimitWait(ctx context.Context, runID string) error
	LimitWaits(ctx context.Context) ([]execution.LimitWait, error)
}

func usageLimitReason(agent string, resetsAt *time.Time) string {
	if resetsAt == nil {
		return "The agent " + agent + " reached its usage limit. The work order continues on its own once " +
			"the limit resets; Commitarium tries again every 15 minutes."
	}
	return "The agent " + agent + " reached its usage limit. The work order continues on its own when it " +
		"resets at " + resetsAt.UTC().Format(time.RFC3339) + "."
}

// limitWaitDue reports whether a run waiting on a limit should try again.
func limitWaitDue(wait execution.LimitWait, now time.Time) bool {
	if wait.ResetsAt != nil {
		// A minute's grace, so the provider has reset before the retry.
		return !now.Before(wait.ResetsAt.Add(time.Minute))
	}
	return !now.Before(wait.CreatedAt.Add(limitRetryInterval))
}

// ResumeDueLimits continues the runs whose agent's usage limit has reset. A
// limit still reached pauses the run again with a new wait.
func (starter *RemoteLeadStarter) ResumeDueLimits(ctx context.Context, now time.Time) (int, error) {
	waits, ok := starter.executions.(remoteLeadLimitWaits)
	if !ok {
		return 0, nil
	}
	pending, err := waits.LimitWaits(ctx)
	if err != nil {
		return 0, err
	}
	resumed := 0
	var errs []error
	for _, wait := range pending {
		if !limitWaitDue(wait, now) {
			continue
		}
		_, admitted, err := starter.RecoverBlockerWithDirective(
			ctx, wait.RunID, wait.RunID+":limit:"+strconv.FormatInt(now.UnixNano(), 10),
			RecoveryDirective{Action: RecoveryActionContinue},
		)
		switch {
		case errors.Is(err, ErrRecoveryNotAllowed):
			// The run moved on some other way; the wait no longer applies.
			if err := waits.ClearLimitWait(ctx, wait.RunID); err != nil {
				errs = append(errs, err)
			}
		case err != nil:
			errs = append(errs, fmt.Errorf("resume run %q after its usage limit: %w", wait.RunID, err))
		case admitted:
			resumed++
		}
	}
	return resumed, errors.Join(errs...)
}

// RunLimitResumer checks every minute for runs whose usage limit has reset,
// until ctx ends. Waits are durable, so it picks them up after a restart.
func (starter *RemoteLeadStarter) RunLimitResumer(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if _, err := starter.ResumeDueLimits(ctx, time.Now().UTC()); err != nil {
			starter.reportError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
