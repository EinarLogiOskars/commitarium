package database

import (
	"reflect"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/projectenvironment"
	"github.com/EinarLogiOskars/commitarium/internal/validation"
)

func TestEnvironmentAndValidationStoresPersistLifecycle(t *testing.T) {
	db, executions := newTestExecutionStore(t)
	run, session := createExecutionRecords(t, db, executions)
	now := run.StartedAt.Add(time.Minute)

	environments := NewProjectEnvironmentStore(db)
	request := projectenvironment.Request{
		ID: "env_test", ProjectID: "prj_execution_test", FeatureID: run.FeatureID,
		RunID: run.ID, SessionID: session.ID, AttemptID: "attempt_test",
		SystemPackages: []string{"libvips-dev"}, Reason: "Image support needs libvips.",
		Status: projectenvironment.StatusRequested, ResolvedPackages: map[string]string{},
		RequestedAt: now, UpdatedAt: now,
	}
	stored, created, err := environments.Create(t.Context(), request)
	if err != nil || !created || stored.ID != request.ID {
		t.Fatalf("create environment request = %#v, %t, %v", stored, created, err)
	}
	if _, changed, err := environments.Transition(t.Context(), request.ID,
		projectenvironment.StatusRequested, projectenvironment.StatusApproved,
		map[string]string{}, "", now.Add(time.Second)); err != nil || !changed {
		t.Fatalf("approve environment request = %t, %v", changed, err)
	}
	packages, err := environments.ListApprovedPackages(t.Context())
	if err != nil || !reflect.DeepEqual(packages, []string{"libvips-dev"}) {
		t.Fatalf("approved packages = %v, %v", packages, err)
	}

	validations := NewValidationStore(db)
	config := validation.Config{
		ProjectID: "prj_execution_test", Commands: []string{"go test ./..."},
		UpdatedAt: now,
	}
	if storedConfig, err := validations.PutConfig(t.Context(), config); err != nil ||
		!reflect.DeepEqual(storedConfig.Commands, config.Commands) {
		t.Fatalf("put validation config = %#v, %v", storedConfig, err)
	}
	job := validation.Job{
		ID: "val_test", ProjectID: config.ProjectID, FeatureID: run.FeatureID,
		RunID: run.ID, WorkspaceID: "wsp_test",
		CommitID: "0123456789abcdef0123456789abcdef01234567",
		Commands: config.Commands, Status: validation.StatusPending,
		Results: []validation.CommandResult{}, CreatedAt: now, UpdatedAt: now,
	}
	storedJob, created, err := validations.CreateJob(t.Context(), job)
	if err != nil || !created || storedJob.ID != job.ID {
		t.Fatalf("create validation job = %#v, %t, %v", storedJob, created, err)
	}
	running, changed, err := validations.TransitionJob(t.Context(), job.ID,
		validation.StatusPending, validation.StatusRunning, []validation.CommandResult{}, "",
		now.Add(2*time.Second))
	if err != nil || !changed || running.Status != validation.StatusRunning {
		t.Fatalf("claim validation job = %#v, %t, %v", running, changed, err)
	}
	result := []validation.CommandResult{{
		Command: "go test ./...", ExitCode: 0, Output: "ok", DurationMS: 12,
	}}
	passed, changed, err := validations.TransitionJob(t.Context(), job.ID,
		validation.StatusRunning, validation.StatusPassed, result, "", now.Add(3*time.Second))
	if err != nil || !changed || passed.Status != validation.StatusPassed || passed.CompletedAt == nil {
		t.Fatalf("complete validation job = %#v, %t, %v", passed, changed, err)
	}
}
