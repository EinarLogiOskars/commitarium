package database

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/pressly/goose/v3"
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

func TestRecoveryMigrationPreservesExistingExecutionRecords(t *testing.T) {
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatalf("open embedded migrations: %v", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	if _, err := provider.UpTo(t.Context(), 4); err != nil {
		t.Fatalf("migrate old schema: %v", err)
	}
	now := time.Date(2026, time.September, 8, 18, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO projects (id, name, created_at) VALUES ('prj_old', 'Old project', ?)`,
		`INSERT INTO features (id, project_id, title, description, state, created_at, updated_at)
		 VALUES ('fea_old', 'prj_old', 'Old feature', '', 'planning', ?, ?)`,
		`INSERT INTO runs (id, feature_id, status, reason, started_at, updated_at, ended_at)
		 VALUES ('run_old', 'fea_old', 'running', '', ?, ?, NULL)`,
		`INSERT INTO sessions (id, run_id, agent_id, role, status, provider_session_id, started_at, updated_at, ended_at)
		 VALUES ('ses_old', 'run_old', 'agt_old', 'lead', 'running', 'provider_old', ?, ?, NULL)`,
	}
	for index, statement := range statements {
		arguments := []any{now}
		if index == 1 || index == 2 || index == 3 {
			arguments = []any{now, now}
		}
		if _, err := db.ExecContext(t.Context(), statement, arguments...); err != nil {
			t.Fatalf("insert old-schema record %d: %v", index, err)
		}
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("apply recovery migration: %v", err)
	}

	storedProject, err := NewProjectStore(db).GetByID(t.Context(), "prj_old")
	if err != nil {
		t.Fatalf("get migrated project: %v", err)
	}
	if storedProject.RecoveryPolicy != project.RecoveryPolicyApprovalRequired {
		t.Fatalf("unexpected migrated recovery policy %+v", storedProject)
	}
	storedSession, err := NewExecutionStore(db).GetSession(t.Context(), "ses_old")
	if err != nil {
		t.Fatalf("get migrated session: %v", err)
	}
	if storedSession.ProviderSessionID != "provider_old" || storedSession.RecoveryAttempt != 0 {
		t.Fatalf("unexpected migrated session %+v", storedSession)
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
