package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	coordinatordatabase "github.com/EinarLogiOskars/commitarium/internal/database"
	"github.com/EinarLogiOskars/commitarium/internal/httpapi"
	"github.com/EinarLogiOskars/commitarium/internal/project"
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

	store := coordinatordatabase.NewProjectStore(db)
	projectService := project.NewService(store)
	handler := httpapi.New(projectService)

	log.Print("Listening...")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		return fmt.Errorf("serve coordinator API: %w", err)
	}

	return nil
}
