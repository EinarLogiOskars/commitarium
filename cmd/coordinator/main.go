package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	coordinatordatabase "github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/forgejo"
	"github.com/EinarLogiOskars/commitarium/internal/gitworkspace"
	"github.com/EinarLogiOskars/commitarium/internal/httpapi"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const (
	defaultRunnerMode           = "simulated"
	realCodexLeadRunnerMode     = "real_codex_lead"
	defaultWorkerRequestTimeout = 10 * time.Second
	defaultForgejoURL           = "http://forgejo:3000"
	defaultForgejoHostURL       = "http://127.0.0.1:3001"
	defaultForgejoTokenFile     = "/run/commitarium-config/forgejo-token"
	defaultForgejoTimeout       = 10 * time.Second
	defaultWorkspaceRoot        = "/workspaces"
)

type config struct {
	databasePath         string
	runnerMode           string
	simulatedStepDelay   time.Duration
	codexWorkerURL       string
	codexWorkerToken     string
	codexAgentProfileID  string
	codexWorkspaceID     string
	workerRequestTimeout time.Duration
	forgejoURL           string
	forgejoHostURL       string
	forgejoTokenFile     string
	forgejoTimeout       time.Duration
	workspaceRoot        string
	gitExecutable        string
}

func main() {
	coordinatorConfig, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if err := run(context.Background(), coordinatorConfig); err != nil {
		log.Fatal(err)
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	databasePath := strings.TrimSpace(getenv("COMMITARIUM_DATABASE_PATH"))
	if databasePath == "" {
		return config{}, errors.New("COMMITARIUM_DATABASE_PATH is required")
	}
	runnerMode := strings.TrimSpace(getenv("COMMITARIUM_RUNNER_MODE"))
	if runnerMode == "" {
		runnerMode = defaultRunnerMode
	}
	if runnerMode != defaultRunnerMode && runnerMode != realCodexLeadRunnerMode {
		return config{}, errors.New("COMMITARIUM_RUNNER_MODE must be simulated or real_codex_lead")
	}
	simulatedStepDelay := 250 * time.Millisecond
	if configuredDelay := strings.TrimSpace(getenv("COMMITARIUM_SIMULATED_STEP_DELAY")); configuredDelay != "" {
		parsed, err := time.ParseDuration(configuredDelay)
		if err != nil || parsed < 0 {
			return config{}, errors.New("COMMITARIUM_SIMULATED_STEP_DELAY must be a nonnegative duration")
		}
		simulatedStepDelay = parsed
	}
	loaded := config{
		databasePath: databasePath, runnerMode: runnerMode,
		simulatedStepDelay:   simulatedStepDelay,
		workerRequestTimeout: defaultWorkerRequestTimeout,
		forgejoURL:           defaultForgejoURL,
		forgejoHostURL:       defaultForgejoHostURL,
		forgejoTokenFile:     defaultForgejoTokenFile,
		forgejoTimeout:       defaultForgejoTimeout,
		workspaceRoot:        defaultWorkspaceRoot,
		gitExecutable:        "git",
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_URL")); value != "" {
		loaded.forgejoURL = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_HOST_URL")); value != "" {
		loaded.forgejoHostURL = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_TOKEN_FILE")); value != "" {
		loaded.forgejoTokenFile = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_REQUEST_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return config{}, errors.New("COMMITARIUM_FORGEJO_REQUEST_TIMEOUT must be a positive duration")
		}
		loaded.forgejoTimeout = parsed
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_WORKSPACE_ROOT")); value != "" {
		loaded.workspaceRoot = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_GIT_EXECUTABLE")); value != "" {
		loaded.gitExecutable = value
	}
	if runnerMode == realCodexLeadRunnerMode {
		required := func(name string) (string, error) {
			value := strings.TrimSpace(getenv(name))
			if value == "" {
				return "", fmt.Errorf("%s is required in real_codex_lead mode", name)
			}
			return value, nil
		}
		var err error
		if loaded.codexWorkerURL, err = required("COMMITARIUM_CODEX_WORKER_URL"); err != nil {
			return config{}, err
		}
		if loaded.codexWorkerToken, err = required("COMMITARIUM_CODEX_WORKER_TOKEN"); err != nil {
			return config{}, err
		}
		if loaded.codexAgentProfileID, err = required("COMMITARIUM_CODEX_PROFILE_ID"); err != nil {
			return config{}, err
		}
		if loaded.codexWorkspaceID, err = required("COMMITARIUM_CODEX_WORKSPACE_ID"); err != nil {
			return config{}, err
		}
		if value := strings.TrimSpace(getenv("COMMITARIUM_CODEX_WORKER_REQUEST_TIMEOUT")); value != "" {
			loaded.workerRequestTimeout, err = time.ParseDuration(value)
			if err != nil || loaded.workerRequestTimeout <= 0 {
				return config{}, errors.New("COMMITARIUM_CODEX_WORKER_REQUEST_TIMEOUT must be a positive duration")
			}
		}
	}
	return loaded, nil
}

func run(ctx context.Context, coordinatorConfig config) error {
	db, err := coordinatordatabase.OpenSQLite(ctx, coordinatorConfig.databasePath)
	if err != nil {
		return fmt.Errorf("open coordinator database: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("close coordinator database: %v", err)
		}
	}()

	if err := coordinatordatabase.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate coordinator database: %w", err)
	}

	projectStore := coordinatordatabase.NewProjectStore(db)
	forgejoClient, err := forgejo.NewClient(forgejo.ClientConfig{
		BaseURL: coordinatorConfig.forgejoURL, TokenFile: coordinatorConfig.forgejoTokenFile,
		RequestTimeout: coordinatorConfig.forgejoTimeout,
	})
	if err != nil {
		return fmt.Errorf("create Forgejo client: %w", err)
	}
	projectService := project.NewServiceWithRepositoryVerifier(projectStore, forgejoClient)
	featureStore := coordinatordatabase.NewFeatureStore(db)
	featureService := feature.NewService(featureStore, projectService)
	workspaceStore := coordinatordatabase.NewWorkspaceStore(db)
	checkoutManager, err := gitworkspace.NewManager(gitworkspace.Config{
		Root:            coordinatorConfig.workspaceRoot,
		InternalBaseURL: coordinatorConfig.forgejoURL,
		HostBaseURL:     coordinatorConfig.forgejoHostURL,
		TokenFile:       coordinatorConfig.forgejoTokenFile,
		GitExecutable:   coordinatorConfig.gitExecutable,
	})
	if err != nil {
		return fmt.Errorf("create managed-checkout service: %w", err)
	}
	workspaceService := workspace.NewServiceWithPreparation(
		workspaceStore, featureService, projectService, forgejoClient,
		checkoutManager, forgejoClient,
	)
	workflowStore := coordinatordatabase.NewWorkflowStore(db)
	workflowService := workflow.NewService(workflowStore)
	executionStore := coordinatordatabase.NewExecutionStore(db)
	executionService := execution.NewService(executionStore)
	activeSessions := orchestration.NewActiveSessions()
	var sessionController httpapi.SessionController
	var runStarter httpapi.RunStarter
	var runRecoverer orchestration.RunRecoverer
	var planningStarter httpapi.PlanningStarter
	switch coordinatorConfig.runnerMode {
	case defaultRunnerMode:
		sessionController = orchestration.NewController(executionService, activeSessions)
		runner := orchestration.NewRunner(workflowService, executionService, activeSessions)
		simulatedStarter := orchestration.NewStarter(
			runner,
			func() orchestration.Assignment {
				return orchestration.NewSimulatedAssignment(coordinatorConfig.simulatedStepDelay)
			},
			2,
			3,
		)
		runStarter = simulatedStarter
		runRecoverer = simulatedStarter
	case realCodexLeadRunnerMode:
		client, err := workerhttp.NewClient(workerhttp.ClientConfig{
			BaseURL:        coordinatorConfig.codexWorkerURL,
			BearerToken:    coordinatorConfig.codexWorkerToken,
			RequestTimeout: coordinatorConfig.workerRequestTimeout,
		})
		if err != nil {
			return fmt.Errorf("create Codex worker client: %w", err)
		}
		ingestion := workeringest.NewService(executionService, workeringest.FilterFunc(
			func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
				return event, nil
			},
		))
		remoteStarter, err := orchestration.NewRemoteLeadStarter(orchestration.RemoteLeadConfig{
			Executions: executionService, Features: featureStore, Goals: workflowService,
			Planning: workflowService, Workspaces: workspaceService,
			Worker: client,
			Pump: workeringest.NewPump(
				executionService, ingestion, workeringest.NewHTTPAttemptSource(client),
			),
			Lifetime: ctx, AgentProfileID: coordinatorConfig.codexAgentProfileID,
			WorkspaceID: coordinatorConfig.codexWorkspaceID,
			ReportError: func(err error) { log.Printf("real Codex lead: %v", err) },
		})
		if err != nil {
			return fmt.Errorf("create real Codex lead runner: %w", err)
		}
		runStarter = remoteStarter
		runRecoverer = remoteStarter
		sessionController = remoteStarter
		planningStarter = remoteStarter
	default:
		return fmt.Errorf("unsupported coordinator runner mode %q", coordinatorConfig.runnerMode)
	}
	recoverer := orchestration.NewRecoverer(
		executionService,
		featureStore,
		projectService,
		runRecoverer,
	)
	recoveredRuns, recoveryErr := recoverer.RecoverAll(ctx)
	if recoveryErr != nil {
		log.Printf("recover interrupted workflows: %v", recoveryErr)
	}
	if recoveredRuns > 0 {
		log.Printf("recovering %d interrupted workflow(s)", recoveredRuns)
	}
	handler := httpapi.NewWithWorkspaceAndPlanningService(
		projectService,
		featureService,
		workflowService,
		executionService,
		sessionController,
		runStarter,
		workspaceService,
		planningStarter,
	)

	log.Print("Listening...")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		return fmt.Errorf("serve coordinator API: %w", err)
	}

	return nil
}
