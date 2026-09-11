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

type ProviderWorkerRoutes struct {
	CodexLead      RemoteLeadWorker
	CodexReviewer  RemoteLeadWorker
	ClaudeLead     RemoteLeadWorker
	ClaudeReviewer RemoteLeadWorker
}

// ProviderRoutedWorker keeps workflow code provider-neutral. The selected
// route comes from the run snapshot, not the project's current settings, so
// recovery always returns to the provider that owns the original session.
type ProviderRoutedWorker struct {
	runs   ProviderRunFinder
	routes ProviderWorkerRoutes
}

func NewProviderRoutedWorker(
	runs ProviderRunFinder,
	routes ProviderWorkerRoutes,
) (*ProviderRoutedWorker, error) {
	if runs == nil || routes.CodexLead == nil || routes.CodexReviewer == nil ||
		routes.ClaudeLead == nil || routes.ClaudeReviewer == nil {
		return nil, errors.New("run finder and all provider worker clients are required")
	}
	return &ProviderRoutedWorker{runs: runs, routes: routes}, nil
}

func (router *ProviderRoutedWorker) PutAttempt(
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
	client, err := router.routes.forAssignment(run.AgentProviders, role)
	if err != nil {
		return workerhttp.Attempt{}, false, err
	}
	return client.PutAttempt(ctx, identity, request)
}

func (router *ProviderRoutedWorker) GetAttempt(
	ctx context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	run, role, err := router.runAndRole(ctx, reference.SessionID)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	client, err := router.routes.forAssignment(run.AgentProviders, role)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	return client.GetAttempt(ctx, reference)
}

func (router *ProviderRoutedWorker) runAndRole(
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

func (routes ProviderWorkerRoutes) forAssignment(
	providers project.AgentProviders,
	role workerhttp.Role,
) (RemoteLeadWorker, error) {
	provider, err := providerForRole(providers, role)
	if err != nil {
		return nil, err
	}
	switch {
	case provider == project.AgentProviderCodex && role == workerhttp.RoleLead:
		return routes.CodexLead, nil
	case provider == project.AgentProviderCodex && role == workerhttp.RoleReviewer:
		return routes.CodexReviewer, nil
	case provider == project.AgentProviderClaude && role == workerhttp.RoleLead:
		return routes.ClaudeLead, nil
	case provider == project.AgentProviderClaude && role == workerhttp.RoleReviewer:
		return routes.ClaudeReviewer, nil
	default:
		return nil, errors.New("run has an unsupported provider assignment")
	}
}

type ProviderPumpRoutes struct {
	CodexLead      RemoteLeadPump
	CodexReviewer  RemoteLeadPump
	ClaudeLead     RemoteLeadPump
	ClaudeReviewer RemoteLeadPump
}

type ProviderRoutedPump struct {
	runs   ProviderRunFinder
	routes ProviderPumpRoutes
}

func NewProviderRoutedPump(
	runs ProviderRunFinder,
	routes ProviderPumpRoutes,
) (*ProviderRoutedPump, error) {
	if runs == nil || routes.CodexLead == nil || routes.CodexReviewer == nil ||
		routes.ClaudeLead == nil || routes.ClaudeReviewer == nil {
		return nil, errors.New("run finder and all provider event pumps are required")
	}
	return &ProviderRoutedPump{runs: runs, routes: routes}, nil
}

func (router *ProviderRoutedPump) Run(
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
	pump, err := router.routes.forAssignment(run.AgentProviders, role)
	if err != nil {
		return workeringest.PumpResult{}, err
	}
	return pump.Run(ctx, sessionID)
}

func (routes ProviderPumpRoutes) forAssignment(
	providers project.AgentProviders,
	role workerhttp.Role,
) (RemoteLeadPump, error) {
	provider, err := providerForRole(providers, role)
	if err != nil {
		return nil, err
	}
	switch {
	case provider == project.AgentProviderCodex && role == workerhttp.RoleLead:
		return routes.CodexLead, nil
	case provider == project.AgentProviderCodex && role == workerhttp.RoleReviewer:
		return routes.CodexReviewer, nil
	case provider == project.AgentProviderClaude && role == workerhttp.RoleLead:
		return routes.ClaudeLead, nil
	case provider == project.AgentProviderClaude && role == workerhttp.RoleReviewer:
		return routes.ClaudeReviewer, nil
	default:
		return nil, errors.New("run has an unsupported provider assignment")
	}
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
