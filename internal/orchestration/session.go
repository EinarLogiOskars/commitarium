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
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
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
	sessionYourTurnReason  = "Your turn."
	sessionWorkingReason   = "The agent is working on your message."
	sessionFinishingReason = "The agent is committing the session's work."
	sessionFinishMessage   = "Finish the session."
	untitledSessionPrefix  = "Untitled session "
)

type remoteLeadFirstTurnExecution interface {
	BeginFirstWorkerTurn(context.Context, worker.Command, string, string, string) (execution.WorkerTurnAdmissionResult, bool, error)
}

type remoteLeadSessionCatchUp interface {
	SessionCatchUp(context.Context, string, string) (string, bool, error)
}

type remoteLeadMergeReadyClearer interface {
	ClearMergeReady(context.Context, string, string) error
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
	if run.Status != execution.RunStatusWaitingForUser || run.Paused {
		return execution.Run{}, ErrCommandNotAllowed
	}
	if storedFeature.State == feature.StateReadyToMerge && run.WaitKind == execution.RunWaitKindMergeGate {
		if err := starter.backToChatting(ctx, storedFeature, command.ID); err != nil {
			return execution.Run{}, err
		}
		storedFeature.State = feature.StateImplementing
	}
	if storedFeature.State != feature.StateImplementing {
		return execution.Run{}, ErrCommandNotAllowed
	}
	lead, err := starter.executions.GetSession(ctx, remoteLeadSessionID(run.ID))
	if err != nil {
		return execution.Run{}, err
	}
	if lead.Status != execution.SessionStatusWaitingForUser {
		return execution.Run{}, ErrSessionBusy
	}
	first := lead.ProviderSessionID == ""
	var checkpoint execution.WorkerAttemptCheckpoint
	attemptID := sessionTurnAttemptID(lead.ID, 1)
	if !first {
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
		turn, ok := lastSessionTurn(lead.ID, checkpoint.AttemptID)
		if !ok {
			return execution.Run{}, fmt.Errorf("%w: the session's last turn is not a session turn", ErrCommandNotAllowed)
		}
		attemptID = sessionTurnAttemptID(lead.ID, turn+1)
	}
	request, err := starter.sessionTurnRequest(ctx, run, storedFeature, lead, attemptID, command)
	if err != nil {
		return execution.Run{}, err
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

// sessionTurnRequest builds a session turn for the user's message. A lead
// session without a provider conversation starts one with the session's
// instructions; otherwise the turn resumes it, catching up with main first
// when it moved.
func (starter *RemoteLeadStarter) sessionTurnRequest(
	ctx context.Context,
	run execution.Run,
	storedFeature feature.Feature,
	lead execution.Session,
	attemptID string,
	command worker.Command,
) (remoteLeadRequest, error) {
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return remoteLeadRequest{}, fmt.Errorf("load session workspace: %w", err)
	}
	request := remoteLeadRequest{
		runID: run.ID, commandID: command.ID, agentName: "lead agent",
		waitingReason: sessionYourTurnReason, waitKind: execution.RunWaitKindClarification,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
		},
		request: workerhttp.PutAttemptRequest{
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				Model:          run.AgentModels.Lead, ProjectID: storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
		},
	}
	if lead.ProviderSessionID == "" {
		previewContext, err := starter.previewRunInstructions(ctx, storedFeature.ProjectID)
		if err != nil {
			return remoteLeadRequest{}, err
		}
		request.identity.IdempotencyKey = attemptID + ":start"
		request.request.Mode = workerhttp.AttemptModeStart
		request.request.Instructions = sessionInstructions(prepared) + previewContext +
			"\n\nThe user's first message:\n" + command.Message
		request.request.OutputContract = workerhttp.OutputContractSessionOpening
	} else {
		request.identity.IdempotencyKey = attemptID + ":resume"
		request.request.Mode = workerhttp.AttemptModeResume
		request.request.ProviderSessionID = lead.ProviderSessionID
		request.request.Instructions = command.Message
		if mainHead, behind := starter.behindMain(ctx, storedFeature); behind {
			request.request.Instructions = sessionCatchUpInstructions(prepared.BaseBranch, mainHead) +
				"\n\nThe user's message:\n" + command.Message
		}
	}
	return request, nil
}

// rebuildSessionTurn rebuilds an admitted session turn from its stored
// message, for a turn that never reached a worker, for example because the
// agent's worker was down. It reports false for any other turn.
func (starter *RemoteLeadStarter) rebuildSessionTurn(
	ctx context.Context,
	request remoteLeadRequest,
) (remoteLeadRequest, bool) {
	if _, ok := sessionTurnNumber(request.identity.SessionID, request.identity.AttemptID); !ok {
		return remoteLeadRequest{}, false
	}
	run, err := starter.executions.GetRun(ctx, request.runID)
	if err != nil {
		return remoteLeadRequest{}, false
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil || storedFeature.Kind != feature.KindSession {
		return remoteLeadRequest{}, false
	}
	lead, err := starter.executions.GetSession(ctx, request.identity.SessionID)
	if err != nil {
		return remoteLeadRequest{}, false
	}
	pending, err := starter.executions.PendingCommandsForSession(ctx, lead.ID)
	if err != nil || len(pending) != 1 || pending[0].Type != worker.CommandMessage {
		return remoteLeadRequest{}, false
	}
	rebuilt, err := starter.sessionTurnRequest(ctx, run, storedFeature, lead, request.identity.AttemptID, worker.Command{
		ID: pending[0].ID, Type: pending[0].Type, Message: pending[0].Message,
	})
	if err != nil {
		return remoteLeadRequest{}, false
	}
	return rebuilt, true
}

// lastSessionTurn numbers a session's last turn: a message turn, or the
// finish turn that followed message turn n-1.
func lastSessionTurn(sessionID, attemptID string) (int, bool) {
	if turn, ok := sessionTurnNumber(sessionID, attemptID); ok {
		return turn, true
	}
	return implementationTurnNumber(sessionID, attemptID)
}

// backToChatting reopens a session at the merge gate for more messages. Its
// merge target is forgotten, so the next Finish pins a new commit.
func (starter *RemoteLeadStarter) backToChatting(ctx context.Context, storedFeature feature.Feature, key string) error {
	clearer, ok := starter.workspaces.(remoteLeadMergeReadyClearer)
	if !ok {
		return errors.New("a session cannot go back to chatting")
	}
	if err := clearer.ClearMergeReady(ctx, storedFeature.ProjectID, storedFeature.ID); err != nil {
		return fmt.Errorf("reopen the session: %w", err)
	}
	if _, err := starter.planning.TransitionFeature(
		ctx, storedFeature.ID, feature.StateImplementing,
		workflow.Actor{Kind: workflow.ActorKindUser, ID: "local-user"}, key+":back-to-chatting",
	); err != nil {
		return fmt.Errorf("reopen the session: %w", err)
	}
	return nil
}

// FinishSession asks the agent to commit the session's work and publish it.
// The published commit then goes through the merge gate like any work order
// without a reviewer (ADR-019).
func (starter *RemoteLeadStarter) FinishSession(
	ctx context.Context,
	runID string,
	idempotencyKey string,
) (execution.Run, error) {
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
	command := worker.Command{ID: idempotencyKey, Type: worker.CommandMessage, Message: sessionFinishMessage}
	if err := command.Validate(); err != nil {
		return execution.Run{}, fmt.Errorf("%w: %v", execution.ErrInvalidCommand, err)
	}
	if existing, err := starter.executions.GetCommand(ctx, command.ID); err == nil {
		if existing.SessionID != remoteLeadSessionID(run.ID) || existing.Message != command.Message {
			return execution.Run{}, execution.ErrCommandConflict
		}
		return run, nil
	} else if !errors.Is(err, execution.ErrNotFound) {
		return execution.Run{}, fmt.Errorf("look up session finish: %w", err)
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
	if lead.Status != execution.SessionStatusWaitingForUser || lead.ProviderSessionID == "" {
		// Nothing has happened in the session yet: there is nothing to finish.
		return execution.Run{}, ErrCommandNotAllowed
	}
	checkpoint, err := starter.executions.GetWorkerAttempt(ctx, lead.ID)
	if err != nil {
		return execution.Run{}, fmt.Errorf("load the session's last turn: %w", err)
	}
	prior, err := starter.worker.GetAttempt(ctx, workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: checkpoint.AttemptID})
	if err != nil {
		return execution.Run{}, fmt.Errorf("confirm the session's last turn: %w", err)
	}
	if err := validateCompletedTurn(lead, checkpoint, prior); err != nil {
		return execution.Run{}, err
	}
	turn, ok := lastSessionTurn(lead.ID, checkpoint.AttemptID)
	if !ok {
		return execution.Run{}, fmt.Errorf("%w: the session's last turn is not a session turn", ErrCommandNotAllowed)
	}
	prepared, err := starter.workspaces.Get(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		return execution.Run{}, fmt.Errorf("load session workspace: %w", err)
	}
	attemptID := implementationAttemptForVersion(lead.ID, run.PlanVersion, turn+1)
	instructions := sessionFinishInstructions(prepared, attemptID)
	if mainHead, behind := starter.behindMain(ctx, storedFeature); behind {
		instructions = sessionCatchUpInstructions(prepared.BaseBranch, mainHead) + "\n\n" + instructions
	}
	request := remoteLeadRequest{
		runID: run.ID, commandID: command.ID, agentName: "lead agent",
		waitingReason: implementationReadyReason, waitKind: execution.RunWaitKindPhaseCheckpoint,
		identity: workerhttp.MutationIdentity{
			AttemptReference: workerhttp.AttemptReference{SessionID: lead.ID, AttemptID: attemptID},
			IdempotencyKey:   attemptID + ":resume",
		},
		request: workerhttp.PutAttemptRequest{
			Mode: workerhttp.AttemptModeResume,
			Assignment: workerhttp.Assignment{
				AgentProfileID: starter.profileID(run.AgentProviders.LeadAgent, worker.RoleLead),
				Model:          run.AgentModels.Lead, ProjectID: storedFeature.ProjectID, FeatureID: storedFeature.ID,
				Role: workerhttp.RoleLead, WorkspaceID: prepared.ID,
			},
			ProviderSessionID: lead.ProviderSessionID,
			Instructions:      instructions,
			OutputContract:    workerhttp.OutputContractImplementationLead,
		},
	}
	if err := request.request.Validate(request.identity); err != nil {
		return execution.Run{}, fmt.Errorf("%w: %v", ErrInvalidRunRequest, err)
	}
	if !starter.claim(run.ID) {
		return execution.Run{}, ErrSessionBusy
	}
	_, admitted, err := starter.executions.BeginWorkerTurn(
		ctx, command, lead.ID, checkpoint, attemptID, feature.StateImplementing, sessionFinishingReason,
	)
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

// sessionFinishInstructions ask the agent to commit and publish the session's
// work for the merge gate.
func sessionFinishInstructions(prepared workspace.Workspace, attemptID string) string {
	return "The user has finished this session. Commit the remaining work on the session branch as one " +
		"commit whose imperative subject describes the whole session. Keep any commits the user asked for " +
		"earlier as they are: do not squash or rewrite them. Then " +
		implementationPublicationInstructions(prepared, attemptID)
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

// behindMain reports whether the default branch moved past the work order's
// checkout, and main's head. A failed check is reported and counts as not
// behind: a session turn goes ahead without catching up and the next one
// checks again; a refused merge stays a plain blocker.
func (starter *RemoteLeadStarter) behindMain(ctx context.Context, storedFeature feature.Feature) (string, bool) {
	catchUp, ok := starter.workspaces.(remoteLeadSessionCatchUp)
	if !ok {
		return "", false
	}
	mainHead, behind, err := catchUp.SessionCatchUp(ctx, storedFeature.ProjectID, storedFeature.ID)
	if err != nil {
		starter.reportError(fmt.Errorf("check whether work order %q is behind main: %w", storedFeature.ID, err))
		return "", false
	}
	return mainHead, behind
}

// sessionCatchUpInstructions ask the agent to merge the moved default branch
// into the session branch before the user's message, keeping uncommitted
// work. Conflicts are resolved with the user, not by the agent alone.
func sessionCatchUpInstructions(baseBranch string, mainHead string) string {
	return "Before the user's message: the base branch '" + baseBranch + "' has moved to " + mainHead +
		" since this session last caught up. Bring it into the session branch while keeping the user's " +
		"uncommitted work: stash everything including untracked files, run 'git fetch commitarium " +
		baseBranch + "' and 'git merge --no-edit FETCH_HEAD', then restore the stash. If the merge or " +
		"restoring the stash conflicts, stop there: do not resolve the conflicts on your own. Explain each " +
		"conflict to the user in plain terms, ask how to resolve it, and continue with their message only " +
		"once it is resolved. Otherwise say in one line that you brought in the latest " + baseBranch +
		", then handle the message."
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
