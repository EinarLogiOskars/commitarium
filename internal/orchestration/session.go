package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// Sessions (ADR-019) are work orders of kind session: one lead conversation
// that the user directs message by message on the session's branch.

var (
	// ErrNotASession rejects a session action on a work order of another kind.
	ErrNotASession = errors.New("this work order is not a session")
	// ErrSessionBusy rejects a message while the agent is still working.
	ErrSessionBusy = errors.New("the session's agent is still working")
)

const (
	// sessionPlanRecord is a session's plan record: it opens the session's
	// branch and draft pull request, as for a quick change.
	sessionPlanRecord = "Interactive session. There is no documented plan: the user directs each " +
		"change in conversation with the agent."
	sessionYourTurnReason = "Your turn."
	sessionWorkingReason  = "The agent is working on your message."
	untitledSessionPrefix = "Untitled session "
)

type remoteLeadFirstTurnExecution interface {
	BeginFirstWorkerTurn(context.Context, worker.Command, string, string, string) (execution.WorkerTurnAdmissionResult, bool, error)
}

type remoteLeadFeatureRenamer interface {
	Rename(context.Context, string, string) error
}

func sessionTurnAttemptID(sessionID string, turn int) string {
	return workflowAttemptForVersion(sessionID, "session", 1, turn)
}

func sessionTurnNumber(sessionID, attemptID string) (int, bool) {
	_, turn, ok := workflowAttemptVersionAndTurn(sessionID, "session", attemptID)
	return turn, ok
}

// openSession leaves a started session waiting for the user's first message.
// The lead session holds no provider conversation until then.
func (starter *RemoteLeadStarter) openSession(
	ctx context.Context,
	run execution.Run,
	sessionID string,
) (execution.Run, bool, error) {
	if _, err := starter.executions.TransitionSession(
		ctx, sessionID, execution.SessionStatusStarting, execution.SessionStatusRunning, "",
	); err != nil {
		return execution.Run{}, false, err
	}
	if _, err := starter.executions.TransitionSession(
		ctx, sessionID, execution.SessionStatusRunning, execution.SessionStatusWaitingForUser, "",
	); err != nil {
		return execution.Run{}, false, err
	}
	if err := starter.waitRun(ctx, run.ID, sessionYourTurnReason, execution.RunWaitKindClarification); err != nil {
		return execution.Run{}, false, err
	}
	opened, err := starter.executions.GetRun(ctx, run.ID)
	return opened, true, err
}

// SendSessionMessage starts the session's next agent turn with the user's
// message. The first message starts the provider conversation with the
// session's instructions; later ones resume it. A retried command returns the
// run without starting another turn.
func (starter *RemoteLeadStarter) SendSessionMessage(
	ctx context.Context,
	runID string,
	command worker.Command,
) (execution.Run, error) {
	if err := command.Validate(); err != nil {
		return execution.Run{}, fmt.Errorf("%w: %v", execution.ErrInvalidCommand, err)
	}
	if command.Type != worker.CommandMessage {
		return execution.Run{}, ErrCommandNotAllowed
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, err
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, err
	}
	if storedFeature.Kind != feature.KindSession {
		return execution.Run{}, ErrNotASession
	}
	if existing, err := starter.executions.GetCommand(ctx, command.ID); err == nil {
		if existing.SessionID != remoteLeadSessionID(run.ID) || existing.Type != command.Type ||
			existing.Message != command.Message {
			return execution.Run{}, execution.ErrCommandConflict
		}
		return run, nil
	} else if !errors.Is(err, execution.ErrNotFound) {
		return execution.Run{}, fmt.Errorf("look up session message: %w", err)
	}
	if run.Status == execution.RunStatusRunning {
		return execution.Run{}, ErrSessionBusy
	}
	if run.Status != execution.RunStatusWaitingForUser || run.Paused ||
		storedFeature.State != feature.StateImplementing {
		return execution.Run{}, ErrCommandNotAllowed
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return execution.Run{}, err
	}
	if lead.Status != execution.SessionStatusWaitingForUser {
		return execution.Run{}, ErrSessionBusy
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return execution.Run{}, fmt.Errorf("load session workspace: %w", err)
	}
	request := remoteLeadRequest{
		runID: run.ID, commandID: command.ID, agentName: "lead agent",
		waitingReason: sessionYourTurnReason, waitKind: execution.RunWaitKindClarification,
		request: workerhttp.PutAttemptRequest{
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				Model:          run.AgentModels.Lead, ProjectID: storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
		},
	}
	first := lead.ProviderSessionID == ""
	var checkpoint execution.WorkerAttemptCheckpoint
	if first {
		previewContext, err := starter.previewRunInstructions(ctx, storedFeature.ProjectID)
		if err != nil {
			return execution.Run{}, err
		}
		attemptID := sessionTurnAttemptID(lead.ID, 1)
		request.identity = workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":start",
		}
		request.request.Mode = workerhttp.AttemptModeStart
		request.request.Instructions = sessionInstructions(prepared) + previewContext +
			"\n\nThe user's first message:\n" + command.Message
		request.request.OutputContract = workerhttp.OutputContractSessionOpening
	} else {
		checkpoint, err = starter.executions.GetWorkerAttempt(ctx, lead.ID)
		if err != nil {
			return execution.Run{}, fmt.Errorf("load the session's last turn: %w", err)
		}
		prior, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{
			SessionID: lead.ID, AttemptID: checkpoint.AttemptID,
		})
		if err != nil {
			return execution.Run{}, fmt.Errorf("confirm the session's last turn: %w", err)
		}
		if err := validateCompletedTurn(lead, checkpoint, prior); err != nil {
			return execution.Run{}, err
		}
		turn, ok := sessionTurnNumber(lead.ID, checkpoint.AttemptID)
		if !ok {
			return execution.Run{}, fmt.Errorf("%w: the session's last turn is not a session turn", ErrCommandNotAllowed)
		}
		attemptID := sessionTurnAttemptID(lead.ID, turn+1)
		request.identity = workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		}
		request.request.Mode = workerhttp.AttemptModeResume
		request.request.ProviderSessionID = lead.ProviderSessionID
		request.request.Instructions = command.Message
	}
	if err := request.request.Validate(request.identity); err != nil {
		return execution.Run{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, ErrSessionBusy
	}
	var admitted bool
	if first {
		firstTurns, ok := starter.executions.(remoteLeadFirstTurnExecution)
		if !ok {
			starter.release(run.ID)
			return execution.Run{}, errors.New("session turns are unavailable")
		}
		_, admitted, err = firstTurns.BeginFirstWorkerTurn(
			ctx, command, lead.ID, request.identity.AttemptID, sessionWorkingReason,
		)
	} else {
		_, admitted, err = starter.executions.BeginWorkerTurn(
			ctx, command, lead.ID, checkpoint, request.identity.AttemptID,
			feature.StateImplementing, sessionWorkingReason,
		)
	}
	if err != nil {
		starter.release(run.ID)
		if errors.Is(err, execution.ErrStateConflict) || errors.Is(err, execution.ErrWorkerAttemptConflict) {
			return execution.Run{}, ErrSessionBusy
		}
		return execution.Run{}, err
	}
	if !admitted {
		starter.release(run.ID)
	} else {
		go starter.launch(request)
	}
	return starter.executions.GetRun(ctx, run.ID)
}

// nameSession renames an untitled session after its first turn's title. A
// session the user already renamed keeps its name.
func (starter *RemoteLeadStarter) nameSession(ctx context.Context, runID string, title string) {
	renamer, ok := starter.features.(remoteLeadFeatureRenamer)
	title = strings.TrimSpace(title)
	if !ok || title == "" {
		return
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		starter.reportError(fmt.Errorf("name session for run %q: %w", runID, err))
		return
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil || storedFeature.Kind != feature.KindSession ||
		!strings.HasPrefix(storedFeature.Title, untitledSessionPrefix) {
		return
	}
	if err := renamer.Rename(ctx, storedFeature.ID, title); err != nil {
		starter.reportError(fmt.Errorf("name session %q: %w", storedFeature.ID, err))
	}
}

// sessionInstructions start a session's provider conversation. The user's
// first message follows them.
func sessionInstructions(prepared workspace.Workspace) string {
	return "You are working with the user in an interactive Commitarium session on this repository. " +
		"The user directs every change in conversation; there is no plan and no reviewer. Work in the " +
		"checkout in your working directory: inspect and edit files as the user asks, run the relevant " +
		"tests or builds when useful, and reply briefly with what you changed and anything the user " +
		"should check. Ask when a request is unclear. Do not commit, push, change the pull request, or " +
		"merge unless the user explicitly asks you to commit: the user previews your uncommitted changes " +
		"and ends the session, and Commitarium then asks you to commit the work. Durable repository " +
		"state is authoritative over conversational memory.\n\n" +
		"For this first reply only, also give a short title (at most 60 characters) naming what the user " +
		"wants to work on in this session.\n\n" +
		"Repository: " + prepared.RepositoryOwner + "/" + prepared.RepositoryName +
		"\nBase branch: " + prepared.BaseBranch + "\nSession branch: " + prepared.Branch +
		"\nBase commit: " + prepared.BaseCommitID +
		fmt.Sprintf("\nDraft pull request: #%d (%s)", prepared.PullRequestNumber, prepared.PullRequestURL)
}
