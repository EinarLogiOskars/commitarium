package database

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
)

func TestFeatureDeletionStoreFencesNewRunsAndPurgesInternalArtifacts(t *testing.T) {
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, time.September, 13, 11, 0, 0, 0, time.UTC)
	projectStore := NewProjectStore(db)
	if err := projectStore.Create(t.Context(), project.Project{
		ID: "prj_delete", Name: "Delete", CreatedAt: now,
		RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		DialogueLimits: project.DefaultDialogueLimits(),
		AgentProviders: project.DefaultAgentProviders(),
		MergePolicy:    project.MergePolicyRequireUserApproval,
		AutonomyPolicy: project.AutonomyPolicyReviewEachPhase,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	featureStore := NewFeatureStore(db)
	if err := featureStore.Create(t.Context(), feature.Feature{
		ID: "fea_delete", ProjectID: "prj_delete", Title: "Delete me",
		State: feature.StateCancelled, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	timestamp := now.Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO workflow_events (id, aggregate_id, event_type, actor_kind, actor_id, occurred_at, sequence, payload_version, idempotency_key, payload)
		 VALUES ('evt_delete', 'fea_delete', 'feature.state_changed', 'user', 'local-user', ?, 1, 1, 'cancel-delete', '{}')`,
		`INSERT INTO runs (id, feature_id, status, reason, started_at, updated_at, ended_at)
		 VALUES ('run_delete', 'fea_delete', 'stopped', 'stopped', ?, ?, ?)`,
		`INSERT INTO sessions (id, run_id, agent_id, role, status, provider_session_id, started_at, updated_at, ended_at)
		 VALUES ('ses_delete', 'run_delete', 'codex-lead', 'lead', 'stopped', 'provider-delete', ?, ?, ?)`,
		`INSERT INTO session_events (id, session_id, sequence, event_type, text, occurred_at)
		 VALUES ('sev_delete', 'ses_delete', 1, 'activity', 'stopped', ?)`,
		`INSERT INTO session_commands (id, session_id, command_type, message, status, requested_at, applied_at, error)
		 VALUES ('cmd_delete', 'ses_delete', 'stop', '', 'applied', ?, ?, '')`,
		`INSERT INTO worker_attempt_checkpoints (session_id, attempt_id, last_event_sequence, created_at, updated_at)
		 VALUES ('ses_delete', 'attempt-delete', 0, ?, ?)`,
		`INSERT INTO planning_messages (run_id, sequence, session_event_id, linked_at)
		 VALUES ('run_delete', 1, 'sev_delete', ?)`,
		`INSERT INTO run_actions (id, run_id, action, occurred_at)
		 VALUES ('action-delete', 'run_delete', 'pause', ?)`,
	}
	for index, statement := range statements {
		arguments := []any{timestamp}
		switch index {
		case 1, 2:
			arguments = []any{timestamp, timestamp, timestamp}
		case 4, 5:
			arguments = []any{timestamp, timestamp}
		}
		if _, err := db.ExecContext(t.Context(), statement, arguments...); err != nil {
			t.Fatalf("insert artifact %d: %v", index, err)
		}
	}
	store := NewFeatureDeletionStore(db)
	if _, err := store.BeginDeletion(t.Context(), "prj_delete", "fea_delete"); err != nil {
		t.Fatalf("claim deletion: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO runs (id, feature_id, status, reason, started_at, updated_at)
		VALUES ('run_too_late', 'fea_delete', 'running', '', ?, ?)`, timestamp, timestamp); err == nil {
		t.Fatal("deletion claim did not fence new run admission")
	}
	if err := store.FinishDeletion(t.Context(), "prj_delete", "fea_delete"); err != nil {
		t.Fatalf("finish deletion: %v", err)
	}
	for _, table := range []string{
		"features", "workflow_events", "runs", "sessions", "session_events",
		"session_commands", "worker_attempt_checkpoints", "planning_messages",
		"run_actions", "feature_deletions",
	} {
		var count int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table %s was not cleaned: count=%d err=%v", table, count, err)
		}
	}
	if _, err := projectStore.GetByID(t.Context(), "prj_delete"); err != nil {
		t.Fatalf("deletion removed project/default-branch metadata: %v", err)
	}
	if _, err := store.BeginDeletion(t.Context(), "prj_delete", "fea_delete"); !errors.Is(err, feature.ErrNotFound) {
		t.Fatalf("missing deletion should return feature not found, got %v", err)
	}
}

func TestFeatureDeletionStoreRefusesRunningWork(t *testing.T) {
	store, db := newFeatureDeletionTestStore(t)
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO runs (id, feature_id, status, reason, started_at, updated_at)
		VALUES ('run_active', 'fea_delete', 'running', '', ?, ?)`, timestamp, timestamp); err != nil {
		t.Fatalf("insert active run: %v", err)
	}
	if _, err := store.BeginDeletion(t.Context(), "prj_delete", "fea_delete"); !errors.Is(err, workorder.ErrActive) {
		t.Fatalf("expected active error, got %v", err)
	}
}

func newFeatureDeletionTestStore(t *testing.T) (*FeatureDeletionStore, *sql.DB) {
	t.Helper()
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Now().UTC()
	if err := NewProjectStore(db).Create(t.Context(), project.Project{
		ID: "prj_delete", Name: "Delete", CreatedAt: now,
		RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		DialogueLimits: project.DefaultDialogueLimits(),
		AgentProviders: project.DefaultAgentProviders(),
		MergePolicy:    project.MergePolicyRequireUserApproval,
		AutonomyPolicy: project.AutonomyPolicyReviewEachPhase,
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := NewFeatureStore(db).Create(t.Context(), feature.Feature{
		ID: "fea_delete", ProjectID: "prj_delete", Title: "Delete me",
		State: feature.StateDraft, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	return NewFeatureDeletionStore(db), db
}
