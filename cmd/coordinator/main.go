package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/agentworker"
	workorderassistant "github.com/EinarLogiOskars/commitarium/internal/assistant"
	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coordinatordatabase "github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/documents"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/forgejo"
	"github.com/EinarLogiOskars/commitarium/internal/gitimport"
	"github.com/EinarLogiOskars/commitarium/internal/gitworkspace"
	"github.com/EinarLogiOskars/commitarium/internal/httpapi"
	"github.com/EinarLogiOskars/commitarium/internal/install"
	"github.com/EinarLogiOskars/commitarium/internal/marketplace"
	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/previewcheck"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectdeletion"
	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/release"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
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
	defaultCredentialsRoot        = "/var/lib/commitarium-credentials"
	marketplaceTimeout            = 15 * time.Second
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
	// marketplaceURL is the app marketplace (MARKETPLACE_PLAN.md); empty
	// leaves the marketplace off. Its token lives in credentialsRoot, which
	// backups leave out.
	marketplaceURL  string
	credentialsRoot string
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
		credentialsRoot:        defaultCredentialsRoot,
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_MARKETPLACE_URL")); value != "" {
		loaded.marketplaceURL = value
	}
	if value := strings.TrimSpace(getenv("COMMITARIUM_CREDENTIALS_ROOT")); value != "" {
		loaded.credentialsRoot = value
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
	// The files attached to projects are copied into each checkout of their
	// repositories, for the assistant, its helpers, and the agents.
	documentStore, err := documents.NewStore(filepath.Join(coordinatorConfig.toolchainRoot, "documents"))
	if err != nil {
		return fmt.Errorf("create project documents: %w", err)
	}
	checkoutManager, err := gitworkspace.NewManager(gitworkspace.Config{
		Root:            coordinatorConfig.workspaceRoot,
		InternalBaseURL: coordinatorConfig.forgejoURL,
		HostBaseURL:     coordinatorConfig.forgejoHostURL,
		TokenFile:       coordinatorConfig.forgejoTokenFile,
		GitExecutable:   coordinatorConfig.gitExecutable,
		Ready:           documentStore.ForCheckouts(projectService),
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
		Ready:           documentStore.ForCheckouts(projectService),
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
	previewCheckService := previewcheck.NewService(coordinatordatabase.NewPreviewCheckStore(db))
	// The official model guide and the user's overrides decide which models
	// the assistant must ask about (ADR-031).
	modelGuide, err := modelguide.NewService(coordinatordatabase.NewModelGuideStore(db))
	if err != nil {
		return fmt.Errorf("create the model guide: %w", err)
	}
	activeSessions := orchestration.NewActiveSessions()
	var sessionController httpapi.SessionController
	var runStarter httpapi.RunStarter
	var runRecoverer orchestration.RunRecoverer
	var realWorkflowStarter httpapi.RealWorkflowStarter
	var modelCatalogService httpapi.ModelCatalogService
	var toolchainAssistantService httpapi.ToolchainAssistantService
	var workOrderAssistant httpapi.WorkOrderAssistant
	var visionService httpapi.VisionService
	var conversationService httpapi.ConversationService
	var assistantWatcher *conversations.Watcher
	var agentWorkerRegistry *agentworker.Registry
	var projectConversations *conversations.Service
	var deletionWorker projectdeletion.WorkerStopper
	var deletionLiveSessions projectdeletion.LiveSessionRegistry
	var assistantCleaner projectdeletion.AssistantCleaner
	// The assistant shares agents' sign-ins with the work orders, whose runner
	// is created after it (LOGIN_RECOVERY_PLAN.md).
	agentSignIns := &lateSignIns{}
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
		agentWorkerRegistry = agentWorkers
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
		visions, err := vision.NewService(
			coordinatorConfig.toolchainRoot, coordinatorConfig.workspaceRoot, projectService,
			func(agentID string) (workerhttp.Service, error) { return agentWorkers.Client(agentID) },
		)
		if err != nil {
			return fmt.Errorf("create project visions: %w", err)
		}
		visions.SetSignIns(agentSignIns)
		visionService = visions.WithRepository(checkoutManager, forgejoClient)
		models := func(ctx context.Context) []conversations.ModelChoice {
			choices := make([]conversations.ModelChoice, 0)
			for _, listed := range catalog.List(ctx) {
				if listed.Role != modelcatalog.RoleLead {
					continue
				}
				for _, model := range listed.Models {
					choices = append(choices, conversations.ModelChoice{
						Provider: listed.Provider, ID: model.ID, Display: model.DisplayName,
						Efforts: model.SupportedReasoningEfforts,
					})
				}
			}
			return choices
		}
		// The assistant chooses work orders' agents from their usage and
		// models (ADR-027), and names the agent it runs on itself.
		usage := agentUsageReader{agents: agentService, workers: agentWorkers}
		var settingsOf *conversations.Service
		projectAssistant, err := conversations.NewService(conversations.Config{
			Root: filepath.Join(coordinatorConfig.toolchainRoot, "assistant"), WorkspaceRoot: coordinatorConfig.workspaceRoot,
			Projects: projectService, Agents: agentService,
			Describer: conversations.StateDescriber{
				Projects: projectService, Agents: agentService, Features: featureService,
				Visions: visions, Toolchains: toolchainService, Models: models, Runs: executionService,
				Documents: documentStore,
				Usage: func(ctx context.Context) []agent.Usage {
					reported, err := usage.AgentUsage(ctx)
					if err != nil {
						return nil
					}
					return reported
				},
				Settings: func(ctx context.Context) (conversations.Settings, error) { return settingsOf.Settings(ctx) },
				Guide:    modelGuide,
				Blockers: executionStore,
			},
			Workers: func(agentID string) (workerhttp.Service, error) { return agentWorkers.Client(agentID) },
			DefaultModel: func(ctx context.Context, provider project.AgentProvider) string {
				for _, choice := range models(ctx) {
					if choice.Provider == provider {
						return choice.ID
					}
				}
				return ""
			},
			Checkouts: checkoutManager, Branches: forgejoClient,
			Visions: visions, Features: featureService,
			Guide: modelGuide, Models: models, ChatUsage: executionStore,
			SignIns: agentSignIns,
			RecoveryExhausted: func(ctx context.Context, workOrderID string) bool {
				runs, err := executionService.RunsForFeature(ctx, workOrderID)
				if err != nil || len(runs) == 0 {
					return false
				}
				runID := execution.NewestRun(runs).ID
				blocker, found, err := executionStore.RunBlocker(ctx, runID)
				if err != nil || !found {
					return false
				}
				recoveries, err := executionStore.RunRecoveries(ctx, runID)
				return err == nil && execution.RecoveryExhausted(blocker, recoveries)
			},
		})
		if err != nil {
			return fmt.Errorf("create the assistant: %w", err)
		}
		settingsOf = projectAssistant
		// Work orders from before their creator was stored are the user's,
		// except those the assistant's conversations show it created (ADR-027).
		if err := projectAssistant.MarkCreatedWork(ctx, featureService); err != nil {
			log.Printf("record the assistant's work orders: %v", err)
		}
		conversationService = projectAssistant
		projectConversations = projectAssistant
		// Changes in the projects' work wake the assistant (events), and
		// autopilot runs its actions; it starts once the API exists.
		assistantWatcher = &conversations.Watcher{
			Service: projectAssistant, Features: featureService, Runs: executionService, LimitWaits: executionService,
			RetryWaits: executionService, Visions: visions,
			Toolchains: toolchainService, VisionSessions: visions,
			// The gap check does not wake an assistant whose agent is at its
			// usage limit: a run waits on it, or its latest report says so.
			AgentLimited: func(ctx context.Context, agentID string) bool {
				now := time.Now()
				if waits, err := executionService.LimitWaits(ctx); err == nil {
					for _, wait := range waits {
						if wait.AgentID == agentID && (wait.ResetsAt == nil || wait.ResetsAt.After(now)) {
							return true
						}
					}
				}
				reported, err := usage.AgentUsage(ctx)
				if err != nil {
					return false
				}
				for _, current := range reported {
					if current.Agent == agentID {
						limited, _ := current.LimitedUntil(now)
						return limited
					}
				}
				return false
			},
		}
		assistant, err := toolchain.NewAssistant(
			coordinatorConfig.toolchainRoot, coordinatorConfig.workspaceRoot, projectService, toolchainService,
			func(agentID string) (workerhttp.Service, error) { return agentWorkers.Client(agentID) },
		)
		if err != nil {
			return fmt.Errorf("create project toolchain assistant: %w", err)
		}
		assistant.SetSignIns(agentSignIns)
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
		clarifier.SetSignIns(agentSignIns)
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
		workerRouter.WithPhaseChoices(featureService)
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
			PreviewChecks:       previewCheckService,
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
		agentSignIns.target = remoteStarter
		if projectConversations != nil {
			assistant := projectConversations
			remoteStarter.FollowSignIns(
				func(agentID string) { assistant.AgentSignedIn(ctx, agentID) },
				func(agentID string) { assistant.TrySignIn(ctx, agentID) },
			)
		}
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
	// Runs paused on an agent's usage limit continue when it resets.
	if resumer, ok := realWorkflowStarter.(interface{ RunLimitResumer(context.Context) }); ok {
		go resumer.RunLimitResumer(ctx)
	}
	// Work waiting on an agent's sign-in is tried again now and then.
	if checker, ok := realWorkflowStarter.(interface{ RunSignInChecker(context.Context) }); ok {
		go checker.RunSignInChecker(ctx)
	}
	// Turns that infrastructure cut off retry by themselves, on schedule.
	if resumer, ok := realWorkflowStarter.(interface{ RunRetryResumer(context.Context) }); ok {
		go resumer.RunRetryResumer(ctx)
	}
	// Preview checks no desktop picked up block their work order with a
	// reason that says to open the app (ADR-028).
	if watcher, ok := realWorkflowStarter.(interface{ RunPreviewCheckWatcher(context.Context) }); ok {
		go watcher.RunPreviewCheckWatcher(ctx)
	}
	runStopper := projectdeletion.NewRunStopper(
		executionService, deletionWorker, sessionController, deletionLiveSessions,
	)
	featureDeletionStore := coordinatordatabase.NewFeatureDeletionStore(db)
	featureDeletionService := workorder.NewServiceWithRunTerminator(
		featureDeletionStore, forgejoClient, runStopper,
		checkoutManager, reviewerCheckoutManager,
	).WithReset(featureDeletionStore, workflowService)
	projectDeletionService := projectdeletion.NewService(
		coordinatordatabase.NewProjectDeletionStore(db),
		featureDeletionService,
		runStopper,
		toolchainService,
		assistantCleaner,
		forgejoClient,
	)
	if projectConversations != nil {
		projectDeletionService.WithConversations(projectConversations)
	}
	projectDeletionService.WithDocuments(documentStore)
	var marketplaceService httpapi.MarketplaceService
	var releaseService httpapi.ReleaseService
	var installService httpapi.InstallService
	if coordinatorConfig.marketplaceURL != "" {
		connected, err := marketplace.NewService(coordinatorConfig.marketplaceURL, coordinatorConfig.credentialsRoot, marketplaceTimeout)
		if err != nil {
			return fmt.Errorf("connect the marketplace: %w", err)
		}
		marketplaceService = connected
		var releaseVisions release.Visions
		if visionService != nil {
			releaseVisions = visionService
		}
		releases, err := release.NewService(release.Config{
			LocalBaseURL: coordinatorConfig.forgejoURL, LocalOwner: coordinatorConfig.forgejoOwner,
			LocalTokenFile: coordinatorConfig.forgejoTokenFile, GitExecutable: coordinatorConfig.gitExecutable,
			StateRoot: coordinatorConfig.toolchainRoot,
		}, projectService, toolchainService, releaseVisions, featureService, connected,
			func(ctx context.Context, owner, name, branch string) (string, error) {
				head, err := forgejoClient.GetBranch(ctx, owner, name, branch)
				return head.CommitID, err
			})
		if err != nil {
			return fmt.Errorf("set up releases: %w", err)
		}
		releaseService = releases
		installs, err := install.NewService(connected, coordinatorConfig.gitExecutable, coordinatorConfig.toolchainRoot)
		if err != nil {
			return fmt.Errorf("set up installs: %w", err)
		}
		installService = installs
	}
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
		visionService,
		conversationService,
		agentUsageReader{agents: agentService, workers: agentWorkerRegistry},
		marketplaceService,
		releaseService,
		installService,
		previewCheckService,
		modelGuide,
		documentStore,
	)

	if assistantWatcher != nil {
		assistantWatcher.Apply = httpapi.AssistantActionApplier(handler)
		assistantWatcher.SetUp = httpapi.ProjectSetUpper(handler)
		assistantWatcher.Helpers = httpapi.HelperReader(handler)
		go assistantWatcher.Run(ctx)
	}

	log.Print("Listening...")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		return fmt.Errorf("serve coordinator API: %w", err)
	}

	return nil
}

// agentUsageReader asks each connected agent's worker for its latest usage
// report; an agent whose worker cannot be reached is left out.
type agentUsageReader struct {
	agents interface {
		List(context.Context) ([]agent.Agent, error)
	}
	workers *agentworker.Registry
}

func (reader agentUsageReader) AgentUsage(ctx context.Context) ([]httpapi.AgentUsage, error) {
	if reader.workers == nil {
		return nil, nil
	}
	agents, err := reader.agents.List(ctx)
	if err != nil {
		return nil, err
	}
	// The workers are asked at once: the assistant's state and every Start
	// read this, and a stopped worker takes up to the timeout to give up.
	answers := make([]*httpapi.AgentUsage, len(agents))
	var asking sync.WaitGroup
	for index, candidate := range agents {
		client, err := reader.workers.Client(candidate.ID)
		if err != nil {
			continue
		}
		asking.Add(1)
		go func() {
			defer asking.Done()
			asked, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if reported, err := client.Usage(asked); err == nil {
				answers[index] = &httpapi.AgentUsage{
					Agent: candidate.ID, Windows: reported.Windows, ObservedAt: reported.ObservedAt,
				}
			}
		}()
	}
	asking.Wait()
	usage := make([]httpapi.AgentUsage, 0, len(agents))
	for _, answer := range answers {
		if answer != nil {
			usage = append(usage, *answer)
		}
	}
	return usage, nil
}

// lateSignIns passes the assistant's sign-in questions to the work order
// runner once it exists; until then no agent is known to need a sign-in.
type lateSignIns struct {
	target conversations.SignIns
}

func (late *lateSignIns) MarkAgentNeedsSignIn(ctx context.Context, agentID, lastError string) error {
	if late.target == nil {
		return nil
	}
	return late.target.MarkAgentNeedsSignIn(ctx, agentID, lastError)
}

func (late *lateSignIns) AgentNeedsSignIn(ctx context.Context, agentID string) bool {
	return late.target != nil && late.target.AgentNeedsSignIn(ctx, agentID)
}

func (late *lateSignIns) AgentAnswered(ctx context.Context, agentID string) {
	if late.target != nil {
		late.target.AgentAnswered(ctx, agentID)
	}
}
