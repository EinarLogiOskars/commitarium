package orchestration

import (
	"context"
	"errors"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type CommandExecution interface {
	CreateCommand(
		ctx context.Context,
		id string,
		sessionID string,
		commandType worker.CommandType,
		message string,
	) (execution.Command, bool, error)
	ResolveCommand(
		ctx context.Context,
		id string,
		status execution.CommandStatus,
		errorMessage string,
	) (execution.Command, error)
	GetSession(ctx context.Context, id string) (execution.Session, error)
	TransitionSession(
		ctx context.Context,
		id string,
		expected execution.SessionStatus,
		status execution.SessionStatus,
		providerSessionID string,
	) (execution.Session, error)
}

type Controller struct {
	executions CommandExecution
	sessions   SessionRegistry
}

var ErrCommandNotAllowed = errors.New("command is not allowed for session state")

func NewController(
	executions CommandExecution,
	sessions SessionRegistry,
) *Controller {
	return &Controller{executions: executions, sessions: sessions}
}

func (c *Controller) SendCommand(
	ctx context.Context,
	sessionID string,
	command worker.Command,
) (execution.Command, error) {
	stored, created, err := c.executions.CreateCommand(
		ctx,
		command.ID,
		sessionID,
		command.Type,
		command.Message,
	)
	if err != nil {
		return execution.Command{}, fmt.Errorf("record session command: %w", err)
	}
	if stored.Status != execution.CommandStatusPending {
		return stored, nil
	}

	liveSession, active := c.sessions.Get(sessionID)
	if !active {
		return c.rejectCommand(ctx, stored, ErrSessionNotActive)
	}
	rolledForwardPause, err := c.prepareCommand(ctx, sessionID, command.Type, created)
	if err != nil {
		return c.rejectCommand(ctx, stored, err)
	}
	if err := liveSession.Send(ctx, command); err != nil {
		if rolledForwardPause {
			_, rollbackErr := c.executions.TransitionSession(
				context.WithoutCancel(ctx),
				sessionID,
				execution.SessionStatusPauseRequested,
				execution.SessionStatusRunning,
				"",
			)
			if rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("roll back pause request: %w", rollbackErr))
			}
		}
		return c.rejectCommand(
			ctx,
			stored,
			fmt.Errorf("send command to session %q: %w", sessionID, err),
		)
	}

	resolved, err := c.executions.ResolveCommand(
		ctx,
		stored.ID,
		execution.CommandStatusApplied,
		"",
	)
	if err != nil {
		return execution.Command{}, fmt.Errorf("mark command %q applied: %w", stored.ID, err)
	}
	return resolved, nil
}

func (c *Controller) prepareCommand(
	ctx context.Context,
	sessionID string,
	commandType worker.CommandType,
	created bool,
) (bool, error) {
	session, err := c.executions.GetSession(ctx, sessionID)
	if err != nil {
		return false, fmt.Errorf("get controlled session %q: %w", sessionID, err)
	}

	switch commandType {
	case worker.CommandMessage:
		if session.Status != execution.SessionStatusRunning &&
			session.Status != execution.SessionStatusPauseRequested &&
			session.Status != execution.SessionStatusPaused {
			return false, commandStateError(commandType, session.Status)
		}
	case worker.CommandPause:
		if created && session.Status != execution.SessionStatusRunning {
			return false, commandStateError(commandType, session.Status)
		}
		if session.Status == execution.SessionStatusRunning {
			if _, err := c.executions.TransitionSession(
				ctx,
				sessionID,
				execution.SessionStatusRunning,
				execution.SessionStatusPauseRequested,
				"",
			); err != nil {
				return false, fmt.Errorf("record pause request: %w", err)
			}
			return true, nil
		}
		if session.Status != execution.SessionStatusPauseRequested &&
			session.Status != execution.SessionStatusPaused {
			return false, commandStateError(commandType, session.Status)
		}
	case worker.CommandContinue:
		if created && session.Status != execution.SessionStatusPaused {
			return false, commandStateError(commandType, session.Status)
		}
		if session.Status != execution.SessionStatusPaused &&
			session.Status != execution.SessionStatusRunning {
			return false, commandStateError(commandType, session.Status)
		}
	case worker.CommandStop:
		if session.Status != execution.SessionStatusRunning &&
			session.Status != execution.SessionStatusPauseRequested &&
			session.Status != execution.SessionStatusPaused {
			return false, commandStateError(commandType, session.Status)
		}
	default:
		return false, commandStateError(commandType, session.Status)
	}
	return false, nil
}

func (c *Controller) rejectCommand(
	ctx context.Context,
	command execution.Command,
	cause error,
) (execution.Command, error) {
	resolved, err := c.executions.ResolveCommand(
		context.WithoutCancel(ctx),
		command.ID,
		execution.CommandStatusRejected,
		cause.Error(),
	)
	if err != nil {
		return execution.Command{}, errors.Join(
			cause,
			fmt.Errorf("mark command %q rejected: %w", command.ID, err),
		)
	}
	return resolved, cause
}

func commandStateError(
	commandType worker.CommandType,
	status execution.SessionStatus,
) error {
	return fmt.Errorf("%w: cannot %s while %s", ErrCommandNotAllowed, commandType, status)
}
