package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/agentworker"
	workorderassistant "github.com/EinarLogiOskars/commitarium/internal/assistant"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	coordinatordatabase "github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/forgejo"
	"github.com/EinarLogiOskars/commitarium/internal/gitimport"
	"github.com/EinarLogiOskars/commitarium/internal/gitworkspace"
	"github.com/EinarLogiOskars/commitarium/internal/httpapi"
	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectdeletion"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

const (
	defaultRunnerMode             = "simulated"
	realAgentsRunnerMode          = "real_agents"
	legacyRealCodexLeadRunnerMode = "real_codex_lead"
	defaultWorkerRequestTimeout   = 10 * time.Second
	defaultAttemptStartTimeout    = 35 * time.Minute
	defaultForgejoURL             = "http://forgejo:3000"
	defaultForgejoHostURL         = "http://127.0.0.1:3001"
	defaultForgejoTokenFile       = "/run/commitarium-config/forgejo-token"
	defaultForgejoViewerLogin     = "commitarium-viewer"
	defaultForgejoTimeout         = 10 * time.Second
	defaultWorkspaceRoot          = "/workspaces"
	defaultReviewerWorkspaceRoot  = "/reviewer-workspaces"
	defaultToolchainRoot          = "/var/lib/commitarium-toolchains"
	defaultAgentWorkerTokenDir    = "/run/commitarium-agent-workers"
)

type config struct {
	databasePath           string
	runnerMode             string
	simulatedStepDelay     time.Duration
	agentWorkerURLTemplate string
	agentWorkerTokenDir    string
	workerRequestTimeout   time.Duration
	attemptStartTimeout    time.Duration
	forgejoURL             string
	forgejoOwner           string
	forgejoHostURL         string
	forgejoTokenFile       string
	forgejoViewerLogin     string
	forgejoTimeout         time.Duration
	workspaceRoot          string
	reviewerWorkspaceRoot  string
	toolchainRoot          string
	gitExecutable          string
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
	switch runnerMode {
	case defaultRunnerMode, realAgentsRunnerMode:
	case legacyRealCodexLeadRunnerMode:
		// Backward compatibility for development environments created before the
		// real workflow gained independent reviewer and Claude routing.
		runnerMode = realAgentsRunnerMode
	default:
		return config{}, errors.New("COMMITARIUM_RUNNER_MODE must be simulated or real_agents")
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
		simulatedStepDelay:     simulatedStepDelay,
		workerRequestTimeout:   defaultWorkerRequestTimeout,
		attemptStartTimeout:    defaultAttemptStartTimeout,
		forgejoURL:             defaultForgejoURL,
		forgejoOwner:           "commitarium_admin",
		forgejoHostURL:         defaultForgejoHostURL,
		forgejoTokenFile:       defaultForgejoTokenFile,
		forgejoViewerLogin:     defaultForgejoViewerLogin,
		forgejoTimeout:         defaultForgejoTimeout,
		workspaceRoot:          defaultWorkspaceRoot,
		reviewerWorkspaceRoot:  defaultReviewerWorkspaceRoot,
		toolchainRoot:          defaultToolchainRoot,
		gitExecutable:          "git",
		agentWorkerURLTemplate: agentworker.DefaultURLTemplate,
		agentWorkerTokenDir:    defaultAgentWorkerTokenDir,
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_URL")); value != "" {
		loaded.forgejoURL = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_OWNER")); value != "" {
		loaded.forgejoOwner = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_HOST_URL")); value != "" {
		loaded.forgejoHostURL = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_TOKEN_FILE")); value != "" {
		loaded.forgejoTokenFile = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_FORGEJO_VIEWER_LOGIN")); value != "" {
		loaded.forgejoViewerLogin = value
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
	if value := strings.TrimSpace(getenv("COMMITARIUM_REVIEWER_WORKSPACE_ROOT")); value != "" {
		loaded.reviewerWorkspaceRoot = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_TOOLCHAIN_ROOT")); value != "" {
		loaded.toolchainRoot = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_GIT_EXECUTABLE")); value != "" {
		loaded.gitExecutable = value
	}
	if runnerMode == realAgentsRunnerMode {
		var err error
		if value := strings.TrimSpace(getenv("COMMITARIUM_AGENT_WORKER_URL_TEMPLATE")); value != "" {
			loaded.agentWorkerURLTemplate = value
		}
		if value := strings.TrimSpace(getenv("COMMITARIUM_AGENT_WORKER_TOKEN_DIR")); value != "" {
			loaded.agentWorkerTokenDir = value
		}
		if value := strings.TrimSpace(getenv("COMMITARIUM_WORKER_REQUEST_TIMEOUT")); value != "" {
			loaded.workerRequestTimeout, err = time.ParseDuration(value)
			if err != nil || loaded.workerRequestTimeout <= 0 {
				return config{}, errors.New("COMMITARIUM_WORKER_REQUEST_TIMEOUT must be a positive duration")
			}
		}
		if value := strings.TrimSpace(getenv("COMMITARIUM_WORKER_ATTEMPT_START_TIMEOUT")); value != "" {
			loaded.attemptStartTimeout, err = time.ParseDuration(value)
			if err != nil || loaded.attemptStartTimeout <= 0 {
				return config{}, errors.New("COMMITARIUM_WORKER_ATTEMPT_START_TIMEOUT must be a positive duration")
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
	agentService := agent.NewService(coordinatordatabase.NewAgentStore(db))
	forgejoClient, err := forgejo.NewClient(forgejo.ClientConfig{
		BaseURL: coordinatorConfig.forgejoURL, HostBaseURL: coordinatorConfig.forgejoHostURL,
		Owner:     coordinatorConfig.forgejoOwner,
		TokenFile: coordinatorConfig.forgejoTokenFile,
		AgentCollaborators: func(ctx context.Context) ([]string, error) {
			agents, err := agentService.List(ctx)
			if err != nil {
				return nil, err
			}
			identities := make([]string, 0, 2*len(agents))
			for _, configured := range agents {
				identities = append(identities, configured.ID+"-lead", configured.ID+"-reviewer")
			}
			return identities, nil
		},
		ReadCollaborators: []string{coordinatorConfig.forgejoViewerLogin},
		RequestTimeout:    coordinatorConfig.forgejoTimeout,
	})
	if err != nil {
		return fmt.Errorf("create Forgejo client: %w", err)
	}
	projectImporter, err := gitimport.NewManager(gitimport.Config{
		InternalBaseURL: coordinatorConfig.forgejoURL,
		TokenFile:       coordinatorConfig.forgejoTokenFile,
		GitExecutable:   coordinatorConfig.gitExecutable,
		Provisioner:     forgejoClient,
	})
	if err != nil {
		return fmt.Errorf("create project import service: %w", err)
	}
	projectService := project.NewServiceWithRepositoryServices(
		projectStore, forgejoClient, projectImporter, forgejoClient, projectImporter,
	)
	toolchainService, err := toolchain.NewManager(coordinatorConfig.toolchainRoot, projectService)
	if err != nil {
		return fmt.Errorf("create project toolchain service: %w", err)
	}
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
	reviewerCheckoutManager, err := gitworkspace.NewManager(gitworkspace.Config{
		Root:            coordinatorConfig.reviewerWorkspaceRoot,
		InternalBaseURL: coordinatorConfig.forgejoURL,
		HostBaseURL:     coordinatorConfig.forgejoHostURL,
		TokenFile:       coordinatorConfig.forgejoTokenFile,
		GitExecutable:   coordinatorConfig.gitExecutable,
	})
	if err != nil {
		return fmt.Errorf("create reviewer managed-checkout service: %w", err)
	}
	workspaceService := workspace.NewServiceWithPreparationAndAccess(
		workspaceStore, featureService, projectService, forgejoClient,
		checkoutManager, forgejoClient, forgejoClient, reviewerCheckoutManager,
	)
	workflowStore := coordinatordatabase.NewWorkflowStore(db)
	workflowService := workflow.NewService(workflowStore)
	executionStore := coordinatordatabase.NewExecutionStore(db)
	executionService := execution.NewService(executionStore)
	environmentService := projectenvironment.NewService(coordinatordatabase.NewProjectEnvironmentStore(db))
	validationService := validation.NewService(coordinatordatabase.NewValidationStore(db))
	activeSessions := orchestration.NewActiveSessions()
	var sessionController httpapi.SessionController
	var runStarter httpapi.RunStarter
	var runRecoverer orchestration.RunRecoverer
	var realWorkflowStarter httpapi.RealWorkflowStarter
	var modelCatalogService httpapi.ModelCatalogService
	var toolchainAssistantService httpapi.ToolchainAssistantService
	var workOrderAssistant httpapi.WorkOrderAssistant
	var deletionWorker projectdeletion.WorkerStopper
	var deletionLiveSessions projectdeletion.LiveSessionRegistry
	var assistantCleaner projectdeletion.AssistantCleaner
	switch coordinatorConfig.runnerMode {
	case defaultRunnerMode:
		deletionLiveSessions = activeSessions
		sessionController = orchestration.NewController(executionService, activeSessions)
		runner := orchestration.NewRunner(workflowService, executionService, activeSessions)
		simulatedStarter := orchestration.NewStarter(
			runner,
			func() orchestration.Assignment {
				return orchestration.NewSimulatedAssignment(coordinatorConfig.simulatedStepDelay)
			},
		)
		runStarter = simulatedStarter
		runRecoverer = simulatedStarter
	case realAgentsRunnerMode:
		agentWorkers, err := agentworker.New(agentworker.Config{
			URLTemplate:         coordinatorConfig.agentWorkerURLTemplate,
			TokenDir:            coordinatorConfig.agentWorkerTokenDir,
			RequestTimeout:      coordinatorConfig.workerRequestTimeout,
			AttemptStartTimeout: coordinatorConfig.attemptStartTimeout,
		})
		if err != nil {
			return fmt.Errorf("create agent worker registry: %w", err)
		}
		modelSource := func(provider project.AgentProvider) modelcatalog.Source {
			return agentworker.ModelSource{Workers: agentWorkers.Models, Agents: agentService, Provider: provider}
		}
		// Model catalogs stay per provider: any agent of the provider answers.
		catalog, err := modelcatalog.New(
			coordinatordatabase.NewModelCatalogStore(db),
			map[modelcatalog.SourceKey]modelcatalog.Source{
				{Provider: project.AgentProviderCodex, Role: modelcatalog.RoleLead}:      modelSource(project.AgentProviderCodex),
				{Provider: project.AgentProviderCodex, Role: modelcatalog.RoleReviewer}:  modelSource(project.AgentProviderCodex),
				{Provider: project.AgentProviderClaude, Role: modelcatalog.RoleLead}:     modelSource(project.AgentProviderClaude),
				{Provider: project.AgentProviderClaude, Role: modelcatalog.RoleReviewer}: modelSource(project.AgentProviderClaude),
			},
		)
		if err != nil {
			return fmt.Errorf("create model catalog: %w", err)
		}
		if err := catalog.Load(ctx); err != nil {
			return fmt.Errorf("load cached model catalog: %w", err)
		}
		go catalog.Run(ctx, 30*time.Minute, func(err error) { log.Printf("refresh model catalog: %v", err) })
		modelCatalogService = catalog
		assistant, err := toolchain.NewAssistant(
			coordinatorConfig.toolchainRoot, coordinatorConfig.workspaceRoot, projectService, toolchainService,
			func(agentID string) (workerhttp.Service, error) { return agentWorkers.Client(agentID) },
		)
		if err != nil {
			return fmt.Errorf("create project toolchain assistant: %w", err)
		}
		toolchainAssistantService = assistant
		assistantCleaner = assistant
		clarifier, err := workorderassistant.NewService(workorderassistant.Config{
			Store: coordinatordatabase.NewAssistantStore(db), Features: featureService,
			Workspaces: workspaceService, Briefs: workflowService, Transitions: workflowService,
			Workers: func(agentID string) (workorderassistant.WorkerService, error) {
				return agentWorkers.Client(agentID)
			},
		})
		if err != nil {
			return fmt.Errorf("create work-order assistant: %w", err)
		}
		workOrderAssistant = clarifier
		ingestion := workeringest.NewService(executionService, workeringest.FilterFunc(
			func(_ context.Context, event workerhttp.Event) (workerhttp.Event, error) {
				return event, nil
			},
		))
		workerRouter, err := orchestration.NewAgentRoutedWorker(
			executionService,
			func(agentID string) (orchestration.RemoteLeadWorker, error) { return agentWorkers.Client(agentID) },
		)
		if err != nil {
			return fmt.Errorf("create agent worker router: %w", err)
		}
		deletionWorker = workerRouter
		pumpRouter, err := orchestration.NewAgentRoutedPump(
			executionService,
			func(agentID string) (orchestration.RemoteLeadPump, error) {
				client, err := agentWorkers.Client(agentID)
				if err != nil {
					return nil, err
				}
				return workeringest.NewRecoveringPump(
					executionService, ingestion, workeringest.NewHTTPAttemptSource(client),
				), nil
			},
		)
		if err != nil {
			return fmt.Errorf("create agent event-pump router: %w", err)
		}
		remoteStarter, err := orchestration.NewRemoteLeadStarter(orchestration.RemoteLeadConfig{
			Executions: executionService, Features: featureStore, Goals: workflowService,
			Planning: workflowService, Artifacts: workflowService, Briefs: workflowService, Workspaces: workspaceService,
			Worker:              workerRouter,
			Pump:                pumpRouter,
			EnvironmentRequests: environmentService,
			Validation:          validationService,
			Toolchains:          toolchainService,
			Usage:               executionStore,
			Lifetime:            ctx,
			ReportError:         func(err error) { log.Printf("real agent workflow: %v", err) },
		})
		if err != nil {
			return fmt.Errorf("create real agent workflow runner: %w", err)
		}
		runStarter = remoteStarter
		runRecoverer = remoteStarter
		sessionController = remoteStarter
		realWorkflowStarter = remoteStarter
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
	runStopper := projectdeletion.NewRunStopper(
		executionService, deletionWorker, sessionController, deletionLiveSessions,
	)
	featureDeletionService := workorder.NewServiceWithRunTerminator(
		coordinatordatabase.NewFeatureDeletionStore(db), forgejoClient, runStopper,
		checkoutManager, reviewerCheckoutManager,
	)
	projectDeletionService := projectdeletion.NewService(
		coordinatordatabase.NewProjectDeletionStore(db),
		featureDeletionService,
		runStopper,
		toolchainService,
		assistantCleaner,
		forgejoClient,
	)
	handler := httpapi.NewWithWorkspaceRealWorkflowDeletionAndModels(
		projectService,
		featureService,
		workflowService,
		executionService,
		sessionController,
		runStarter,
		workspaceService,
		realWorkflowStarter,
		featureDeletionService,
		modelCatalogService,
		toolchainService,
		toolchainAssistantService,
		projectDeletionService,
		environmentService,
		validationService,
		executionStore,
		workOrderAssistant,
		agentService,
	)

	log.Print("Listening...")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		return fmt.Errorf("serve coordinator API: %w", err)
	}

	return nil
}
