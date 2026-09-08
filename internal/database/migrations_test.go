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

func TestMigrateCreatesExecutionTables(t *testing.T) {
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

	rows, err := db.QueryContext(
		t.Context(),
		`SELECT name
		 FROM sqlite_schema
		 WHERE type = 'table'
		   AND name IN ('runs', 'sessions', 'session_events', 'session_commands')`,
	)
	if err != nil {
		t.Fatalf("query execution tables: %v", err)
	}
	defer rows.Close()

	found := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table names: %v", err)
	}
	for _, name := range []string{"runs", "sessions", "session_events", "session_commands"} {
		if !found[name] {
			t.Errorf("expected table %q to exist", name)
		}
	}
}
