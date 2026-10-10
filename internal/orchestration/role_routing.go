package orchestration

import (
	"context"
	"errors"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
)

type ProviderRunFinder interface {
	GetRun(context.Context, string) (execution.Run, error)
}

// AgentWorkers resolves an agent ID to its worker (ADR-016).
type AgentWorkers func(agentID string) (RemoteLeadWorker, error)

// AgentPumps resolves an agent ID to an event pump for its worker.
type AgentPumps func(agentID string) (RemoteLeadPump, error)

type remoteLeadForceStopper interface {
	ForceStop(context.Context, workerhttp.MutationIdentity, workerhttp.ForceStopRequest) (workerhttp.Attempt, error)
}

// AgentRoutedWorker keeps workflow code agent-neutral. The selected agent
// comes from the run snapshot, not the project's current settings, so
// recovery always returns to the agent that owns the original session.
type AgentRoutedWorker struct {
	runs    ProviderRunFinder
	workers AgentWorkers
}

func NewAgentRoutedWorker(runs ProviderRunFinder, workers AgentWorkers) (*AgentRoutedWorker, error) {
	if runs == nil || workers == nil {
		return nil, errors.New("run finder and agent workers are required")
	}
	return &AgentRoutedWorker{runs: runs, workers: workers}, nil
}

func (router *AgentRoutedWorker) PutAttempt(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	run, role, err := router.runAndRole(ctx, identity.SessionID)
	if err != nil {
		return workerhttp.Attempt{}, false, err
	}
	if request.Assignment.Role != role {
		return workerhttp.Attempt{}, false, errors.New("assignment role does not match session identity")
	}
	model := run.AgentModels.Lead
	if role == workerhttp.RoleReviewer {
		model = run.AgentModels.Reviewer
	}
	if model != "" && request.Assignment.Model != "" && request.Assignment.Model != model {
		return workerhttp.Attempt{}, false, errors.New("assignment model does not match run snapshot")
	}
	if model != "" {
		request.Assignment.Model = model
	}
	client, err := router.forAssignment(run.AgentProviders, role)
	if err != nil {
		return workerhttp.Attempt{}, false, err
	}
	return client.PutAttempt(ctx, identity, request)
}

func (router *AgentRoutedWorker) GetAttempt(
	ctx context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	run, role, err := router.runAndRole(ctx, reference.SessionID)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	client, err := router.forAssignment(run.AgentProviders, role)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	return client.GetAttempt(ctx, reference)
}

func (router *AgentRoutedWorker) ForceStop(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.ForceStopRequest,
) (workerhttp.Attempt, error) {
	run, role, err := router.runAndRole(ctx, identity.SessionID)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	client, err := router.forAssignment(run.AgentProviders, role)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	stopper, ok := client.(remoteLeadForceStopper)
	if !ok {
		return workerhttp.Attempt{}, errors.New("selected agent worker does not support forced termination")
	}
	return stopper.ForceStop(ctx, identity, request)
}

func (router *AgentRoutedWorker) Supersede(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.SupersedeRequest,
) (workerhttp.Attempt, error) {
	run, role, err := router.runAndRole(ctx, identity.SessionID)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	client, err := router.forAssignment(run.AgentProviders, role)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	return client.Supersede(ctx, identity, request)
}

func (router *AgentRoutedWorker) runAndRole(
	ctx context.Context,
	sessionID string,
) (execution.Run, workerhttp.Role, error) {
	runID, role, err := runAndRoleForSession(sessionID)
	if err != nil {
		return execution.Run{}, "", err
	}
	run, err := router.runs.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, "", err
	}
	return run, role, nil
}

func (router *AgentRoutedWorker) forAssignment(
	providers project.AgentProviders,
	role workerhttp.Role,
) (RemoteLeadWorker, error) {
	agentID, err := providerForRole(providers, role)
	if err != nil {
		return nil, err
	}
	return router.workers(string(agentID))
}

type AgentRoutedPump struct {
	runs  ProviderRunFinder
	pumps AgentPumps
}

func NewAgentRoutedPump(runs ProviderRunFinder, pumps AgentPumps) (*AgentRoutedPump, error) {
	if runs == nil || pumps == nil {
		return nil, errors.New("run finder and agent event pumps are required")
	}
	return &AgentRoutedPump{runs: runs, pumps: pumps}, nil
}

func (router *AgentRoutedPump) Run(
	ctx context.Context,
	sessionID string,
) (workeringest.PumpResult, error) {
	runID, role, err := runAndRoleForSession(sessionID)
	if err != nil {
		return workeringest.PumpResult{}, err
	}
	run, err := router.runs.GetRun(ctx, runID)
	if err != nil {
		return workeringest.PumpResult{}, err
	}
	agentID, err := providerForRole(run.AgentProviders, role)
	if err != nil {
		return workeringest.PumpResult{}, err
	}
	pump, err := router.pumps(string(agentID))
	if err != nil {
		return workeringest.PumpResult{}, err
	}
	return pump.Run(ctx, sessionID)
}

func runAndRoleForSession(sessionID string) (string, workerhttp.Role, error) {
	switch {
	case strings.HasSuffix(sessionID, ":lead"):
		return strings.TrimSuffix(sessionID, ":lead"), workerhttp.RoleLead, nil
	case strings.HasSuffix(sessionID, ":reviewer"):
		return strings.TrimSuffix(sessionID, ":reviewer"), workerhttp.RoleReviewer, nil
	default:
		return "", "", errors.New("remote workflow session has an unsupported identity")
	}
}

func providerForRole(
	providers project.AgentProviders,
	role workerhttp.Role,
) (project.AgentProvider, error) {
	normalized, err := providers.Normalize()
	if err != nil {
		return "", err
	}
	switch role {
	case workerhttp.RoleLead:
		return normalized.Lead, nil
	case workerhttp.RoleReviewer:
		return normalized.Reviewer, nil
	default:
		return "", errors.New("remote workflow attempt has an unsupported role")
	}
}
