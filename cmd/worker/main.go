package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workerjournal"
	"github.com/EinarLogiOskars/commitarium/internal/workerservice"
)

const (
	defaultListenAddress = ":8081"
	defaultStepDelay     = 250 * time.Millisecond
	defaultHealthURL     = "http://127.0.0.1:8081/internal/v1/health"
)

type config struct {
	databasePath  string
	listenAddress string
	bearerToken   string
	stepDelay     time.Duration
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := checkHealth(context.Background(), defaultHealthURL); err != nil {
			log.Fatal(err)
		}
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
	bearerToken := getenv("COMMITARIUM_WORKER_TOKEN")
	if bearerToken == "" || strings.IndexFunc(bearerToken, unicode.IsSpace) >= 0 {
		return config{}, errors.New("COMMITARIUM_WORKER_TOKEN is required and cannot contain whitespace")
	}
	listenAddress := strings.TrimSpace(getenv("COMMITARIUM_WORKER_LISTEN_ADDRESS"))
	if listenAddress == "" {
		listenAddress = defaultListenAddress
	}
	stepDelay := defaultStepDelay
	if value := strings.TrimSpace(getenv("COMMITARIUM_WORKER_STEP_DELAY")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 0 {
			return config{}, fmt.Errorf("COMMITARIUM_WORKER_STEP_DELAY must be a nonnegative duration")
		}
		stepDelay = parsed
	}
	return config{
		databasePath:  databasePath,
		listenAddress: listenAddress,
		bearerToken:   bearerToken,
		stepDelay:     stepDelay,
	}, nil
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

	scripted := worker.NewRepeatingScriptedAdapter("codex-simulated", simulatedScripts())
	provider := worker.NewAutomaticScriptedAdapter(scripted, workerConfig.stepDelay)
	workingDirectory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve simulated worker directory: %w", err)
	}
	service, recovery, err := workerservice.New(ctx, workerservice.Config{
		Journal:             workerjournal.NewStore(db),
		Provider:            provider,
		EnvironmentResolver: simulatedEnvironmentResolver(workingDirectory),
		Normalizer:          workerservice.NormalizerFunc(normalizeSimulatedEvent),
		Lifetime:            ctx,
	})
	if err != nil {
		return fmt.Errorf("create worker service: %w", err)
	}
	if recovery.AttemptsMarked > 0 || recovery.MutationsMarked > 0 {
		log.Printf(
			"worker startup marked %d attempt(s) and %d command(s) indeterminate",
			recovery.AttemptsMarked,
			recovery.MutationsMarked,
		)
	}
	handler, err := workerhttp.NewServer(workerhttp.ServerConfig{
		BearerToken: workerConfig.bearerToken,
		Provider:    workerhttp.ProviderCodex,
		Capabilities: []workerhttp.Capability{
			workerhttp.CapabilityStart,
			workerhttp.CapabilityResume,
			workerhttp.CapabilityMessage,
			workerhttp.CapabilityPause,
			workerhttp.CapabilityContinue,
			workerhttp.CapabilityCooperativeStop,
			workerhttp.CapabilityEventReplay,
		},
		MaxConcurrentAttempts: 1,
		EventSource:           service,
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
		log.Printf("simulated Codex worker listening on %s", workerConfig.listenAddress)
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

// The deterministic worker has no provider credentials or repository
// processes. It still fills the same launch contract so the service boundary
// is exercised without pretending that this is a real materialization lookup.
func simulatedEnvironmentResolver(directory string) workerservice.EnvironmentResolver {
	return workerservice.EnvironmentResolverFunc(func(
		ctx context.Context,
		assignment workerhttp.Assignment,
	) (worker.LaunchEnvironment, error) {
		if err := ctx.Err(); err != nil {
			return worker.LaunchEnvironment{}, err
		}
		return worker.LaunchEnvironment{
			AgentProfileID:        assignment.AgentProfileID,
			ProjectID:             assignment.ProjectID,
			FeatureID:             assignment.FeatureID,
			Role:                  worker.Role(assignment.Role),
			WorkspaceID:           assignment.WorkspaceID,
			ConfigurationRevision: assignment.ConfigurationRevision,
			MaterializationDigest: assignment.MaterializationDigest,
			WorkingDirectory:      directory,
			Variables:             []string{},
		}, nil
	})
}

func normalizeSimulatedEvent(
	_ context.Context,
	event worker.Event,
) (workerservice.NormalizedEvent, error) {
	switch event.Type {
	case worker.EventMessage,
		worker.EventActivity,
		worker.EventInputRequired,
		worker.EventPauseAcknowledged,
		worker.EventContinued,
		worker.EventRecoveryAssessment:
	default:
		return workerservice.NormalizedEvent{}, fmt.Errorf("unsupported simulated event type %q", event.Type)
	}
	normalized := workerservice.NormalizedEvent{
		Type:      workerhttp.EventType(event.Type),
		Text:      event.Text,
		Redaction: workerhttp.RedactionMetadata{},
	}
	if event.RecoveryAssessment != nil {
		normalized.RecoveryAssessment = &workerhttp.RecoveryAssessment{
			Consistent:         event.RecoveryAssessment.Consistent,
			RequiresUserReview: event.RecoveryAssessment.RequiresUserReview,
		}
	}
	return normalized, nil
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
