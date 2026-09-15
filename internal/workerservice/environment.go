package workerservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

var (
	ErrInvalidEnvironmentResolver = errors.New("invalid worker environment resolver")
	ErrProfileUnavailable         = errors.New("worker agent profile is unavailable")
	ErrWorkspaceUnavailable       = errors.New("worker workspace is unavailable")
	ErrToolchainUnavailable       = errors.New("project toolchain is unavailable")
	ErrConfigurationMismatch      = errors.New("worker launch environment does not match the assignment")
)

const defaultToolchainInstallTimeout = 30 * time.Minute

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
	toolchainRoot  string
	miseExecutable string
	installTimeout time.Duration
}

type RootedEnvironmentResolverConfig struct {
	AgentProfileID string
	WorkspaceRoot  string
	Variables      []string
	RoleVariables  map[worker.Role][]string
	ToolchainRoot  string
	MiseExecutable string
	InstallTimeout time.Duration
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
	toolchainRoot := strings.TrimSpace(config.ToolchainRoot)
	if toolchainRoot != "" {
		toolchainRoot, err = canonicalDirectory(toolchainRoot)
		if err != nil {
			return nil, fmt.Errorf("%w: toolchain root: %v", ErrInvalidEnvironmentResolver, err)
		}
	}
	miseExecutable := strings.TrimSpace(config.MiseExecutable)
	if miseExecutable == "" {
		miseExecutable = "mise"
	}
	installTimeout := config.InstallTimeout
	if installTimeout <= 0 {
		installTimeout = defaultToolchainInstallTimeout
	}
	return &RootedEnvironmentResolver{
		agentProfileID: config.AgentProfileID,
		workspaceRoot:  root,
		variables:      variables,
		roleVariables:  roleVariables,
		toolchainRoot:  toolchainRoot,
		miseExecutable: miseExecutable,
		installTimeout: installTimeout,
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
	if resolver.toolchainRoot != "" {
		variables, err = resolver.prepareToolchain(ctx, assignment.ProjectID, directory, variables)
		if err != nil {
			return worker.LaunchEnvironment{}, err
		}
	}
	environment := launchEnvironment(assignment, directory, variables)
	if err := environment.Validate(); err != nil {
		return worker.LaunchEnvironment{}, fmt.Errorf("%w: %v", ErrConfigurationMismatch, err)
	}
	return environment, nil
}

func (resolver *RootedEnvironmentResolver) prepareToolchain(
	ctx context.Context,
	projectID string,
	workingDirectory string,
	variables []string,
) ([]string, error) {
	projectRoot := filepath.Join(resolver.toolchainRoot, "projects", projectID)
	if !directoryWithin(resolver.toolchainRoot, projectRoot) {
		return nil, fmt.Errorf("%w: project identity escapes the toolchain root", ErrConfigurationMismatch)
	}
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		return nil, fmt.Errorf("%w: project toolchain directory cannot be created", ErrToolchainUnavailable)
	}
	configPath := filepath.Join(projectRoot, "mise.toml")
	dataPath := filepath.Join(resolver.toolchainRoot, "data")
	variables = setVariable(variables, "PATH", filepath.Join(dataPath, "shims")+":/usr/local/bin:/usr/bin:/bin")
	for name, value := range map[string]string{
		"COMMITARIUM_TOOLCHAIN_CONFIG":          configPath,
		"MISE_CACHE_DIR":                        filepath.Join(resolver.toolchainRoot, "cache"),
		"MISE_DATA_DIR":                         dataPath,
		"MISE_GLOBAL_CONFIG_FILE":               configPath,
		"MISE_GLOBAL_CONFIG_ROOT":               projectRoot,
		"MISE_IGNORED_CONFIG_PATHS":             workingDirectory,
		"MISE_OVERRIDE_TOOL_VERSIONS_FILENAMES": "none",
		"MISE_PYTHON_COMPILE":                   "0",
		"MISE_SAFE":                             "1",
		"MISE_STATE_DIR":                        filepath.Join(resolver.toolchainRoot, "state"),
		"MISE_JOBS":                             "4",
	} {
		variables = setVariable(variables, name, value)
	}
	info, err := os.Stat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return variables, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: generated mise config cannot be read", ErrToolchainUnavailable)
	}
	lock, err := os.OpenFile(configPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: project toolchain lock cannot be opened", ErrToolchainUnavailable)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("%w: project toolchain cannot be locked", ErrToolchainUnavailable)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if configured, err := toolchain.ReadGeneratedConfig(configPath); err != nil || len(configured) == 0 {
		return nil, fmt.Errorf("%w: generated mise config is unsafe or empty", ErrToolchainUnavailable)
	}
	installContext, cancel := context.WithTimeout(ctx, resolver.installTimeout)
	defer cancel()
	command := exec.CommandContext(installContext, resolver.miseExecutable, "install", "--yes")
	command.Dir = workingDirectory
	command.Env = variables
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if errors.Is(installContext.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: installation exceeded %s", ErrToolchainUnavailable, resolver.installTimeout)
		}
		return nil, fmt.Errorf("%w: mise install failed: %v", ErrToolchainUnavailable, err)
	}
	return variables, nil
}

func setVariable(variables []string, name string, value string) []string {
	prefix := name + "="
	for index, variable := range variables {
		if strings.HasPrefix(variable, prefix) {
			variables[index] = prefix + value
			return variables
		}
	}
	return append(variables, prefix+value)
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
	case errors.Is(err, ErrToolchainUnavailable):
		return workerhttp.ErrorToolchainUnavailable, "assigned project toolchain is unavailable", true
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
