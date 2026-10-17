package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
)

// A turn that infrastructure cut off (the worker or the coordinator
// restarting, a lost connection to the worker or the provider's service) is
// retried by itself a few times, with backoff, before the work order blocks.
// A retry is the Continue the user would give: it continues the run's
// blocker, and the interrupted attempt's fenced successor resumes the same
// conversation (recoverySuccessorAttemptID). Real problems (a refused
// result, an agent's question, a usage limit, an expired login) block as
// before. Retries are not autopilot's continues, and do not ask for the user.

// retryBackoff is how long each automatic retry waits; once they are spent,
// the turn blocks.
var retryBackoff = [...]time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}

// AutomaticRetries is how many times an interrupted turn retries by itself.
const AutomaticRetries = len(retryBackoff)

// retryCheckEvery is how often the coordinator looks for a retry due.
const retryCheckEvery = 10 * time.Second

// errInterruptedAttempt is a worker attempt left indeterminate: the worker
// restarted while it ran.
var errInterruptedAttempt = errors.New("worker reports an indeterminate provider attempt")

type remoteLeadRetryWaits interface {
	RecordRetryWait(ctx context.Context, wait execution.RetryWait) error
	ClearRetryWait(ctx context.Context, runID string) error
	RetryWaitForRun(ctx context.Context, runID string) (execution.RetryWait, error)
	RetryWaits(ctx context.Context) ([]execution.RetryWait, error)
}

// remoteLeadWorkerReachability is implemented by workers that can say
// whether the worker for a session answers at all.
type remoteLeadWorkerReachability interface {
	Reachable(ctx context.Context, sessionID string) error
}

// retryTurn is an interrupted attempt's worker request, kept to send again
// should the worker never have admitted it.
type retryTurn struct {
	IdempotencyKey string                       `json:"idempotency_key"`
	Request        workerhttp.PutAttemptRequest `json:"request"`
}

// interruption says what cut a turn off when infrastructure did, or "".
func interruption(cause error) string {
	switch {
	case errors.Is(cause, errInterruptedAttempt), errors.Is(cause, workeringest.ErrAttemptIndeterminate):
		return "the worker restarted"
	case errors.Is(cause, workerhttp.ErrRequestFailed), errors.Is(cause, workeringest.ErrStreamEndedActive):
		return "the connection to the worker was lost"
	}
	return ""
}

// providerInterruption says when a failed turn's provider service or
// network failed it, rather than the work, or "".
func providerInterruption(result *workerhttp.TerminalResult) string {
	if result == nil || result.Outcome != workerhttp.OutcomeFailed || result.Error == nil ||
		result.Error.Code != workerhttp.ErrorInternal {
		return ""
	}
	if worker.IsTransientFailure(result.Error.Message) || worker.IsTransientFailure(result.Summary) {
		return "the provider's service failed"
	}
	return ""
}

// scheduleRetry records the next automatic retry of an interrupted turn and
// returns the run's reason while it waits. ok is false once the retries are
// spent, or cannot be kept; retries then says how many ran, and the caller
// blocks as before.
func (starter *RemoteLeadStarter) scheduleRetry(
	ctx context.Context, request remoteLeadRequest, cause string,
) (reason string, retries int, ok bool) {
	waits, supported := starter.executions.(remoteLeadRetryWaits)
	if !supported || ctx.Err() != nil {
		return "", 0, false
	}
	prior, err := waits.RetryWaitForRun(ctx, request.runID)
	if err != nil {
		starter.reportError(fmt.Errorf("load retry wait for run %q: %w", request.runID, err))
		return "", 0, false
	}
	retries = prior.Retries
	if retries >= len(retryBackoff) {
		starter.clearRetryWait(ctx, request.runID)
		return "", retries, false
	}
	turn := ""
	if request.request.Validate(request.identity) == nil {
		if encoded, err := json.Marshal(retryTurn{
			IdempotencyKey: request.identity.IdempotencyKey, Request: request.request,
		}); err == nil {
			turn = string(encoded)
		}
	}
	delay := retryBackoff[retries]
	dueAt := time.Now().UTC().Add(delay)
	if err := waits.RecordRetryWait(ctx, execution.RetryWait{
		RunID: request.runID, SessionID: request.identity.SessionID, AttemptID: request.identity.AttemptID,
		Retries: retries, Cause: cause, DueAt: &dueAt, Turn: turn,
	}); err != nil {
		starter.reportError(err)
		return "", retries, false
	}
	agent := request.agentName
	if agent == "" {
		agent = "agent"
	}
	return fmt.Sprintf("The %s's turn was interrupted (%s). Retrying automatically in %s (%d of %d).",
		agent, cause, delayWords(delay), retries+1, len(retryBackoff)), retries, true
}

// awaitRetry pauses the interrupted turn until its retry is due. The
// transcript says what happened; the run waits as a blocker, which the retry
// continues, but it does not ask for the user: the API shows it as retrying.
func (starter *RemoteLeadStarter) awaitRetry(ctx context.Context, request remoteLeadRequest, reason string, retry int) {
	_, _ = starter.executions.RecordSessionEventWithID(
		ctx, request.identity.AttemptID+":recovery:retry:"+strconv.Itoa(retry),
		request.identity.SessionID,
		worker.Event{
			Type: worker.EventRecoveryAssessment, Text: reason,
			RecoveryAssessment: &worker.RecoveryAssessment{Consistent: true},
		},
	)
	session, err := starter.executions.GetSession(ctx, request.identity.SessionID)
	if err == nil && session.Status == execution.SessionStatusRunning {
		_, _ = starter.executions.TransitionSession(
			ctx, session.ID, session.Status, execution.SessionStatusPauseRequested, session.ProviderSessionID,
		)
	}
	starter.noteBlocker(ctx, request.runID, execution.BlockerEnvironment, "", reason, reason)
	if err := starter.waitRun(ctx, request.runID, reason, execution.RunWaitKindBlocker); err != nil {
		starter.reportError(err)
	}
}

// spentRetries says, in a blocker's reason, that automatic retries ran.
func spentRetries(retries int) string {
	if retries == 0 {
		return ""
	}
	return fmt.Sprintf(" It was interrupted again after %d automatic retries.", retries)
}

func delayWords(delay time.Duration) string {
	if delay < time.Minute {
		return strconv.Itoa(int(delay/time.Second)) + " seconds"
	}
	if delay == time.Minute {
		return "1 minute"
	}
	return strconv.Itoa(int(delay/time.Minute)) + " minutes"
}

func (starter *RemoteLeadStarter) clearRetryWait(ctx context.Context, runID string) {
	if waits, ok := starter.executions.(remoteLeadRetryWaits); ok {
		if err := waits.ClearRetryWait(ctx, runID); err != nil {
			starter.reportError(err)
		}
	}
}

// turnEnded forgets the retries of a session's turn that ended for a reason
// of its own: completed, or blocked. Another session's retry (the parallel
// reviewer's) stays.
func (starter *RemoteLeadStarter) turnEnded(ctx context.Context, runID, sessionID string) {
	waits, ok := starter.executions.(remoteLeadRetryWaits)
	if !ok || ctx.Err() != nil {
		return
	}
	wait, err := waits.RetryWaitForRun(ctx, runID)
	if err == nil && wait.RunID != "" && wait.SessionID == sessionID {
		err = waits.ClearRetryWait(ctx, runID)
	}
	if err != nil {
		starter.reportError(err)
	}
}

// interruptedTurn rebuilds a turn the worker never admitted, from the retry
// wait that kept it, so the retry sends it again with the same identity.
func (starter *RemoteLeadStarter) interruptedTurn(
	ctx context.Context, request remoteLeadRequest, inspectErr error,
) (remoteLeadRequest, bool) {
	var remote *workerhttp.RemoteError
	waits, ok := starter.executions.(remoteLeadRetryWaits)
	if !ok || !errors.As(inspectErr, &remote) || remote.ProtocolError.Code != workerhttp.ErrorNotFound {
		return remoteLeadRequest{}, false
	}
	wait, err := waits.RetryWaitForRun(ctx, request.runID)
	if err != nil || wait.Turn == "" || wait.SessionID != request.identity.SessionID ||
		wait.AttemptID != request.identity.AttemptID {
		return remoteLeadRequest{}, false
	}
	var turn retryTurn
	if err := json.Unmarshal([]byte(wait.Turn), &turn); err != nil {
		return remoteLeadRequest{}, false
	}
	request.identity.IdempotencyKey = turn.IdempotencyKey
	request.request = turn.Request
	if request.request.Validate(request.identity) != nil {
		return remoteLeadRequest{}, false
	}
	return request, true
}

// interruptionBriefing tells a retried turn's agent what happened.
func interruptionBriefing(cause string) string {
	return "\n\nThe previous attempt was interrupted (" + cause + "), not stopped by a problem with the work; " +
		"Commitarium is retrying it automatically. Before continuing, check the durable state (the branch, " +
		"its commits, and the implementation checklist) and carry on from there. Never redo or republish " +
		"work that is already published."
}

// ResumeDueRetries starts the automatic retries that are due. A worker that
// does not answer yet is waited for without spending a retry.
func (starter *RemoteLeadStarter) ResumeDueRetries(ctx context.Context, now time.Time) (int, error) {
	waits, ok := starter.executions.(remoteLeadRetryWaits)
	if !ok {
		return 0, nil
	}
	pending, err := waits.RetryWaits(ctx)
	if err != nil {
		return 0, err
	}
	resumed := 0
	var errs []error
	for _, wait := range pending {
		if wait.DueAt == nil || now.Before(*wait.DueAt) {
			continue
		}
		if reachability, ok := starter.worker.(remoteLeadWorkerReachability); ok {
			if err := reachability.Reachable(ctx, wait.SessionID); err != nil {
				continue
			}
		}
		// Recorded as started first, so a retry that is interrupted in turn
		// schedules the next one from this count.
		started := wait
		started.Retries++
		started.DueAt = nil
		if err := waits.RecordRetryWait(ctx, started); err != nil {
			errs = append(errs, err)
			continue
		}
		_, admitted, err := starter.RecoverBlockerWithDirective(
			ctx, wait.RunID, wait.RunID+":retry:"+wait.AttemptID+":"+strconv.Itoa(started.Retries),
			RecoveryDirective{Action: RecoveryActionContinue, interruption: wait.Cause},
		)
		switch {
		case errors.Is(err, ErrRecoveryNotAllowed):
			// The run moved on. A paused one retries once it is resumed.
			run, getErr := starter.executions.GetRun(ctx, wait.RunID)
			if getErr == nil && run.Status == execution.RunStatusWaitingForUser && run.Paused {
				err = waits.RecordRetryWait(context.WithoutCancel(ctx), wait)
			} else {
				err = waits.ClearRetryWait(context.WithoutCancel(ctx), wait.RunID)
			}
			if err != nil {
				errs = append(errs, err)
			}
		case err != nil:
			// The retry did not start: keep it due for the next look.
			if restoreErr := waits.RecordRetryWait(context.WithoutCancel(ctx), wait); restoreErr != nil {
				errs = append(errs, restoreErr)
			}
			errs = append(errs, fmt.Errorf("retry interrupted turn of run %q: %w", wait.RunID, err))
		case !admitted:
			// Recovery refused to start it and kept the blocker: it needs the
			// user after all.
			if err := waits.ClearRetryWait(ctx, wait.RunID); err != nil {
				errs = append(errs, err)
			}
			run, getErr := starter.executions.GetRun(ctx, wait.RunID)
			if getErr == nil && run.Status == execution.RunStatusWaitingForUser && !run.Paused &&
				run.WaitKind == execution.RunWaitKindBlocker {
				reason := "The turn was interrupted (" + wait.Cause + ") and its automatic retry could not start. " +
					"Continue to try again."
				starter.noteBlocker(ctx, wait.RunID, execution.BlockerEnvironment, "", wait.Cause, reason)
				errs = append(errs, starter.waitRun(ctx, wait.RunID, reason, execution.RunWaitKindBlocker))
			}
		default:
			resumed++
		}
	}
	return resumed, errors.Join(errs...)
}

// RunRetryResumer starts due automatic retries until ctx ends. The waits are
// durable, so a coordinator restart continues their schedule.
func (starter *RemoteLeadStarter) RunRetryResumer(ctx context.Context) {
	ticker := time.NewTicker(retryCheckEvery)
	defer ticker.Stop()
	for {
		if _, err := starter.ResumeDueRetries(ctx, time.Now().UTC()); err != nil {
			starter.reportError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
