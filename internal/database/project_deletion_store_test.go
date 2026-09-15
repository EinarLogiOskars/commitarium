package database

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/projectdeletion"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
)

func TestProjectDeletionStorePurgesOwnedDatabaseGraphAndKeepsOtherProject(t *testing.T) {
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "coordinator.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	now := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	projects := NewProjectStore(db)
	createDeletionTestProject(t, projects, "prj_delete", "delete-repo", now)
	createDeletionTestProject(t, projects, "prj_keep", "keep-repo", now.Add(time.Second))
	features := NewFeatureStore(db)
	for _, item := range []feature.Feature{
		{ID: "fea_delete", ProjectID: "prj_delete", Title: "Delete", State: feature.StateCancelled, CreatedAt: now, UpdatedAt: now},
		{ID: "fea_keep", ProjectID: "prj_keep", Title: "Keep", State: feature.StateCancelled, CreatedAt: now, UpdatedAt: now},
	} {
		if err := features.Create(t.Context(), item); err != nil {
			t.Fatalf("create feature %s: %v", item.ID, err)
		}
	}
	timestamp := now.Format(time.RFC3339Nano)
	for _, suffix := range []string{"delete", "keep"} {
		if _, err := db.ExecContext(t.Context(), `
			INSERT INTO runs (id, feature_id, status, reason, started_at, updated_at, ended_at)
			VALUES (?, ?, 'stopped', 'done', ?, ?, ?)`,
			"run_"+suffix, "fea_"+suffix, timestamp, timestamp, timestamp); err != nil {
			t.Fatalf("insert %s run: %v", suffix, err)
		}
		if _, err := db.ExecContext(t.Context(), `
			INSERT INTO sessions (id, run_id, agent_id, role, status, provider_session_id, started_at, updated_at, ended_at)
			VALUES (?, ?, 'codex-lead', 'lead', 'stopped', ?, ?, ?, ?)`,
			"ses_"+suffix, "run_"+suffix, "provider_"+suffix, timestamp, timestamp, timestamp); err != nil {
			t.Fatalf("insert %s session: %v", suffix, err)
		}
		if _, err := db.ExecContext(t.Context(), `
			INSERT INTO session_events (id, session_id, sequence, event_type, text, occurred_at)
			VALUES (?, ?, 1, 'activity', 'done', ?)`, "sev_"+suffix, "ses_"+suffix, timestamp); err != nil {
			t.Fatalf("insert %s event: %v", suffix, err)
		}
	}

	store := NewProjectDeletionStore(db)
	deletion, err := store.BeginDeletion(t.Context(), "prj_delete", "delete-project-1", false)
	if err != nil || deletion.Repository == nil || deletion.Repository.Name != "delete-repo" {
		t.Fatalf("begin deletion=%+v err=%v", deletion, err)
	}
	ids, err := store.ListFeatureIDs(t.Context(), "prj_delete")
	if err != nil || len(ids) != 1 || ids[0] != "fea_delete" {
		t.Fatalf("feature ids=%v err=%v", ids, err)
	}
	featureDeletion := workorder.NewService(NewFeatureDeletionStore(db), nil, nil)
	if _, err := featureDeletion.Delete(t.Context(), "prj_delete", "fea_delete"); err != nil {
		t.Fatalf("delete feature graph: %v", err)
	}
	if err := store.FinishDeletion(t.Context(), "prj_delete", "delete-project-1"); err != nil {
		t.Fatalf("finish project deletion: %v", err)
	}
	if _, err := projects.GetByID(t.Context(), "prj_delete"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("deleted project still exists: %v", err)
	}
	if _, err := projects.GetByID(t.Context(), "prj_keep"); err != nil {
		t.Fatalf("other project was removed: %v", err)
	}
	for table, want := range map[string]int{
		"projects": 1, "features": 1, "runs": 1, "sessions": 1, "session_events": 1,
	} {
		var count int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != want {
			t.Fatalf("table %s count=%d want=%d err=%v", table, count, want, err)
		}
	}
	replayed, err := store.BeginDeletion(t.Context(), "prj_delete", "delete-project-1", false)
	if err != nil || !replayed.Completed {
		t.Fatalf("completed retry=%+v err=%v", replayed, err)
	}
	if _, err := store.BeginDeletion(t.Context(), "prj_delete", "delete-project-2", false); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("second delete should be not found, got %v", err)
	}
	if _, err := store.BeginDeletion(t.Context(), "prj_unknown", "delete-unknown", false); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("unknown delete should be not found, got %v", err)
	}
}

func TestProjectDeletionStoreRefusesActiveRunUnlessForcedAndFencesNewWork(t *testing.T) {
	store, db := newProjectDeletionTestStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO runs (id, feature_id, status, reason, started_at, updated_at)
		VALUES ('run_active', 'fea_delete', 'running', '', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert active run: %v", err)
	}
	if _, err := store.BeginDeletion(t.Context(), "prj_delete", "default-delete", false); !errors.Is(err, projectdeletion.ErrActive) {
		t.Fatalf("expected active refusal, got %v", err)
	}
	var claims int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM project_deletions`).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("active refusal left a claim: count=%d err=%v", claims, err)
	}
	if _, err := store.BeginDeletion(t.Context(), "prj_delete", "force-delete", true); err != nil {
		t.Fatalf("force claim: %v", err)
	}
	if err := NewFeatureStore(db).Create(t.Context(), feature.Feature{
		ID: "fea_too_late", ProjectID: "prj_delete", Title: "Late", State: feature.StateDraft,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("project deletion claim did not fence new features")
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO runs (id, feature_id, status, reason, started_at, updated_at)
		VALUES ('run_too_late', 'fea_delete', 'running', '', ?, ?)`, now, now); err == nil {
		t.Fatal("project deletion claim did not fence new runs")
	}
}

func TestProjectDeletionStoreRejectsRepositorySharedByAnotherProject(t *testing.T) {
	store, db := newProjectDeletionTestStore(t)
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO projects (
			id, name, recovery_policy, merge_policy, autonomy_policy,
			planning_round_limit, implementation_review_round_limit,
			lead_provider, reviewer_provider, forgejo_owner, forgejo_repository,
			forgejo_default_branch, forgejo_bound_at, created_at
		) SELECT 'prj_other', 'Other', recovery_policy, merge_policy, autonomy_policy,
			planning_round_limit, implementation_review_round_limit,
			lead_provider, reviewer_provider, forgejo_owner, forgejo_repository,
			forgejo_default_branch, forgejo_bound_at, created_at
		FROM projects WHERE id = 'prj_delete'`); err != nil {
		t.Fatalf("create conflicting project: %v", err)
	}
	if _, err := store.BeginDeletion(t.Context(), "prj_delete", "delete-shared", false); !errors.Is(err, projectdeletion.ErrUnsafeArtifacts) {
		t.Fatalf("expected shared repository conflict, got %v", err)
	}
}

func newProjectDeletionTestStore(t *testing.T) (*ProjectDeletionStore, *sql.DB) {
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
	projects := NewProjectStore(db)
	createDeletionTestProject(t, projects, "prj_delete", "delete-repo", now)
	if err := NewFeatureStore(db).Create(t.Context(), feature.Feature{
		ID: "fea_delete", ProjectID: "prj_delete", Title: "Delete", State: feature.StateDraft,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create feature: %v", err)
	}
	return NewProjectDeletionStore(db), db
}

func createDeletionTestProject(t *testing.T, store *ProjectStore, id, repository string, now time.Time) {
	t.Helper()
	if err := store.Create(t.Context(), project.Project{
		ID: id, Name: id, RecoveryPolicy: project.RecoveryPolicyApprovalRequired,
		DialogueLimits: project.DefaultDialogueLimits(), AgentProviders: project.DefaultAgentProviders(),
		MergePolicy: project.MergePolicyRequireUserApproval, AutonomyPolicy: project.AutonomyPolicyReviewEachPhase,
		ForgejoRepository: &project.ForgejoRepository{
			Owner: "owner", Name: repository, DefaultBranch: "main", BoundAt: now,
		}, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create project %s: %v", id, err)
	}
}
