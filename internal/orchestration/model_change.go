package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A user who notices a wrong model switches it without resetting the work:
// at once, by stopping the agent and resuming its conversation on the new
// model, or from the agent's next turn.

var ErrModelChangeUnavailable = errors.New("changing a run's models is not available")

type remoteLeadModelChanges interface {
	ChangeRunModels(ctx context.Context, runID string, models project.AgentModels, stopAttempts []string) (execution.Run, []execution.ModelChange, error)
	TakeModelSwitch(ctx context.Context, runID, attemptID string) (bool, error)
}

// ChangeModels switches a run's models. With now, an agent working on a
// changed role is stopped and resumes on the new model; otherwise the change
// applies from its next turn.
func (starter *RemoteLeadStarter) ChangeModels(
	ctx context.Context,
	runID string,
	models project.AgentModels,
	now bool,
) (execution.Run, error) {
	changer, ok := starter.executions.(remoteLeadModelChanges)
	if !ok {
		return execution.Run{}, ErrModelChangeUnavailable
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, err
	}
	if run.Status.IsTerminal() {
		return execution.Run{}, fmt.Errorf("%w: the run has ended", ErrInvalidRunRequest)
	}
	sessions, err := starter.executions.ActiveSessionsForRun(ctx, runID)
	if err != nil {
		return execution.Run{}, err
	}
	stops := make([]workerhttp.AttemptReference, 0, len(sessions))
	if now {
		if _, canStop := starter.worker.(remoteLeadForceStopper); !canStop {
			return execution.Run{}, ErrModelChangeUnavailable
		}
		for _, session := range sessions {
			wanted := models.Lead
			if session.Role == worker.RoleReviewer {
				wanted = models.Reviewer
			}
			if wanted == "" || wanted == modelForSession(run, session.Role) {
				continue
			}
			checkpoint, err := starter.executions.GetWorkerAttempt(ctx, session.ID)
			if err != nil {
				continue
			}
			reference := workerhttp.AttemptReference{SessionID: session.ID, AttemptID: checkpoint.AttemptID}
			attempt, err := starter.worker.GetAttempt(ctx, reference)
			if err != nil || !attemptIsLive(attempt.State) {
				continue
			}
			stops = append(stops, reference)
		}
	}
	stopIDs := make([]string, 0, len(stops))
	for _, reference := range stops {
		stopIDs = append(stopIDs, reference.AttemptID)
	}
	changed, changes, err := changer.ChangeRunModels(ctx, runID, models, stopIDs)
	if err != nil {
		return execution.Run{}, err
	}
	for _, change := range changes {
		starter.recordModelChange(ctx, sessions, change)
	}
	for _, reference := range stops {
		go starter.stopForModelSwitch(reference)
	}
	return changed, nil
}

func attemptIsLive(state workerhttp.AttemptState) bool {
	switch state {
	case workerhttp.AttemptStateStarting, workerhttp.AttemptStateRunning, workerhttp.AttemptStatePauseRequested:
		return true
	}
	return false
}

// recordModelChange notes the change in the conversation of the role's
// active session, so the transcript shows where the model changed.
func (starter *RemoteLeadStarter) recordModelChange(ctx context.Context, sessions []execution.Session, change execution.ModelChange) {
	for _, session := range sessions {
		if session.Role != change.Role {
			continue
		}
		text := "Switched the " + string(change.Role) + " to " + change.ToModel + " from " + change.FromModel
		if change.Stopped {
			text += ", stopping its turn."
		} else {
			text += ", from its next turn."
		}
		id := fmt.Sprintf("%s:model-change:%d", session.ID, change.CreatedAt.UnixNano())
		if _, err := starter.executions.RecordSessionEventWithID(
			ctx, id, session.ID, worker.Event{Type: worker.EventActivity, Text: text},
		); err != nil {
			starter.reportError(fmt.Errorf("record model change for session %q: %w", session.ID, err))
		}
	}
}

// stopForModelSwitch force-stops a turn marked for a model switch; its
// observer then resumes it on the new model.
func (starter *RemoteLeadStarter) stopForModelSwitch(reference workerhttp.AttemptReference) {
	stopper, ok := starter.worker.(remoteLeadForceStopper)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(starter.lifetime, 2*time.Minute)
	defer cancel()
	if _, err := stopper.ForceStop(ctx, workerhttp.MutationIdentity{
		AttemptReference: reference,
		IdempotencyKey:   reference.AttemptID + ":model-switch",
	}, workerhttp.ForceStopRequest{Reason: "the user switched the model"}); err != nil {
		starter.reportError(fmt.Errorf("stop attempt %q to switch models: %w", reference.AttemptID, err))
	}
}

// resumeAfterModelSwitch continues a turn the user stopped to switch models.
// A session hands the turn back to the user; other work resumes the same
// conversation on the new model from where it stopped.
func (starter *RemoteLeadStarter) resumeAfterModelSwitch(
	ctx context.Context,
	request remoteLeadRequest,
	session execution.Session,
	attempt workerhttp.Attempt,
) error {
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return err
	}
	model := modelForSession(run, session.Role)
	if _, _, isSession := workflowAttemptVersionAndTurn(session.ID, "session", attempt.AttemptID); isSession {
		if _, err := starter.executions.RecordSessionEventWithID(
			ctx, attempt.AttemptID+":model-switch", session.ID,
			worker.Event{Type: worker.EventActivity, Text: "Stopped to switch to " + model + ". Send a message to continue."},
		); err != nil {
			return err
		}
		return starter.waitRun(ctx, request.runID, sessionYourTurnReason, execution.RunWaitKindClarification)
	}
	request.modelSwitch = true
	request.allowRecoveryContinuation = true
	request.recoveryMessage = "The user stopped your previous turn to switch you to the model " + model +
		". Continue the same work from where it stopped."
	continued, err := starter.continueFailedAttempt(ctx, request, attempt)
	if err != nil {
		return err
	}
	if !continued {
		return errors.New("the turn stopped for a model switch could not be resumed")
	}
	return nil
}

// takeModelSwitch reports whether a stopped attempt was stopped to switch
// models.
func (starter *RemoteLeadStarter) takeModelSwitch(ctx context.Context, runID, attemptID string) (bool, error) {
	changer, ok := starter.executions.(remoteLeadModelChanges)
	if !ok {
		return false, nil
	}
	return changer.TakeModelSwitch(ctx, runID, attemptID)
}
