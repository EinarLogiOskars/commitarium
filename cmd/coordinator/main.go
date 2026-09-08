package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	coordinatordatabase "github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/httpapi"
	"github.com/EinarLogiOskars/commitarium/internal/orchestration"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	databasePath := os.Getenv("COMMITARIUM_DATABASE_PATH")
	if databasePath == "" {
		return fmt.Errorf("COMMITARIUM_DATABASE_PATH is required")
	}

	db, err := coordinatordatabase.OpenSQLite(ctx, databasePath)
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
	projectService := project.NewService(projectStore)
	featureStore := coordinatordatabase.NewFeatureStore(db)
	featureService := feature.NewService(featureStore, projectService)
	workflowStore := coordinatordatabase.NewWorkflowStore(db)
	workflowService := workflow.NewService(workflowStore)
	executionStore := coordinatordatabase.NewExecutionStore(db)
	executionService := execution.NewService(executionStore)
	activeSessions := orchestration.NewActiveSessions()
	sessionController := orchestration.NewController(executionService, activeSessions)
	runner := orchestration.NewRunner(workflowService, executionService, activeSessions)
	simulatedStepDelay := 250 * time.Millisecond
	if configuredDelay := os.Getenv("COMMITARIUM_SIMULATED_STEP_DELAY"); configuredDelay != "" {
		simulatedStepDelay, err = time.ParseDuration(configuredDelay)
		if err != nil {
			return fmt.Errorf("parse COMMITARIUM_SIMULATED_STEP_DELAY: %w", err)
		}
	}
	runStarter := orchestration.NewStarter(
		runner,
		func() orchestration.Assignment {
			return orchestration.NewSimulatedAssignment(simulatedStepDelay)
		},
		2,
		3,
	)
	recoverer := orchestration.NewRecoverer(
		executionService,
		featureStore,
		projectService,
		runStarter,
	)
	recoveredRuns, recoveryErr := recoverer.RecoverAll(ctx)
	if recoveryErr != nil {
		log.Printf("recover interrupted workflows: %v", recoveryErr)
	}
	if recoveredRuns > 0 {
		log.Printf("recovering %d interrupted workflow(s)", recoveredRuns)
	}
	handler := httpapi.New(
		projectService,
		featureService,
		workflowService,
		executionService,
		sessionController,
		runStarter,
	)

	log.Print("Listening...")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		return fmt.Errorf("serve coordinator API: %w", err)
	}

	return nil
}
