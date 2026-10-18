package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/EinarLogiOskars/commitarium/internal/claudeadapter"
	"github.com/EinarLogiOskars/commitarium/internal/codexadapter"
	"github.com/EinarLogiOskars/commitarium/internal/processsupervisor"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/secretfile"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workerjournal"
	"github.com/EinarLogiOskars/commitarium/internal/workerservice"
)

const (
	defaultListenAddress = ":8081"
	defaultStepDelay     = 250 * time.Millisecond
	defaultHealthURL     = "http://127.0.0.1:8081/internal/v1/health"
	defaultActivityURL   = "http://127.0.0.1:8081/internal/v1/activity"
	defaultAdapter       = "simulated"
	defaultCodexBinary   = "/usr/local/bin/codex"
	defaultCodexSandbox  = "read-only"
	defaultClaudeBinary  = "/usr/local/bin/claude"
	defaultClaudeMode    = "plan"
)

type config struct {
	databasePath  string
	listenAddress string
	bearerToken   string
	adapter       string
	stepDelay     time.Duration
	codex         codexConfig
	claude        claudeConfig
}

type codexConfig struct {
	executable        string
	model             string
	availableModels   []string
	sandbox           string
	providerStatePath string
	workspaceRoot     string
	profileID         string
	forgejoURL        string
	identities        map[worker.Role]forgejoIdentity
	toolchainRoot     string
	coordinatorURL    string
}

type claudeConfig struct {
	executable        string
	model             string
	availableModels   []string
	permissionMode    string
	providerStatePath string
	workspaceRoot     string
	profileID         string
	forgejoURL        string
	identities        map[worker.Role]forgejoIdentity
	toolchainRoot     string
	coordinatorURL    string
}

type runtime struct {
	provider            worker.Adapter
	providerKind        workerhttp.Provider
	environmentResolver workerservice.EnvironmentResolver
	capabilities        []workerhttp.Capability
	modelSource         workerhttp.ModelSource
	usage               *worker.UsageTracker
	description         string
	// cliExecutable is the provider CLI whose version the worker reports,
	// when it is the one the desktop keeps updated.
	cliExecutable string
	// secrets are materialized credential values the worker removes from
	// activity before it is spooled (ADR-006).
	secrets []string
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := checkHealth(context.Background(), defaultHealthURL); err != nil {
			log.Fatal(err)
		}
		return
	}
	// The desktop asks, through docker exec, whether restarting this worker
	// would cut off a turn. Asked for JSON (an environment variable, which
	// workers before it ignore), it also learns the CLI version the worker
	// started with; otherwise the answer stays the bare attempt count.
	if len(os.Args) == 2 && os.Args[1] == "activity" {
		token, err := secretfile.RequiredPath(os.Getenv, "COMMITARIUM_WORKER_TOKEN_FILE", "worker API token")
		if err != nil {
			log.Fatal(err)
		}
		activity, err := checkActivity(context.Background(), defaultActivityURL, token)
		if err != nil {
			log.Fatal(err)
		}
		if os.Getenv("COMMITARIUM_ACTIVITY_FORMAT") == "json" {
			if err := json.NewEncoder(os.Stdout).Encode(activity); err != nil {
				log.Fatal(err)
			}
			return
		}
		fmt.Println(activity.Attempts)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	workerConfig, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if err := run(ctx, workerConfig); err != nil {
		log.Fatal(err)
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	databasePath := strings.TrimSpace(getenv("COMMITARIUM_WORKER_DATABASE_PATH"))
	if databasePath == "" {
		return config{}, errors.New("COMMITARIUM_WORKER_DATABASE_PATH is required")
	}
	bearerToken, err := secretfile.RequiredPath(
		getenv,
		"COMMITARIUM_WORKER_TOKEN_FILE",
		"worker API token",
	)
	if err != nil {
		return config{}, err
	}
	listenAddress := strings.TrimSpace(getenv("COMMITARIUM_WORKER_LISTEN_ADDRESS"))
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}
	adapter := strings.TrimSpace(getenv("COMMITARIUM_WORKER_ADAPTER"))
	if adapter == "" {
		adapter = defaultAdapter
	}
	if adapter != "simulated" && adapter != "codex" && adapter != "claude_code" {
		return config{}, errors.New("COMMITARIUM_WORKER_ADAPTER must be simulated, codex, or claude_code")
	}
	stepDelay := defaultStepDelay
	if value := strings.TrimSpace(getenv("COMMITARIUM_WORKER_STEP_DELAY")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 0 {
			return config{}, fmt.Errorf("COMMITARIUM_WORKER_STEP_DELAY must be a nonnegative duration")
		}
		stepDelay = parsed
	}
	loaded := config{
		databasePath:  databasePath,
		listenAddress: listenAddress,
		bearerToken:   bearerToken,
		adapter:       adapter,
		stepDelay:     stepDelay,
	}
	if adapter == "codex" {
		codex, err := loadCodexConfig(getenv)
		if err != nil {
			return config{}, err
		}
		loaded.codex = codex
	} else if adapter == "claude_code" {
		claude, err := loadClaudeConfig(getenv)
		if err != nil {
			return config{}, err
		}
		loaded.claude = claude
	}
	return loaded, nil
}

func run(ctx context.Context, workerConfig config) error {
	db, err := workerjournal.OpenSQLite(ctx, workerConfig.databasePath)
	if err != nil {
		return fmt.Errorf("open worker journal: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("close worker journal: %v", err)
		}
	}()
	if err := workerjournal.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate worker journal: %w", err)
	}

	workerRuntime, err := newRuntime(workerConfig)
	if err != nil {
		return err
	}
	service, recovery, err := workerservice.New(ctx, workerservice.Config{
		Journal:             workerjournal.NewStore(db),
		Provider:            workerRuntime.provider,
		EnvironmentResolver: workerRuntime.environmentResolver,
		Normalizer:          redactingNormalizer(workerRuntime.secrets),
		Lifetime:            ctx,
	})
	if err != nil {
		return fmt.Errorf("create worker service: %w", err)
	}
	if recovery.AttemptsResumed > 0 || recovery.AttemptsMarked > 0 || recovery.MutationsMarked > 0 {
		log.Printf(
			"worker startup resumed %d attempt(s) and marked %d attempt(s) and %d command(s) indeterminate",
			recovery.AttemptsResumed,
			recovery.AttemptsMarked,
			recovery.MutationsMarked,
		)
	}
	handler, err := workerhttp.NewServer(workerhttp.ServerConfig{
		BearerToken:  workerConfig.bearerToken,
		Provider:     workerRuntime.providerKind,
		Capabilities: workerRuntime.capabilities,
		// Each attempt is its own provider process; one agent serves several
		// work orders at once (ADR-016).
		MaxConcurrentAttempts: 0,
		EventSource:           service,
		ModelSource:           workerRuntime.modelSource,
		UsageSource:           usageSource(workerRuntime.usage),
		ActivitySource:        service.ActiveAttempts,
		CLIVersion:            startupCLIVersion(ctx, workerRuntime.cliExecutable),
	}, service)
	if err != nil {
		return fmt.Errorf("create worker HTTP server: %w", err)
	}
	httpServer := &http.Server{
		Addr:              workerConfig.listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() {
		log.Printf("%s listening on %s", workerRuntime.description, workerConfig.listenAddress)
		serveErrors <- httpServer.ListenAndServe()
	}()
	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve worker API: %w", err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shut down worker API: %w", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve worker API during shutdown: %w", err)
		}
		return nil
	}
}

func newRuntime(workerConfig config) (runtime, error) {
	switch workerConfig.adapter {
	case "simulated":
		workingDirectory, err := os.Getwd()
		if err != nil {
			return runtime{}, fmt.Errorf("resolve simulated worker directory: %w", err)
		}
		scripted := worker.NewRepeatingScriptedAdapter("codex-simulated", simulatedScripts())
		return runtime{
			provider:            worker.NewAutomaticScriptedAdapter(scripted, workerConfig.stepDelay),
			providerKind:        workerhttp.ProviderCodex,
			environmentResolver: simulatedEnvironmentResolver(workingDirectory),
			capabilities: []workerhttp.Capability{
				workerhttp.CapabilityStart,
				workerhttp.CapabilityResume,
				workerhttp.CapabilityMessage,
				workerhttp.CapabilityPause,
				workerhttp.CapabilityContinue,
				workerhttp.CapabilityCooperativeStop,
				workerhttp.CapabilityEventReplay,
			},
			description: "simulated Codex worker",
			modelSource: staticModelSource("codex-simulated-v1"),
		}, nil
	case "codex":
		return newCodexRuntime(workerConfig.codex)
	case "claude_code":
		return newClaudeRuntime(workerConfig.claude)
	default:
		return runtime{}, fmt.Errorf("unsupported worker adapter %q", workerConfig.adapter)
	}
}

func loadCodexConfig(getenv func(string) string) (codexConfig, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			return "", fmt.Errorf("%s is required for the Codex adapter", name)
		}
		return value, nil
	}
	providerStatePath, err := required("COMMITARIUM_CODEX_PROVIDER_STATE_PATH")
	if err != nil {
		return codexConfig{}, err
	}
	if !filepath.IsAbs(providerStatePath) {
		return codexConfig{}, errors.New("COMMITARIUM_CODEX_PROVIDER_STATE_PATH must be absolute")
	}
	workspaceRoot, err := required("COMMITARIUM_CODEX_WORKSPACE_ROOT")
	if err != nil {
		return codexConfig{}, err
	}
	profileID, err := required("COMMITARIUM_CODEX_PROFILE_ID")
	if err != nil {
		return codexConfig{}, err
	}
	forgejoURL, err := required("COMMITARIUM_CODEX_FORGEJO_URL")
	if err != nil {
		return codexConfig{}, err
	}
	parsedForgejoURL, err := url.Parse(strings.TrimRight(forgejoURL, "/"))
	if err != nil || (parsedForgejoURL.Scheme != "http" && parsedForgejoURL.Scheme != "https") ||
		parsedForgejoURL.Host == "" || parsedForgejoURL.User != nil ||
		parsedForgejoURL.RawQuery != "" || parsedForgejoURL.Fragment != "" {
		return codexConfig{}, errors.New("COMMITARIUM_CODEX_FORGEJO_URL must be an absolute HTTP URL without credentials, query, or fragment")
	}
	identities, err := loadForgejoIdentities("COMMITARIUM_CODEX", "Codex", getenv, workspaceRoot)
	if err != nil {
		return codexConfig{}, err
	}
	executable := strings.TrimSpace(getenv("COMMITARIUM_CODEX_EXECUTABLE"))
	if executable == "" {
		executable = preferUpdatedCLI(defaultCodexBinary)
	}
	sandbox := strings.TrimSpace(getenv("COMMITARIUM_CODEX_SANDBOX"))
	if sandbox == "" {
		sandbox = defaultCodexSandbox
	}
	if sandbox != "read-only" && sandbox != "workspace-write" && sandbox != "danger-full-access" {
		return codexConfig{}, errors.New(
			"COMMITARIUM_CODEX_SANDBOX must be read-only, workspace-write, or danger-full-access",
		)
	}
	availableModels, err := parseAvailableModels(
		getenv("COMMITARIUM_CODEX_AVAILABLE_MODELS"),
		strings.TrimSpace(getenv("COMMITARIUM_CODEX_MODEL")),
	)
	if err != nil {
		return codexConfig{}, fmt.Errorf("COMMITARIUM_CODEX_AVAILABLE_MODELS: %w", err)
	}
	toolchainRoot := strings.TrimSpace(getenv("COMMITARIUM_TOOLCHAIN_ROOT"))
	if toolchainRoot != "" && !filepath.IsAbs(toolchainRoot) {
		return codexConfig{}, errors.New("COMMITARIUM_TOOLCHAIN_ROOT must be absolute")
	}
	coordinatorURL, err := optionalHTTPURL(getenv("COMMITARIUM_COORDINATOR_URL"))
	if err != nil {
		return codexConfig{}, fmt.Errorf("COMMITARIUM_COORDINATOR_URL: %w", err)
	}
	return codexConfig{
		executable: executable, model: strings.TrimSpace(getenv("COMMITARIUM_CODEX_MODEL")),
		availableModels: availableModels,
		sandbox:         sandbox, providerStatePath: providerStatePath,
		workspaceRoot: workspaceRoot, profileID: profileID,
		forgejoURL: strings.TrimRight(forgejoURL, "/"), identities: identities,
		toolchainRoot: toolchainRoot, coordinatorURL: coordinatorURL,
	}, nil
}

func loadClaudeConfig(getenv func(string) string) (claudeConfig, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			return "", fmt.Errorf("%s is required for the Claude adapter", name)
		}
		return value, nil
	}
	providerStatePath, err := required("COMMITARIUM_CLAUDE_PROVIDER_STATE_PATH")
	if err != nil {
		return claudeConfig{}, err
	}
	if !filepath.IsAbs(providerStatePath) {
		return claudeConfig{}, errors.New("COMMITARIUM_CLAUDE_PROVIDER_STATE_PATH must be absolute")
	}
	workspaceRoot, err := required("COMMITARIUM_CLAUDE_WORKSPACE_ROOT")
	if err != nil {
		return claudeConfig{}, err
	}
	profileID, err := required("COMMITARIUM_CLAUDE_PROFILE_ID")
	if err != nil {
		return claudeConfig{}, err
	}
	forgejoURL, err := required("COMMITARIUM_CLAUDE_FORGEJO_URL")
	if err != nil {
		return claudeConfig{}, err
	}
	parsedForgejoURL, err := url.Parse(strings.TrimRight(forgejoURL, "/"))
	if err != nil || (parsedForgejoURL.Scheme != "http" && parsedForgejoURL.Scheme != "https") ||
		parsedForgejoURL.Host == "" || parsedForgejoURL.User != nil ||
		parsedForgejoURL.RawQuery != "" || parsedForgejoURL.Fragment != "" {
		return claudeConfig{}, errors.New("COMMITARIUM_CLAUDE_FORGEJO_URL must be an absolute HTTP URL without credentials, query, or fragment")
	}
	identities, err := loadForgejoIdentities("COMMITARIUM_CLAUDE", "Claude", getenv, workspaceRoot)
	if err != nil {
		return claudeConfig{}, err
	}
	executable := strings.TrimSpace(getenv("COMMITARIUM_CLAUDE_EXECUTABLE"))
	if executable == "" {
		executable = preferUpdatedCLI(defaultClaudeBinary)
	}
	permissionMode := strings.TrimSpace(getenv("COMMITARIUM_CLAUDE_PERMISSION_MODE"))
	if permissionMode == "" {
		permissionMode = defaultClaudeMode
	}
	if permissionMode != "plan" && permissionMode != "acceptEdits" &&
		permissionMode != "bypassPermissions" && permissionMode != "dontAsk" &&
		permissionMode != "auto" && permissionMode != "manual" {
		return claudeConfig{}, errors.New(
			"COMMITARIUM_CLAUDE_PERMISSION_MODE must be plan, acceptEdits, bypassPermissions, dontAsk, auto, or manual",
		)
	}
	availableModels, err := parseAvailableModels(
		getenv("COMMITARIUM_CLAUDE_AVAILABLE_MODELS"),
		strings.TrimSpace(getenv("COMMITARIUM_CLAUDE_MODEL")),
	)
	if err != nil {
		return claudeConfig{}, fmt.Errorf("COMMITARIUM_CLAUDE_AVAILABLE_MODELS: %w", err)
	}
	toolchainRoot := strings.TrimSpace(getenv("COMMITARIUM_TOOLCHAIN_ROOT"))
	if toolchainRoot != "" && !filepath.IsAbs(toolchainRoot) {
		return claudeConfig{}, errors.New("COMMITARIUM_TOOLCHAIN_ROOT must be absolute")
	}
	coordinatorURL, err := optionalHTTPURL(getenv("COMMITARIUM_COORDINATOR_URL"))
	if err != nil {
		return claudeConfig{}, fmt.Errorf("COMMITARIUM_COORDINATOR_URL: %w", err)
	}
	return claudeConfig{
		executable: executable, model: strings.TrimSpace(getenv("COMMITARIUM_CLAUDE_MODEL")),
		availableModels: availableModels,
		permissionMode:  permissionMode, providerStatePath: providerStatePath,
		workspaceRoot: workspaceRoot, profileID: profileID,
		forgejoURL: strings.TrimRight(forgejoURL, "/"), identities: identities,
		toolchainRoot: toolchainRoot, coordinatorURL: coordinatorURL,
	}, nil
}

func newCodexRuntime(config codexConfig) (runtime, error) {
	providerState, err := os.Stat(config.providerStatePath)
	if err != nil || !providerState.IsDir() {
		return runtime{}, errors.New("Codex provider-state directory is unavailable")
	}
	roleVariables, roleWorkspaceRoots, tokens, err := forgejoRoleEnvironment(config.forgejoURL, config.identities, "Codex")
	if err != nil {
		return runtime{}, err
	}
	baseVariables := []string{
		"CODEX_HOME=" + config.providerStatePath,
		"HOME=" + config.providerStatePath,
		"LANG=C.UTF-8",
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	if config.coordinatorURL != "" {
		baseVariables = append(baseVariables, "COMMITARIUM_COORDINATOR_URL="+config.coordinatorURL)
	}
	resolver, err := workerservice.NewRootedEnvironmentResolver(
		workerservice.RootedEnvironmentResolverConfig{
			AgentProfileID:     config.profileID,
			WorkspaceRoot:      config.workspaceRoot,
			ToolchainRoot:      config.toolchainRoot,
			Variables:          baseVariables,
			RoleVariables:      roleVariables,
			RoleWorkspaceRoots: roleWorkspaceRoots,
		},
	)
	if err != nil {
		return runtime{}, fmt.Errorf("create Codex launch environment: %w", err)
	}
	provider, err := codexadapter.New(codexadapter.Config{
		Supervisor: processsupervisor.New(), Executable: config.executable,
		Arguments: []string{
			"app-server", "--listen", "stdio://",
			"-c", `cli_auth_credentials_store="file"`,
		},
		Model: config.model, ApprovalPolicy: "never", Sandbox: config.sandbox,
	})
	if err != nil {
		return runtime{}, fmt.Errorf("create Codex adapter: %w", err)
	}
	modelSource := workerhttp.ModelSourceFunc(func(ctx context.Context) ([]workerhttp.Model, error) {
		discovered, err := provider.Models(ctx, config.providerStatePath, []string{
			"CODEX_HOME=" + config.providerStatePath,
			"HOME=" + config.providerStatePath,
			"LANG=C.UTF-8",
			"PATH=/usr/local/bin:/usr/bin:/bin",
		})
		if err != nil {
			return nil, err
		}
		models := make([]workerhttp.Model, 0, len(discovered)+len(config.availableModels))
		seen := make(map[string]struct{}, len(discovered)+len(config.availableModels))
		appendModel := func(id, display, defaultEffort string, efforts []string) {
			if project.ValidateExactModelID(id) != nil {
				return
			}
			if _, exists := seen[id]; exists {
				return
			}
			seen[id] = struct{}{}
			models = append(models, workerhttp.Model{ID: id, DisplayName: display,
				DefaultReasoningEffort: defaultEffort, SupportedReasoningEfforts: efforts})
		}
		for _, model := range discovered {
			appendModel(model.ID, model.DisplayName, model.DefaultReasoningEffort, model.SupportedReasoningEfforts)
		}
		for _, id := range config.availableModels {
			appendModel(id, id, "", nil)
		}
		return models, nil
	})
	return runtime{
		provider: provider, providerKind: workerhttp.ProviderCodex, environmentResolver: resolver,
		capabilities: []workerhttp.Capability{
			workerhttp.CapabilityStart,
			workerhttp.CapabilityResume,
			workerhttp.CapabilityMessage,
			workerhttp.CapabilityCooperativeStop,
			workerhttp.CapabilityForceStop,
			workerhttp.CapabilityEventReplay,
		},
		description:   "Codex worker",
		secrets:       tokens,
		modelSource:   modelSource,
		usage:         &codexadapter.Usage,
		cliExecutable: updatableCLI(config.executable, defaultCodexBinary),
	}, nil
}

func newClaudeRuntime(config claudeConfig) (runtime, error) {
	providerState, err := os.Stat(config.providerStatePath)
	if err != nil || !providerState.IsDir() {
		return runtime{}, errors.New("Claude provider-state directory is unavailable")
	}
	roleVariables, roleWorkspaceRoots, tokens, err := forgejoRoleEnvironment(config.forgejoURL, config.identities, "Claude")
	if err != nil {
		return runtime{}, err
	}
	baseVariables := []string{
		"CLAUDE_CONFIG_DIR=" + config.providerStatePath,
		"HOME=" + config.providerStatePath,
		"LANG=C.UTF-8",
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	if config.coordinatorURL != "" {
		baseVariables = append(baseVariables, "COMMITARIUM_COORDINATOR_URL="+config.coordinatorURL)
	}
	resolver, err := workerservice.NewRootedEnvironmentResolver(
		workerservice.RootedEnvironmentResolverConfig{
			AgentProfileID:     config.profileID,
			WorkspaceRoot:      config.workspaceRoot,
			ToolchainRoot:      config.toolchainRoot,
			Variables:          baseVariables,
			RoleVariables:      roleVariables,
			RoleWorkspaceRoots: roleWorkspaceRoots,
		},
	)
	if err != nil {
		return runtime{}, fmt.Errorf("create Claude launch environment: %w", err)
	}
	provider, err := claudeadapter.New(claudeadapter.Config{
		Supervisor: processsupervisor.New(), Executable: config.executable,
		Model: config.model, PermissionMode: config.permissionMode,
	})
	if err != nil {
		return runtime{}, fmt.Errorf("create Claude adapter: %w", err)
	}
	return runtime{
		provider: provider, providerKind: workerhttp.ProviderClaudeCode,
		environmentResolver: resolver,
		capabilities: []workerhttp.Capability{
			workerhttp.CapabilityStart,
			workerhttp.CapabilityResume,
			workerhttp.CapabilityCooperativeStop,
			workerhttp.CapabilityForceStop,
			workerhttp.CapabilityEventReplay,
		},
		description:   "Claude worker",
		secrets:       tokens,
		modelSource:   claudeModelSource(provider, baseVariables, config.availableModels),
		usage:         &claudeadapter.Usage,
		cliExecutable: updatableCLI(config.executable, defaultClaudeBinary),
	}, nil
}

func parseAvailableModels(value string, configuredModel string) ([]string, error) {
	parts := strings.Split(value, ",")
	if strings.TrimSpace(value) == "" {
		parts = nil
	}
	if configuredModel != "" {
		parts = append(parts, configuredModel)
	}
	seen := make(map[string]struct{}, len(parts))
	models := make([]string, 0, len(parts))
	for _, part := range parts {
		model := strings.TrimSpace(part)
		if model == "" {
			continue
		}
		if err := project.ValidateExactModelID(model); err != nil {
			return nil, err
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	return models, nil
}

func optionalHTTPURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("must be an absolute HTTP URL without credentials, query, or fragment")
	}
	return value, nil
}

// updatedCLIDirectory holds provider CLIs the desktop updated after the image
// was built; a CLI found there is used instead of the image's when it runs.
const updatedCLIDirectory = "/opt/commitarium-cli/bin"

func preferUpdatedCLI(bundled string) string {
	return preferUpdatedCLIIn(updatedCLIDirectory, bundled)
}

// preferUpdatedCLIIn falls back to the bundled CLI when the updated one cannot
// even report its version, so a broken update cannot fail every turn. The
// desktop runs logins and version checks by the same rule (agents.rs).
func preferUpdatedCLIIn(directory string, bundled string) string {
	updated := filepath.Join(directory, filepath.Base(bundled))
	info, err := os.Stat(updated)
	if err != nil || info.IsDir() {
		return bundled
	}
	versionContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(versionContext, updated, "--version").Run(); err != nil {
		log.Printf("updated provider CLI %s does not run, using %s: %v", updated, bundled, err)
		return bundled
	}
	return updated
}

// updatableCLI is executable when it is the CLI the desktop keeps updated, or
// the image's one it falls back to, and empty when a configured executable
// overrides it: the desktop compares the reported version with its updated CLI
// and would otherwise restart forever.
func updatableCLI(executable string, bundled string) string {
	if executable != bundled && executable != filepath.Join(updatedCLIDirectory, filepath.Base(bundled)) {
		return ""
	}
	return executable
}

// startupCLIVersion is the version of the provider CLI this worker runs,
// read once at startup. Long-running provider state (resolved model aliases,
// a Codex app-server) keeps whatever it loaded, so a CLI installed later
// needs a restart; the desktop compares this with the installed one.
func startupCLIVersion(ctx context.Context, executable string) string {
	if executable == "" {
		return ""
	}
	versionContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionContext, executable, "--version").Output()
	if err != nil {
		log.Printf("read the provider CLI version: %v", err)
		return ""
	}
	return firstVersion(string(output))
}

// firstVersion is the first x.y.z version in a CLI's --version output, read
// the way the desktop reads it.
func firstVersion(output string) string {
	for _, word := range strings.Fields(output) {
		word = strings.TrimPrefix(word, "v")
		core, _, _ := strings.Cut(word, "+")
		core, _, _ = strings.Cut(core, "-")
		parts := strings.Split(core, ".")
		valid := len(parts) == 3
		for _, part := range parts {
			if _, err := strconv.ParseUint(part, 10, 64); err != nil {
				valid = false
			}
		}
		if valid {
			return word
		}
	}
	return ""
}

// claudeModelRefresh is how long resolved Claude models are reused.
const claudeModelRefresh = 24 * time.Hour

// claudeModelSource lists the models Claude Code's aliases resolve to, newest
// of each family, followed by the configured ones. Resolving runs Claude
// Code briefly, so it is cached; without a login the configured list stands.
func claudeModelSource(provider *claudeadapter.Adapter, environment []string, configured []string) workerhttp.ModelSource {
	var mu sync.Mutex
	var cached []workerhttp.Model
	var resolvedAt time.Time
	return workerhttp.ModelSourceFunc(func(ctx context.Context) ([]workerhttp.Model, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != nil && time.Since(resolvedAt) < claudeModelRefresh {
			return cached, nil
		}
		resolved, err := provider.ResolveModelAliases(ctx, os.TempDir(), environment, claudeadapter.ModelAliases)
		if err != nil {
			if cached != nil {
				return cached, nil
			}
			return withEfforts(staticModels(configured), claudeadapter.Efforts), nil
		}
		ids := make([]string, 0, len(resolved)+len(configured))
		seen := make(map[string]struct{}, len(resolved)+len(configured))
		for _, id := range append(append([]string(nil), resolved...), configured...) {
			if _, exists := seen[id]; exists || project.ValidateExactModelID(id) != nil {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		cached, resolvedAt = withEfforts(staticModels(ids), claudeadapter.Efforts), time.Now()
		return cached, nil
	})
}

// withEfforts lists the reasoning efforts each model accepts.
func withEfforts(models []workerhttp.Model, efforts []string) []workerhttp.Model {
	for index := range models {
		models[index].SupportedReasoningEfforts = append([]string(nil), efforts...)
	}
	return models
}

func staticModels(ids []string) []workerhttp.Model {
	models := make([]workerhttp.Model, len(ids))
	for index, id := range ids {
		models[index] = workerhttp.Model{ID: id, DisplayName: staticModelDisplayName(id)}
	}
	return models
}

func staticModelSource(ids ...string) workerhttp.ModelSource {
	return workerhttp.ModelSourceFunc(func(context.Context) ([]workerhttp.Model, error) {
		models := make([]workerhttp.Model, len(ids))
		for index, id := range ids {
			models[index] = workerhttp.Model{ID: id, DisplayName: staticModelDisplayName(id)}
		}
		return models, nil
	})
}

func staticModelDisplayName(id string) string {
	if displayName, ok := map[string]string{
		"claude-opus-5-5":           "Claude Opus 5.5",
		"claude-sonnet-5-5":         "Claude Sonnet 5.5",
		"claude-haiku-5-5":          "Claude Haiku 5.5",
		"claude-fable-5-1":          "Claude Fable 5.1",
		"claude-opus-5":             "Claude Opus 5",
		"claude-opus-4-8":           "Claude Opus 4.8",
		"claude-sonnet-5":           "Claude Sonnet 5",
		"claude-haiku-4-5-20251001": "Claude Haiku 4.5",
	}[id]; ok {
		return displayName
	}
	return claudeDisplayName(id)
}

// claudeDisplayName names a Claude model ID such as "claude-opus-5-2" or
// "claude-haiku-4-5-20251001" as "Claude Opus 5.2" or "Claude Haiku 4.5".
func claudeDisplayName(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "claude-"), "-")
	if !strings.HasPrefix(id, "claude-") || len(parts) < 2 {
		return id
	}
	if last := parts[len(parts)-1]; len(last) == 8 && strings.Trim(last, "0123456789") == "" {
		parts = parts[:len(parts)-1]
	}
	family := parts[0]
	if family == "" {
		return id
	}
	return "Claude " + strings.ToUpper(family[:1]) + family[1:] + " " + strings.Join(parts[1:], ".")
}

// The deterministic worker has no provider credentials or repository
// processes. It still fills the same launch contract so the service boundary
// is exercised without pretending that it owns a real workspace.
func simulatedEnvironmentResolver(directory string) workerservice.EnvironmentResolver {
	return workerservice.EnvironmentResolverFunc(func(
		ctx context.Context,
		assignment workerhttp.Assignment,
	) (worker.LaunchEnvironment, error) {
		if err := ctx.Err(); err != nil {
			return worker.LaunchEnvironment{}, err
		}
		return worker.LaunchEnvironment{
			AgentProfileID:   assignment.AgentProfileID,
			ProjectID:        assignment.ProjectID,
			FeatureID:        assignment.FeatureID,
			Role:             worker.Role(assignment.Role),
			WorkspaceID:      assignment.WorkspaceID,
			WorkingDirectory: directory,
			Variables:        []string{},
		}, nil
	})
}

func normalizeObservableEvent(
	_ context.Context,
	event worker.Event,
) (workerservice.NormalizedEvent, error) {
	switch event.Type {
	case worker.EventMessage,
		worker.EventPlanSubmitted,
		worker.EventActivity,
		worker.EventInputRequired,
		worker.EventPauseAcknowledged,
		worker.EventContinued,
		worker.EventRecoveryAssessment:
	default:
		return workerservice.NormalizedEvent{}, fmt.Errorf("unsupported observable event type %q", event.Type)
	}
	normalized := workerservice.NormalizedEvent{
		Type:      workerhttp.EventType(event.Type),
		Text:      event.Text,
		StreamID:  event.StreamID,
		Redaction: workerhttp.RedactionMetadata{},
	}
	if event.Activity != nil {
		normalized.Activity = &workerhttp.Activity{
			Kind:       workerhttp.ActivityKind(event.Activity.Kind),
			Command:    event.Activity.Command,
			ExitCode:   event.Activity.ExitCode,
			DurationMS: event.Activity.DurationMS,
			Operation:  workerhttp.FileOperation(event.Activity.Operation),
			Path:       event.Activity.Path,
			OldPath:    event.Activity.OldPath,
			Additions:  event.Activity.Additions,
			Deletions:  event.Activity.Deletions,
		}
	}
	if event.RecoveryAssessment != nil {
		normalized.RecoveryAssessment = &workerhttp.RecoveryAssessment{
			Consistent:         event.RecoveryAssessment.Consistent,
			RequiresUserReview: event.RecoveryAssessment.RequiresUserReview,
		}
	}
	return normalized, nil
}

// redactingNormalizer replaces every known secret value in an observable
// event with [REDACTED] before the event leaves the worker.
func redactingNormalizer(secrets []string) workerservice.NormalizerFunc {
	return func(ctx context.Context, event worker.Event) (workerservice.NormalizedEvent, error) {
		normalized, err := normalizeObservableEvent(ctx, event)
		if err != nil {
			return normalized, err
		}
		count := 0
		redact := func(value *string) {
			for _, secret := range secrets {
				if occurrences := strings.Count(*value, secret); occurrences > 0 {
					count += occurrences
					*value = strings.ReplaceAll(*value, secret, "[REDACTED]")
				}
			}
		}
		redact(&normalized.Text)
		if normalized.Activity != nil {
			redact(&normalized.Activity.Command)
			redact(&normalized.Activity.Path)
			redact(&normalized.Activity.OldPath)
		}
		if count > 0 {
			normalized.Redaction = workerhttp.RedactionMetadata{
				Count: count, Categories: []workerhttp.RedactionCategory{workerhttp.RedactionCredential},
			}
		}
		return normalized, nil
	}
}

func simulatedScripts() map[worker.Role]worker.Script {
	return map[worker.Role]worker.Script{
		worker.RoleLead: {
			Events: []worker.Event{
				{Type: worker.EventActivity, Text: "Inspecting the feature goal and durable workflow state."},
				{Type: worker.EventMessage, Text: "Deterministic planning lead completed its pass."},
			},
			Disposition: worker.DispositionSucceeded,
			Summary:     "deterministic planning lead completed",
		},
		worker.RoleConsultant: {
			Events: []worker.Event{
				{Type: worker.EventActivity, Text: "Reviewing the proposed plan for gaps and risks."},
				{Type: worker.EventMessage, Text: "Deterministic planning consultant completed its pass."},
			},
			Disposition: worker.DispositionSucceeded,
			Summary:     "deterministic planning consultant completed",
		},
		worker.RoleCoder: {
			Events: []worker.Event{
				{Type: worker.EventActivity, Text: "Inspecting the accepted implementation assignment."},
				{Type: worker.EventMessage, Text: "Deterministic implementation completed."},
			},
			Disposition: worker.DispositionSucceeded,
			Summary:     "deterministic implementation completed",
		},
		worker.RoleReviewer: {
			Events: []worker.Event{
				{Type: worker.EventActivity, Text: "Reviewing the deterministic implementation result."},
				{Type: worker.EventMessage, Text: "Deterministic review approved the result."},
			},
			Disposition: worker.DispositionSucceeded,
			Summary:     "deterministic review completed",
		},
	}
}

func checkHealth(ctx context.Context, healthURL string) error {
	requestContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, healthURL, nil)
	if err != nil {
		return fmt.Errorf("create worker health request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("request worker health: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("worker health returned HTTP %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024))
	decoder.DisallowUnknownFields()
	var health workerhttp.HealthResponse
	if err := decoder.Decode(&health); err != nil {
		return fmt.Errorf("decode worker health: %w", err)
	}
	if err := health.Validate(); err != nil {
		return fmt.Errorf("validate worker health: %w", err)
	}
	return nil
}

// checkActivity is how many attempts the worker is launching or running, and
// the CLI version it started with.
func checkActivity(ctx context.Context, activityURL string, token string) (workerhttp.ActivityResponse, error) {
	requestContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, activityURL, nil)
	if err != nil {
		return workerhttp.ActivityResponse{}, fmt.Errorf("create worker activity request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return workerhttp.ActivityResponse{}, fmt.Errorf("request worker activity: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return workerhttp.ActivityResponse{}, fmt.Errorf("worker activity returned HTTP %d", response.StatusCode)
	}
	var activity workerhttp.ActivityResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 16*1024)).Decode(&activity); err != nil {
		return workerhttp.ActivityResponse{}, fmt.Errorf("decode worker activity: %w", err)
	}
	return activity, nil
}

// forgejoIdentity is one role's Forgejo account, Git author, and workspace
// tree.
type forgejoIdentity struct {
	workspaceRoot  string
	tokenFile      string
	login          string
	gitAuthorName  string
	gitAuthorEmail string
}

// loadForgejoIdentities reads the identities a worker holds. A single-role
// worker names its role in <prefix>_FORGEJO_ROLE and uses the unprefixed
// identity settings. An agent worker (ADR-016) serves both roles and has a
// lead and a reviewer identity, each with its own workspace tree.
func loadForgejoIdentities(
	prefix string,
	label string,
	getenv func(string) string,
	defaultWorkspaceRoot string,
) (map[worker.Role]forgejoIdentity, error) {
	read := func(name string) (string, error) {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			return "", fmt.Errorf("%s is required for the %s adapter", name, label)
		}
		return value, nil
	}
	identity := func(settings string, workspaceRoot string) (forgejoIdentity, error) {
		tokenFile, err := read(settings + "_FORGEJO_TOKEN_FILE")
		if err != nil {
			return forgejoIdentity{}, err
		}
		if !filepath.IsAbs(tokenFile) {
			return forgejoIdentity{}, errors.New(settings + "_FORGEJO_TOKEN_FILE must be absolute")
		}
		login, err := read(settings + "_FORGEJO_LOGIN")
		if err != nil {
			return forgejoIdentity{}, err
		}
		authorName, err := read(settings + "_GIT_AUTHOR_NAME")
		if err != nil {
			return forgejoIdentity{}, err
		}
		authorEmail, err := read(settings + "_GIT_AUTHOR_EMAIL")
		if err != nil {
			return forgejoIdentity{}, err
		}
		for _, value := range []string{login, authorName, authorEmail} {
			if strings.ContainsAny(value, "\r\n") {
				return forgejoIdentity{}, errors.New(label + " Forgejo and Git identity values cannot contain newlines")
			}
		}
		return forgejoIdentity{
			workspaceRoot: workspaceRoot, tokenFile: tokenFile, login: login,
			gitAuthorName: authorName, gitAuthorEmail: authorEmail,
		}, nil
	}
	if role := strings.TrimSpace(getenv(prefix + "_FORGEJO_ROLE")); role != "" {
		if worker.Role(role) != worker.RoleLead && worker.Role(role) != worker.RoleReviewer {
			return nil, errors.New(prefix + "_FORGEJO_ROLE must be lead or reviewer")
		}
		single, err := identity(prefix, defaultWorkspaceRoot)
		if err != nil {
			return nil, err
		}
		return map[worker.Role]forgejoIdentity{worker.Role(role): single}, nil
	}
	identities := make(map[worker.Role]forgejoIdentity, 2)
	for role, settings := range map[worker.Role]string{
		worker.RoleLead: prefix + "_LEAD", worker.RoleReviewer: prefix + "_REVIEWER",
	} {
		workspaceRoot, err := read(settings + "_WORKSPACE_ROOT")
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(workspaceRoot) {
			return nil, errors.New(settings + "_WORKSPACE_ROOT must be absolute")
		}
		configured, err := identity(settings, workspaceRoot)
		if err != nil {
			return nil, err
		}
		identities[role] = configured
	}
	return identities, nil
}

// forgejoCredentialHelper answers Git's credential requests for the Forgejo
// origin from the role's token file, so the token itself never sits in the
// agent's environment where an `env` dump would print it.
const forgejoCredentialHelper = `!f() { if [ "$1" = get ]; then ` +
	`printf 'username=%s\npassword=%s\n' "$COMMITARIUM_FORGEJO_LOGIN" "$(cat "$COMMITARIUM_FORGEJO_TOKEN_FILE")"; fi; }; f`

// forgejoRoleEnvironment turns each role's identity into the variables its
// turns receive (Forgejo access and Git author) and the workspace tree they
// open. Roles without an identity, such as the assistants' consultant role,
// get neither. It also returns the tokens it read, which the worker redacts
// from activity.
func forgejoRoleEnvironment(
	forgejoURL string,
	identities map[worker.Role]forgejoIdentity,
	label string,
) (map[worker.Role][]string, map[worker.Role]string, []string, error) {
	variables := make(map[worker.Role][]string, len(identities))
	roots := make(map[worker.Role]string, len(identities))
	tokens := make([]string, 0, len(identities))
	for role, identity := range identities {
		tokenBytes, err := os.ReadFile(identity.tokenFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s %s Forgejo token file is unavailable", label, role)
		}
		token := strings.TrimSpace(string(tokenBytes))
		if token == "" || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
			return nil, nil, nil, fmt.Errorf("%s %s Forgejo token is empty or contains whitespace", label, role)
		}
		tokens = append(tokens, token)
		variables[role] = []string{
			"COMMITARIUM_FORGEJO_URL=" + forgejoURL,
			"COMMITARIUM_FORGEJO_TOKEN_FILE=" + identity.tokenFile,
			"COMMITARIUM_FORGEJO_LOGIN=" + identity.login,
			"GIT_AUTHOR_NAME=" + identity.gitAuthorName,
			"GIT_AUTHOR_EMAIL=" + identity.gitAuthorEmail,
			"GIT_COMMITTER_NAME=" + identity.gitAuthorName,
			"GIT_COMMITTER_EMAIL=" + identity.gitAuthorEmail,
			"GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=credential." + forgejoURL + ".helper",
			"GIT_CONFIG_VALUE_0=" + forgejoCredentialHelper,
		}
		roots[role] = identity.workspaceRoot
	}
	return variables, roots, tokens, nil
}

// usageSource serves an adapter's latest usage report, when it keeps one.
func usageSource(tracker *worker.UsageTracker) func() ([]workerhttp.UsageWindow, *time.Time) {
	if tracker == nil {
		return nil
	}
	return func() ([]workerhttp.UsageWindow, *time.Time) {
		windows, observedAt := tracker.Latest()
		reported := make([]workerhttp.UsageWindow, 0, len(windows))
		for _, window := range windows {
			reported = append(reported, workerhttp.UsageWindow{
				Name: window.Name, UsedPercent: window.UsedPercent, ResetsAt: window.ResetsAt, Limited: window.Limited,
			})
		}
		return reported, observedAt
	}
}
