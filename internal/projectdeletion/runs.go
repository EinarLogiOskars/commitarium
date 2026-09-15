package projectdeletion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type RunExecution interface {
	RunsForFeature(context.Context, string) ([]execution.Run, error)
	SessionsForRun(context.Context, string) ([]execution.Session, error)
	GetRun(context.Context, string) (execution.Run, error)
	GetSession(context.Context, string) (execution.Session, error)
	GetWorkerAttempt(context.Context, string) (execution.WorkerAttemptCheckpoint, error)
	TransitionRun(context.Context, string, execution.RunStatus, execution.RunStatus, string, ...execution.RunWaitKind) (execution.Run, error)
	TransitionSession(context.Context, string, execution.SessionStatus, execution.SessionStatus, string) (execution.Session, error)
}

type WorkerStopper interface {
	GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error)
	ForceStop(context.Context, workerhttp.MutationIdentity, workerhttp.ForceStopRequest) (workerhttp.Attempt, error)
}

type SessionController interface {
	SendCommand(context.Context, string, worker.Command) (execution.Command, error)
}

type LiveSessionRegistry interface {
	Get(string) (worker.Session, bool)
}

type RunStopper struct {
	executions   RunExecution
	worker       WorkerStopper
	controller   SessionController
	liveSessions LiveSessionRegistry
	now          func() time.Time
}

func NewRunStopper(
	executions RunExecution,
	worker WorkerStopper,
	controller SessionController,
	liveSessions ...LiveSessionRegistry,
) *RunStopper {
	stopper := &RunStopper{executions: executions, worker: worker, controller: controller,
		now: func() time.Time { return time.Now().UTC() }}
	if len(liveSessions) > 0 {
		stopper.liveSessions = liveSessions[0]
	}
	return stopper
}

func (stopper *RunStopper) StopFeatureRuns(ctx context.Context, featureID, requestKey string) error {
	if stopper == nil || stopper.executions == nil {
		return ErrUnavailable
	}
	runs, err := stopper.executions.RunsForFeature(ctx, featureID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		sessions, listErr := stopper.executions.SessionsForRun(ctx, run.ID)
		if listErr != nil {
			return listErr
		}
		for _, session := range sessions {
			if session.Status.IsTerminal() {
				continue
			}
			if err := stopper.stopSession(ctx, session, requestKey); err != nil {
				return err
			}
		}
		current, getErr := stopper.executions.GetRun(ctx, run.ID)
		if errors.Is(getErr, execution.ErrNotFound) {
			continue
		}
		if getErr != nil {
			return getErr
		}
		if !current.Status.IsTerminal() {
			if _, transitionErr := stopper.executions.TransitionRun(
				ctx, current.ID, current.Status, execution.RunStatusStopped,
				"The user forced deletion of this project.",
			); transitionErr != nil {
				latest, latestErr := stopper.executions.GetRun(ctx, current.ID)
				if latestErr != nil || !latest.Status.IsTerminal() {
					return transitionErr
				}
			}
		}
	}
	return nil
}

func (stopper *RunStopper) stopSession(
	ctx context.Context,
	session execution.Session,
	requestKey string,
) error {
	checkpoint, checkpointErr := stopper.executions.GetWorkerAttempt(ctx, session.ID)
	if checkpointErr == nil && stopper.worker != nil {
		reference := workerhttp.AttemptReference{SessionID: session.ID, AttemptID: checkpoint.AttemptID}
		attempt, err := stopper.worker.GetAttempt(ctx, reference)
		if err != nil {
			return err
		}
		if attempt.State != workerhttp.AttemptStateTerminal {
			attempt, err = stopper.worker.ForceStop(ctx, workerhttp.MutationIdentity{
				AttemptReference: reference,
				IdempotencyKey:   runDeletionKey(requestKey, session.ID),
			}, workerhttp.ForceStopRequest{Reason: "The user forced deletion of this project."})
			if err != nil {
				return err
			}
			if attempt.State != workerhttp.AttemptStateTerminal {
				return errors.New("worker did not confirm terminal state after forced stop")
			}
		}
	} else if checkpointErr == nil {
		return errors.New("worker force-stop service is unavailable")
	} else if !errors.Is(checkpointErr, execution.ErrNotFound) {
		return checkpointErr
	} else if session.Status != execution.SessionStatusWaitingForUser {
		if stopper.liveSessions != nil {
			if _, active := stopper.liveSessions.Get(session.ID); !active {
				// Simulated runs have no provider checkpoint. After a restart the
				// empty in-memory registry proves no process owns this session.
				goto retireSession
			}
		}
		if stopper.controller == nil {
			return errors.New("session controller is unavailable")
		}
		if _, err := stopper.controller.SendCommand(ctx, session.ID, worker.Command{
			ID: runDeletionKey(requestKey, session.ID), Type: worker.CommandStop,
		}); err != nil {
			return err
		}
		if err := stopper.waitForTerminal(ctx, session.ID); err != nil {
			return err
		}
		return nil
	}

retireSession:
	current, err := stopper.executions.GetSession(ctx, session.ID)
	if errors.Is(err, execution.ErrNotFound) || current.Status.IsTerminal() {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := stopper.executions.TransitionSession(
		ctx, current.ID, current.Status, execution.SessionStatusStopped, current.ProviderSessionID,
	); err != nil {
		latest, latestErr := stopper.executions.GetSession(ctx, current.ID)
		if latestErr != nil || !latest.Status.IsTerminal() {
			return err
		}
	}
	return nil
}

func (stopper *RunStopper) waitForTerminal(ctx context.Context, sessionID string) error {
	deadline := stopper.now().Add(10 * time.Second)
	for {
		current, err := stopper.executions.GetSession(ctx, sessionID)
		if err != nil {
			return err
		}
		if current.Status.IsTerminal() {
			return nil
		}
		if !stopper.now().Before(deadline) {
			return fmt.Errorf("session %q did not stop before the deadline", sessionID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func runDeletionKey(requestKey, sessionID string) string {
	digest := sha256.Sum256([]byte(requestKey + "\x00" + sessionID))
	return "pdel_" + hex.EncodeToString(digest[:12])
}
