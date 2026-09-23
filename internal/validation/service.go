package validation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Store interface {
	PutConfig(context.Context, Config) (Config, error)
	GetConfig(context.Context, string) (Config, error)
	CreateJob(context.Context, Job) (Job, bool, error)
	GetJob(context.Context, string) (Job, error)
	ListJobsByRun(context.Context, string) ([]Job, error)
	TransitionJob(context.Context, string, Status, Status, []CommandResult, string, time.Time) (Job, bool, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (service *Service) Configure(ctx context.Context, projectID string, commands []string) (Config, error) {
	normalized, err := NormalizeCommands(commands)
	if err != nil || strings.TrimSpace(projectID) == "" {
		return Config{}, ErrInvalid
	}
	return service.store.PutConfig(ctx, Config{ProjectID: projectID, Commands: normalized, UpdatedAt: service.now()})
}

func (service *Service) GetConfig(ctx context.Context, projectID string) (Config, error) {
	return service.store.GetConfig(ctx, projectID)
}

func (service *Service) EnsureJob(ctx context.Context, projectID, featureID, runID, workspaceID, commitID string) (Job, bool, error) {
	config, err := service.store.GetConfig(ctx, projectID)
	if err != nil {
		return Job{}, false, err
	}
	digest := validationDigest(runID, commitID, config.Commands)
	now := service.now()
	job := Job{
		ID: "val_" + hex.EncodeToString(digest[:16]), ProjectID: projectID, FeatureID: featureID,
		RunID: runID, WorkspaceID: workspaceID, CommitID: commitID,
		Commands: append([]string(nil), config.Commands...), Status: StatusPending,
		Results: []CommandResult{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := job.Validate(); err != nil {
		return Job{}, false, err
	}
	return service.store.CreateJob(ctx, job)
}

// Retry creates a new auditable attempt instead of overwriting a terminal
// result. It uses the project's current command configuration, so changing the
// validation contract also invalidates an older passing result.
func (service *Service) Retry(ctx context.Context, id string) (Job, bool, error) {
	prior, err := service.GetJob(ctx, id)
	if err != nil {
		return Job{}, false, err
	}
	if prior.Status != StatusFailed && prior.Status != StatusPassed {
		return Job{}, false, ErrConflict
	}
	baseJob, created, err := service.EnsureJob(
		ctx, prior.ProjectID, prior.FeatureID, prior.RunID, prior.WorkspaceID, prior.CommitID,
	)
	if err != nil {
		return Job{}, false, err
	}
	if created || baseJob.Status == StatusPending || baseJob.Status == StatusRunning {
		return baseJob, created, nil
	}
	config, err := service.store.GetConfig(ctx, prior.ProjectID)
	if err != nil {
		return Job{}, false, err
	}
	jobs, err := service.store.ListJobsByRun(ctx, prior.RunID)
	if err != nil {
		return Job{}, false, err
	}
	base := baseJob.ID
	attempt := 0
	for _, job := range jobs {
		if job.CommitID == prior.CommitID && slices.Equal(job.Commands, config.Commands) {
			if job.Status == StatusPending || job.Status == StatusRunning || job.Status == StatusPassed {
				return job, false, nil
			}
			attempt++
		}
	}
	now := service.now()
	job := Job{
		ID: fmt.Sprintf("%s_r%d", base, attempt), ProjectID: prior.ProjectID,
		FeatureID: prior.FeatureID, RunID: prior.RunID, WorkspaceID: prior.WorkspaceID,
		CommitID: prior.CommitID, Commands: append([]string(nil), config.Commands...),
		Status: StatusPending, Results: []CommandResult{}, CreatedAt: now, UpdatedAt: now,
	}
	if err := job.Validate(); err != nil {
		return Job{}, false, err
	}
	return service.store.CreateJob(ctx, job)
}

func validationDigest(runID, commitID string, commands []string) [32]byte {
	value := runID + "\x00" + commitID
	for _, command := range commands {
		value += "\x00" + command
	}
	return sha256.Sum256([]byte(value))
}

func (service *Service) GetJob(ctx context.Context, id string) (Job, error) {
	return service.store.GetJob(ctx, id)
}

func (service *Service) JobsForRun(ctx context.Context, runID string) ([]Job, error) {
	return service.store.ListJobsByRun(ctx, runID)
}

func (service *Service) Claim(ctx context.Context, id string) (Job, bool, error) {
	job, err := service.GetJob(ctx, id)
	if err != nil {
		return Job{}, false, err
	}
	if job.Status == StatusRunning {
		return job, false, nil
	}
	// Returning a terminal job lets the native runner replay /complete after an
	// uncertain response so Forgejo publication and automatic merge can finish
	// without executing repository code a second time.
	if job.Status == StatusPassed || job.Status == StatusFailed {
		return job, false, nil
	}
	return service.store.TransitionJob(ctx, id, StatusPending, StatusRunning, []CommandResult{}, "", service.now())
}

func (service *Service) Complete(ctx context.Context, id string, results []CommandResult, detail string) (Job, bool, error) {
	job, err := service.GetJob(ctx, id)
	if err != nil {
		return Job{}, false, err
	}
	if job.Status == StatusPassed || job.Status == StatusFailed {
		return job, false, nil
	}
	if job.Status != StatusRunning || len(results) > len(job.Commands) {
		return Job{}, false, ErrConflict
	}
	status := StatusPassed
	if len(results) != len(job.Commands) {
		status = StatusFailed
	}
	for index, result := range results {
		if result.Command != job.Commands[index] || result.DurationMS < 0 {
			return Job{}, false, ErrInvalid
		}
		if result.ExitCode != 0 {
			status = StatusFailed
		}
		if len(result.Output) > 256*1024 {
			results[index].Output = result.Output[:256*1024]
		}
	}
	detail = strings.TrimSpace(detail)
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	return service.store.TransitionJob(ctx, id, StatusRunning, status, results, detail, service.now())
}

func (service *Service) RequirePassed(ctx context.Context, runID, commitID string) error {
	jobs, err := service.store.ListJobsByRun(ctx, runID)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return ErrRequired
	}
	config, err := service.store.GetConfig(ctx, jobs[0].ProjectID)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.CommitID == commitID && job.Status == StatusPassed && slices.Equal(job.Commands, config.Commands) {
			return nil
		}
	}
	return ErrRequired
}
