package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMigrateCreatesProjectsTable(t *testing.T) {
	db, err := OpenSQLite(
		t.Context(),
		filepath.Join(t.TempDir(), "coordinator.db"),
	)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf(
			"migrate database a second time: %v",
			err,
		)
	}

	var tableName string
	if err := db.QueryRowContext(
		t.Context(),
		`
			SELECT name
			FROM sqlite_schema
			WHERE type = 'table' AND name = 'projects'
		`,
	).Scan(&tableName); err != nil {
		t.Fatalf("find projects table: %v", err)
	}

	if tableName != "projects" {
		t.Errorf(
			"expected table %q, got %q",
			"projects",
			tableName,
		)
	}
}

func TestMigrateReturnsCanceledContext(t *testing.T) {
	db, err := OpenSQLite(
		t.Context(),
		filepath.Join(t.TempDir(), "coordinator.db"),
	)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = Migrate(ctx, db)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"expected context cancellation, got %v",
			err,
		)
	}
}
