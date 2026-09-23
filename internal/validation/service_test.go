package validation

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryStore struct {
	config Config
	jobs   map[string]Job
}

func (store *memoryStore) PutConfig(_ context.Context, config Config) (Config, error) {
	store.config = config
	return config, nil
}

func (store *memoryStore) GetConfig(_ context.Context, projectID string) (Config, error) {
	if store.config.ProjectID != projectID {
		return Config{}, ErrNotFound
	}
	return store.config, nil
}

func (store *memoryStore) CreateJob(_ context.Context, job Job) (Job, bool, error) {
	if stored, ok := store.jobs[job.ID]; ok {
		return stored, false, nil
	}
	store.jobs[job.ID] = job
	return job, true, nil
}

func (store *memoryStore) GetJob(_ context.Context, id string) (Job, error) {
	job, ok := store.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return job, nil
}

func (store *memoryStore) ListJobsByRun(_ context.Context, runID string) ([]Job, error) {
	jobs := make([]Job, 0)
	for _, job := range store.jobs {
		if job.RunID == runID {
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}

func (store *memoryStore) TransitionJob(_ context.Context, id string, from, to Status, results []CommandResult, detail string, occurredAt time.Time) (Job, bool, error) {
	job, ok := store.jobs[id]
	if !ok {
		return Job{}, false, ErrNotFound
	}
	if job.Status == to {
		return job, false, nil
	}
	if job.Status != from {
		return Job{}, false, ErrConflict
	}
	job.Status, job.Results, job.Error, job.UpdatedAt = to, results, detail, occurredAt
	if to == StatusPassed || to == StatusFailed {
		job.CompletedAt = &occurredAt
	}
	store.jobs[id] = job
	return job, true, nil
}

func TestPassingJobMatchesCurrentConfigurationAndRetryPreservesHistory(t *testing.T) {
	store := &memoryStore{jobs: map[string]Job{}}
	service := NewService(store)
	clock := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	ctx := t.Context()

	if _, err := service.Configure(ctx, "project", []string{"go test ./..."}); err != nil {
		t.Fatal(err)
	}
	job, created, err := service.EnsureJob(ctx, "project", "feature", "run", "workspace", "0123456789abcdef0123456789abcdef01234567")
	if err != nil || !created {
		t.Fatalf("EnsureJob = %#v, %v, %v", job, created, err)
	}
	if _, _, err := service.Claim(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	passed, _, err := service.Complete(ctx, job.ID, []CommandResult{{Command: "go test ./...", ExitCode: 0, Output: "ok", DurationMS: 12}}, "")
	if err != nil || passed.Status != StatusPassed {
		t.Fatalf("Complete = %#v, %v", passed, err)
	}
	if err := service.RequirePassed(ctx, "run", job.CommitID); err != nil {
		t.Fatalf("RequirePassed = %v", err)
	}

	if _, err := service.Configure(ctx, "project", []string{"go test ./...", "go vet ./..."}); err != nil {
		t.Fatal(err)
	}
	if err := service.RequirePassed(ctx, "run", job.CommitID); !errors.Is(err, ErrRequired) {
		t.Fatalf("RequirePassed after config change = %v", err)
	}
	current, created, err := service.Retry(ctx, job.ID)
	if err != nil || !created || current.ID == job.ID || len(current.Commands) != 2 {
		t.Fatalf("Retry after config change = %#v, %v, %v", current, created, err)
	}
	if store.jobs[job.ID].Status != StatusPassed {
		t.Fatal("retry overwrote the original terminal job")
	}
}

func TestRetryCreatesSeparateAttemptForSameConfiguration(t *testing.T) {
	store := &memoryStore{jobs: map[string]Job{}}
	service := NewService(store)
	ctx := t.Context()
	if _, err := service.Configure(ctx, "project", []string{"go test ./..."}); err != nil {
		t.Fatal(err)
	}
	job, _, err := service.EnsureJob(ctx, "project", "feature", "run", "workspace", "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Claim(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Complete(ctx, job.ID, []CommandResult{{Command: "go test ./...", ExitCode: 1, DurationMS: 1}}, ""); err != nil {
		t.Fatal(err)
	}
	retry, created, err := service.Retry(ctx, job.ID)
	if err != nil || !created || retry.ID == job.ID || retry.Status != StatusPending {
		t.Fatalf("Retry = %#v, %v, %v", retry, created, err)
	}
}
