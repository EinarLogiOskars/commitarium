package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	coordinatordatabase "github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/httpapi"
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
	handler := httpapi.New(projectService, featureService, workflowService)

	log.Print("Listening...")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		return fmt.Errorf("serve coordinator API: %w", err)
	}

	return nil
}
