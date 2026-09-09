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
		`INSERT INTO session_events (id, session_id, sequence, event_type, text, occurred_at)
		 VALUES ('sev_old', 'ses_old', 1, 'activity', 'old activity', ?)`,
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
	events, err := NewExecutionStore(db).ListEvents(t.Context(), "ses_old")
	if err != nil {
		t.Fatalf("list migrated events: %v", err)
	}
	if len(events) != 1 || events[0].WorkerAttemptID != "" || events[0].WorkerEventSequence != 0 {
		t.Fatalf("unexpected migrated events %+v", events)
	}
}

func TestWaitingSessionMigrationPreservesDependentRecords(t *testing.T) {
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
	if _, err := provider.UpTo(t.Context(), 6); err != nil {
		t.Fatalf("migrate version-six schema: %v", err)
	}
	now := time.Date(2026, time.September, 9, 16, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects (id, name, recovery_policy, created_at)
		  VALUES ('prj_waiting', 'Waiting project', 'approval_required', ?)`, []any{now}},
		{`INSERT INTO features (id, project_id, title, description, state, created_at, updated_at)
		  VALUES ('fea_waiting', 'prj_waiting', 'Clarify goal', '', 'draft', ?, ?)`, []any{now, now}},
		{`INSERT INTO runs (id, feature_id, status, reason, started_at, updated_at, ended_at)
		  VALUES ('run_waiting', 'fea_waiting', 'running', '', ?, ?, NULL)`, []any{now, now}},
		{`INSERT INTO sessions (
			id, run_id, agent_id, role, status, provider_session_id,
			started_at, updated_at, ended_at, outcome, disposition, summary, recovery_attempt
		  ) VALUES ('ses_waiting', 'run_waiting', 'codex-lead', 'lead', 'running',
		            'provider_waiting', ?, ?, NULL, '', '', '', 0)`, []any{now, now}},
		{`INSERT INTO session_events (
			id, session_id, sequence, event_type, text, occurred_at,
			worker_attempt_id, worker_event_sequence
		  ) VALUES ('sev_waiting', 'ses_waiting', 1, 'message', 'Please clarify', ?,
		            'att_waiting', 1)`, []any{now}},
		{`INSERT INTO session_commands (
			id, session_id, command_type, message, status, requested_at, applied_at, error
		  ) VALUES ('cmd_waiting', 'ses_waiting', 'message', 'More detail', 'pending', ?, NULL, '')`, []any{now}},
		{`INSERT INTO worker_attempt_checkpoints (
			session_id, attempt_id, last_event_sequence, created_at, updated_at
		  ) VALUES ('ses_waiting', 'att_waiting', 1, ?, ?)`, []any{now, now}},
	}
	for index, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatalf("insert version-six record %d: %v", index, err)
		}
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("apply waiting-session migration: %v", err)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE sessions SET status = 'waiting_for_user' WHERE id = 'ses_waiting'`,
	); err != nil {
		t.Fatalf("store waiting session: %v", err)
	}
	for table, want := range map[string]int{
		"sessions": 1, "session_events": 1, "session_commands": 1,
		"worker_attempt_checkpoints": 1,
	} {
		var got int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != want {
			t.Errorf("%s count=%d, want %d", table, got, want)
		}
	}
	rows, err := db.QueryContext(t.Context(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatalf("check foreign keys: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("waiting-session migration left a broken foreign key")
	}
}

func TestGoalAcceptanceMigrationPreservesExistingFeatures(t *testing.T) {
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
	if _, err := provider.UpTo(t.Context(), 7); err != nil {
		t.Fatalf("migrate version-seven schema: %v", err)
	}
	now := time.Date(2026, time.September, 9, 17, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.ExecContext(
		t.Context(),
		`INSERT INTO projects (id, name, recovery_policy, created_at)
		 VALUES ('prj_goal', 'Goal project', 'approval_required', ?)`,
		now,
	); err != nil {
		t.Fatalf("insert old project: %v", err)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`INSERT INTO features (id, project_id, title, description, state, created_at, updated_at)
		 VALUES ('fea_goal', 'prj_goal', 'Clarify goal', '', 'draft', ?, ?)`,
		now, now,
	); err != nil {
		t.Fatalf("insert old feature: %v", err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("apply goal-acceptance migration: %v", err)
	}

	stored, err := NewFeatureStore(db).GetByID(t.Context(), "fea_goal")
	if err != nil {
		t.Fatalf("get migrated feature: %v", err)
	}
	if stored.AcceptedGoal != "" || stored.GoalAcceptedAt != nil {
		t.Fatalf("old feature acquired a false acceptance: %+v", stored)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE features SET accepted_goal = 'Ship it' WHERE id = 'fea_goal'`,
	); err == nil {
		t.Fatal("schema accepted a goal without its acceptance time")
	}
}

func TestForgejoRepositoryMigrationPreservesExistingProjects(t *testing.T) {
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
	if _, err := provider.UpTo(t.Context(), 8); err != nil {
		t.Fatalf("migrate version-eight schema: %v", err)
	}
	now := time.Date(2026, time.September, 9, 18, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.ExecContext(
		t.Context(),
		`INSERT INTO projects (id, name, recovery_policy, created_at)
		 VALUES ('prj_existing', 'Existing project', 'approval_required', ?)`,
		now,
	); err != nil {
		t.Fatalf("insert existing project: %v", err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("apply Forgejo repository migration: %v", err)
	}
	stored, err := NewProjectStore(db).GetByID(t.Context(), "prj_existing")
	if err != nil {
		t.Fatalf("get migrated project: %v", err)
	}
	if stored.ForgejoRepository != nil {
		t.Fatalf("existing project acquired a false repository binding: %+v", stored)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE projects SET forgejo_owner = 'owner' WHERE id = 'prj_existing'`,
	); err == nil {
		t.Fatal("schema accepted a partial Forgejo repository binding")
	}
}

func TestWorkspaceMigrationPreservesExistingFeatures(t *testing.T) {
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
	if _, err := provider.UpTo(t.Context(), 9); err != nil {
		t.Fatalf("migrate version-nine schema: %v", err)
	}
	now := time.Date(2026, time.September, 9, 20, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	if _, err := db.ExecContext(
		t.Context(),
		`INSERT INTO projects (id, name, recovery_policy, created_at)
		 VALUES ('prj_workspace', 'Workspace project', 'approval_required', ?)`,
		now,
	); err != nil {
		t.Fatalf("insert existing project: %v", err)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`INSERT INTO features (
			id, project_id, title, description, state,
			accepted_goal, goal_accepted_at, created_at, updated_at
		 ) VALUES (
			'fea_workspace', 'prj_workspace', 'Workspace feature', '', 'draft',
			'', NULL, ?, ?
		 )`,
		now, now,
	); err != nil {
		t.Fatalf("insert existing feature: %v", err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("apply workspace migration: %v", err)
	}
	if _, err := NewFeatureStore(db).GetByID(t.Context(), "fea_workspace"); err != nil {
		t.Fatalf("get preserved feature: %v", err)
	}
	var workspaces int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM feature_workspaces`).Scan(&workspaces); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if workspaces != 0 {
		t.Fatalf("existing feature acquired a false workspace")
	}
}

func TestCheckoutMigrationPreservesExistingWorkspace(t *testing.T) {
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
	if _, err := provider.UpTo(t.Context(), 10); err != nil {
		t.Fatalf("migrate version-ten schema: %v", err)
	}
	now := time.Date(2026, time.September, 9, 21, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO projects (id, name, recovery_policy, created_at)
		 VALUES ('prj_checkout', 'Checkout project', 'approval_required', ?)`,
		`INSERT INTO features (
			id, project_id, title, description, state,
			accepted_goal, goal_accepted_at, created_at, updated_at
		 ) VALUES ('fea_checkout', 'prj_checkout', 'Checkout feature', '', 'draft',
		           'Ship it', ?, ?, ?)`,
		`INSERT INTO feature_workspaces (
			feature_id, id, project_id, repository_owner, repository_name,
			base_branch, branch_name, base_commit_id, status,
			branch_created_at, created_at, updated_at
		 ) VALUES ('fea_checkout', 'wsp_fea_checkout', 'prj_checkout', 'owner', 'repository',
		           'main', 'commitarium/fea_checkout',
		           '0123456789abcdef0123456789abcdef01234567', 'branch_ready', ?, ?, ?)`,
	}
	for index, statement := range statements {
		arguments := []any{now}
		if index > 0 {
			arguments = []any{now, now, now}
		}
		if _, err := db.ExecContext(t.Context(), statement, arguments...); err != nil {
			t.Fatalf("insert version-ten record %d: %v", index, err)
		}
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("apply checkout migration: %v", err)
	}
	stored, err := NewWorkspaceStore(db).GetByFeatureID(t.Context(), "fea_checkout")
	if err != nil {
		t.Fatalf("get preserved workspace: %v", err)
	}
	if stored.CheckoutReady() || stored.CheckoutRelativePath != "" || stored.CheckoutCreatedAt != nil {
		t.Fatalf("existing workspace acquired a false checkout: %+v", stored)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE feature_workspaces
		 SET checkout_relative_path = 'wsp_fea_checkout'
		 WHERE feature_id = 'fea_checkout'`,
	); err == nil {
		t.Fatal("schema accepted a checkout path without its creation time")
	}
}

func TestPullRequestMigrationPreservesExistingCheckout(t *testing.T) {
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
	if _, err := provider.UpTo(t.Context(), 11); err != nil {
		t.Fatalf("migrate version-eleven schema: %v", err)
	}
	now := time.Date(2026, time.September, 9, 22, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects (id, name, recovery_policy, created_at)
		  VALUES ('prj_pr', 'Pull request project', 'approval_required', ?)`, []any{now}},
		{`INSERT INTO features (
			id, project_id, title, description, state,
			accepted_goal, goal_accepted_at, created_at, updated_at
		  ) VALUES ('fea_pr', 'prj_pr', 'Pull request feature', '', 'draft',
		            'Ship it', ?, ?, ?)`, []any{now, now, now}},
		{`INSERT INTO feature_workspaces (
			feature_id, id, project_id, repository_owner, repository_name,
			base_branch, branch_name, base_commit_id, status, branch_created_at,
			checkout_relative_path, checkout_created_at, created_at, updated_at
		  ) VALUES ('fea_pr', 'wsp_fea_pr', 'prj_pr', 'owner', 'repository',
		            'main', 'commitarium/fea_pr',
		            '0123456789abcdef0123456789abcdef01234567', 'branch_ready', ?,
		            'wsp_fea_pr', ?, ?, ?)`, []any{now, now, now, now}},
	}
	for index, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatalf("insert version-eleven record %d: %v", index, err)
		}
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("apply pull request migration: %v", err)
	}
	stored, err := NewWorkspaceStore(db).GetByFeatureID(t.Context(), "fea_pr")
	if err != nil {
		t.Fatalf("get preserved workspace: %v", err)
	}
	if stored.PullRequestReady() || stored.PullRequestNumber != 0 ||
		stored.PullRequestURL != "" || stored.PullRequestRecordedAt != nil {
		t.Fatalf("existing workspace acquired a false pull request: %+v", stored)
	}
	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE feature_workspaces
		 SET pull_request_number = 7
		 WHERE feature_id = 'fea_pr'`,
	); err == nil {
		t.Fatal("schema accepted a pull request number without its URL and recording time")
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
		   AND name IN ('runs', 'sessions', 'session_events', 'session_commands',
		                'worker_attempt_checkpoints')`,
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
	for _, name := range []string{
		"runs", "sessions", "session_events", "session_commands", "worker_attempt_checkpoints",
	} {
		if !found[name] {
			t.Errorf("expected table %q to exist", name)
		}
	}
}
