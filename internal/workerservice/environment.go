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
	ErrConfigurationMismatch      = errors.New("worker materialized configuration does not match the assignment")
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

// MaterializedEnvironment is one trusted-host-produced launch manifest. Its
// assignment contains references and a digest, never secret values.
type MaterializedEnvironment struct {
	Assignment       workerhttp.Assignment
	WorkingDirectory string
	Variables        []string
}

type ImmutableEnvironmentResolverConfig struct {
	AgentProfileID   string
	WorkspaceRoot    string
	Materializations []MaterializedEnvironment
}

// ImmutableEnvironmentResolver takes a defensive snapshot of already
// materialized environments. A worker can therefore launch or recover only
// the exact configuration revision and manifest digest it was given at setup.
type ImmutableEnvironmentResolver struct {
	agentProfileID string
	byWorkspace    map[string]worker.LaunchEnvironment
}

func NewImmutableEnvironmentResolver(
	config ImmutableEnvironmentResolverConfig,
) (*ImmutableEnvironmentResolver, error) {
	profileID := config.AgentProfileID
	if len(config.Materializations) == 0 {
		return nil, fmt.Errorf("%w: at least one materialization is required", ErrInvalidEnvironmentResolver)
	}
	root, err := canonicalDirectory(config.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace root: %v", ErrInvalidEnvironmentResolver, err)
	}
	resolved := &ImmutableEnvironmentResolver{
		agentProfileID: profileID,
		byWorkspace:    make(map[string]worker.LaunchEnvironment, len(config.Materializations)),
	}
	for _, materialization := range config.Materializations {
		if err := materialization.Assignment.Validate(); err != nil {
			return nil, fmt.Errorf("%w: assignment: %v", ErrInvalidEnvironmentResolver, err)
		}
		if materialization.Assignment.AgentProfileID != profileID {
			return nil, fmt.Errorf(
				"%w: workspace %q belongs to a different agent profile",
				ErrInvalidEnvironmentResolver,
				materialization.Assignment.WorkspaceID,
			)
		}
		directory, err := canonicalDirectory(materialization.WorkingDirectory)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: workspace %q: %v",
				ErrInvalidEnvironmentResolver,
				materialization.Assignment.WorkspaceID,
				err,
			)
		}
		if !directoryWithin(root, directory) {
			return nil, fmt.Errorf(
				"%w: workspace %q is outside the configured root",
				ErrInvalidEnvironmentResolver,
				materialization.Assignment.WorkspaceID,
			)
		}
		environment := launchEnvironment(materialization.Assignment, directory, materialization.Variables)
		if err := environment.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidEnvironmentResolver, err)
		}
		workspaceID := materialization.Assignment.WorkspaceID
		if _, exists := resolved.byWorkspace[workspaceID]; exists {
			return nil, fmt.Errorf(
				"%w: workspace %q is duplicated",
				ErrInvalidEnvironmentResolver,
				workspaceID,
			)
		}
		resolved.byWorkspace[workspaceID] = environment.Clone()
	}
	return resolved, nil
}

func (resolver *ImmutableEnvironmentResolver) Resolve(
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
	environment, exists := resolver.byWorkspace[assignment.WorkspaceID]
	if !exists {
		return worker.LaunchEnvironment{}, fmt.Errorf(
			"%w: workspace %q is not materialized",
			ErrWorkspaceUnavailable,
			assignment.WorkspaceID,
		)
	}
	if !launchEnvironmentMatches(environment, assignment) {
		return worker.LaunchEnvironment{}, fmt.Errorf(
			"%w: workspace %q revision or assignment changed",
			ErrConfigurationMismatch,
			assignment.WorkspaceID,
		)
	}
	info, err := os.Stat(environment.WorkingDirectory)
	if err != nil || !info.IsDir() {
		return worker.LaunchEnvironment{}, fmt.Errorf(
			"%w: workspace %q cannot be opened",
			ErrWorkspaceUnavailable,
			assignment.WorkspaceID,
		)
	}
	return environment.Clone(), nil
}

func launchEnvironment(
	assignment workerhttp.Assignment,
	directory string,
	variables []string,
) worker.LaunchEnvironment {
	return worker.LaunchEnvironment{
		AgentProfileID:        assignment.AgentProfileID,
		ProjectID:             assignment.ProjectID,
		FeatureID:             assignment.FeatureID,
		Role:                  worker.Role(assignment.Role),
		WorkspaceID:           assignment.WorkspaceID,
		ConfigurationRevision: assignment.ConfigurationRevision,
		MaterializationDigest: assignment.MaterializationDigest,
		WorkingDirectory:      directory,
		Variables:             cloneVariables(variables),
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
		environment.WorkspaceID == assignment.WorkspaceID &&
		environment.ConfigurationRevision == assignment.ConfigurationRevision &&
		environment.MaterializationDigest == assignment.MaterializationDigest
}

func environmentFailure(err error) (workerhttp.ErrorCode, string, bool) {
	switch {
	case errors.Is(err, ErrProfileUnavailable):
		return workerhttp.ErrorProfileUnavailable, "assigned agent profile is unavailable", true
	case errors.Is(err, ErrWorkspaceUnavailable):
		return workerhttp.ErrorWorkspaceUnavailable, "assigned workspace is unavailable", true
	case errors.Is(err, ErrConfigurationMismatch):
		return workerhttp.ErrorConfigurationMismatch, "materialized configuration does not match the requested assignment", false
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
