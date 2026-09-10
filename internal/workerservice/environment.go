package workerservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

var (
	ErrInvalidEnvironmentResolver = errors.New("invalid worker environment resolver")
	ErrProfileUnavailable         = errors.New("worker agent profile is unavailable")
	ErrWorkspaceUnavailable       = errors.New("worker workspace is unavailable")
	ErrConfigurationMismatch      = errors.New("worker launch environment does not match the assignment")
)

// EnvironmentResolver turns the coordinator's non-secret assignment identity
// into process launch data that exists only inside the worker trust boundary.
type EnvironmentResolver interface {
	Resolve(context.Context, workerhttp.Assignment) (worker.LaunchEnvironment, error)
}

type EnvironmentResolverFunc func(
	context.Context,
	workerhttp.Assignment,
) (worker.LaunchEnvironment, error)

func (function EnvironmentResolverFunc) Resolve(
	ctx context.Context,
	assignment workerhttp.Assignment,
) (worker.LaunchEnvironment, error) {
	return function(ctx, assignment)
}

// RootedEnvironmentResolver maps a validated workspace ID directly to one
// child directory of a worker-owned root. The coordinator can select any
// prepared workspace without the worker maintaining a second project manifest.
// Canonical path checks prevent a symlinked child from escaping the root.
type RootedEnvironmentResolver struct {
	agentProfileID string
	workspaceRoot  string
	variables      []string
	roleVariables  map[worker.Role][]string
}

type RootedEnvironmentResolverConfig struct {
	AgentProfileID string
	WorkspaceRoot  string
	Variables      []string
	RoleVariables  map[worker.Role][]string
}

func NewRootedEnvironmentResolver(
	config RootedEnvironmentResolverConfig,
) (*RootedEnvironmentResolver, error) {
	root, err := canonicalDirectory(config.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace root: %v", ErrInvalidEnvironmentResolver, err)
	}
	variables := cloneVariables(config.Variables)
	probe := worker.LaunchEnvironment{
		AgentProfileID:   config.AgentProfileID,
		ProjectID:        "project_validation",
		FeatureID:        "feature_validation",
		Role:             worker.RoleCoder,
		WorkspaceID:      "workspace_validation",
		WorkingDirectory: root,
		Variables:        variables,
	}
	if err := probe.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidEnvironmentResolver, err)
	}
	roleVariables := make(map[worker.Role][]string, len(config.RoleVariables))
	for role, configured := range config.RoleVariables {
		if !role.IsValid() {
			return nil, fmt.Errorf(
				"%w: role %q is not recognized", ErrInvalidEnvironmentResolver, role,
			)
		}
		combined := append(cloneVariables(variables), configured...)
		roleProbe := probe
		roleProbe.Role = role
		roleProbe.Variables = combined
		if err := roleProbe.Validate(); err != nil {
			return nil, fmt.Errorf("%w: role %q: %v", ErrInvalidEnvironmentResolver, role, err)
		}
		roleVariables[role] = cloneVariables(configured)
	}
	return &RootedEnvironmentResolver{
		agentProfileID: config.AgentProfileID,
		workspaceRoot:  root,
		variables:      variables,
		roleVariables:  roleVariables,
	}, nil
}

func (resolver *RootedEnvironmentResolver) Resolve(
	ctx context.Context,
	assignment workerhttp.Assignment,
) (worker.LaunchEnvironment, error) {
	if resolver == nil {
		return worker.LaunchEnvironment{}, fmt.Errorf("%w: resolver is nil", ErrInvalidEnvironmentResolver)
	}
	if ctx == nil {
		return worker.LaunchEnvironment{}, fmt.Errorf("%w: context is required", ErrInvalidEnvironmentResolver)
	}
	if err := ctx.Err(); err != nil {
		return worker.LaunchEnvironment{}, err
	}
	if err := assignment.Validate(); err != nil {
		return worker.LaunchEnvironment{}, fmt.Errorf("%w: %v", ErrConfigurationMismatch, err)
	}
	if assignment.AgentProfileID != resolver.agentProfileID {
		return worker.LaunchEnvironment{}, fmt.Errorf(
			"%w: profile %q is not mounted by this worker",
			ErrProfileUnavailable,
			assignment.AgentProfileID,
		)
	}

	directory, err := canonicalDirectory(filepath.Join(resolver.workspaceRoot, assignment.WorkspaceID))
	if err != nil || !directoryWithin(resolver.workspaceRoot, directory) {
		return worker.LaunchEnvironment{}, fmt.Errorf(
			"%w: workspace %q cannot be opened inside the configured root",
			ErrWorkspaceUnavailable,
			assignment.WorkspaceID,
		)
	}
	variables := cloneVariables(resolver.variables)
	variables = append(variables, resolver.roleVariables[worker.Role(assignment.Role)]...)
	environment := launchEnvironment(assignment, directory, variables)
	if err := environment.Validate(); err != nil {
		return worker.LaunchEnvironment{}, fmt.Errorf("%w: %v", ErrConfigurationMismatch, err)
	}
	return environment, nil
}

func launchEnvironment(
	assignment workerhttp.Assignment,
	directory string,
	variables []string,
) worker.LaunchEnvironment {
	return worker.LaunchEnvironment{
		AgentProfileID:   assignment.AgentProfileID,
		ProjectID:        assignment.ProjectID,
		FeatureID:        assignment.FeatureID,
		Role:             worker.Role(assignment.Role),
		WorkspaceID:      assignment.WorkspaceID,
		WorkingDirectory: directory,
		Variables:        cloneVariables(variables),
	}
}

func launchEnvironmentMatches(
	environment worker.LaunchEnvironment,
	assignment workerhttp.Assignment,
) bool {
	return environment.AgentProfileID == assignment.AgentProfileID &&
		environment.ProjectID == assignment.ProjectID &&
		environment.FeatureID == assignment.FeatureID &&
		environment.Role == worker.Role(assignment.Role) &&
		environment.WorkspaceID == assignment.WorkspaceID
}

func environmentFailure(err error) (workerhttp.ErrorCode, string, bool) {
	switch {
	case errors.Is(err, ErrProfileUnavailable):
		return workerhttp.ErrorProfileUnavailable, "assigned agent profile is unavailable", true
	case errors.Is(err, ErrWorkspaceUnavailable):
		return workerhttp.ErrorWorkspaceUnavailable, "assigned workspace is unavailable", true
	case errors.Is(err, ErrConfigurationMismatch):
		return workerhttp.ErrorConfigurationMismatch, "launch environment does not match the requested assignment", false
	default:
		return workerhttp.ErrorInternal, "worker could not resolve the launch environment", true
	}
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("directory must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return canonical, nil
}

func directoryWithin(root string, directory string) bool {
	relative, err := filepath.Rel(root, directory)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func cloneVariables(variables []string) []string {
	if variables == nil {
		return nil
	}
	cloned := make([]string, len(variables))
	copy(cloned, variables)
	return cloned
}
