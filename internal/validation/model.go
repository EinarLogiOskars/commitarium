package validation

import (
	"errors"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
)

var (
	ErrInvalid  = errors.New("invalid validation record")
	ErrNotFound = errors.New("validation record not found")
	ErrConflict = errors.New("validation state conflict")
	ErrRequired = errors.New("successful isolated validation is required for the approved revision")
)

type Config struct {
	ProjectID string    `json:"project_id"`
	Commands  []string  `json:"commands"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CommandResult struct {
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
	DurationMS int64  `json:"duration_ms"`
}

type Job struct {
	ID          string          `json:"id"`
	ProjectID   string          `json:"project_id"`
	FeatureID   string          `json:"feature_id"`
	RunID       string          `json:"run_id"`
	WorkspaceID string          `json:"workspace_id"`
	CommitID    string          `json:"commit_id"`
	Commands    []string        `json:"commands"`
	Status      Status          `json:"status"`
	Results     []CommandResult `json:"results"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

func NormalizeCommands(commands []string) ([]string, error) {
	if len(commands) == 0 || len(commands) > 16 {
		return nil, ErrInvalid
	}
	normalized := make([]string, len(commands))
	for index, command := range commands {
		command = strings.TrimSpace(command)
		if command == "" || len(command) > 4096 || strings.ContainsRune(command, '\x00') {
			return nil, ErrInvalid
		}
		normalized[index] = command
	}
	return normalized, nil
}

func (config Config) Validate() error {
	if strings.TrimSpace(config.ProjectID) == "" || config.UpdatedAt.IsZero() {
		return ErrInvalid
	}
	_, err := NormalizeCommands(config.Commands)
	return err
}

func (job Job) Validate() error {
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.ProjectID) == "" ||
		strings.TrimSpace(job.FeatureID) == "" || strings.TrimSpace(job.RunID) == "" ||
		strings.TrimSpace(job.WorkspaceID) == "" || !workspace.ValidCommitID(job.CommitID) ||
		job.Results == nil || job.CreatedAt.IsZero() || job.UpdatedAt.Before(job.CreatedAt) {
		return ErrInvalid
	}
	if _, err := NormalizeCommands(job.Commands); err != nil {
		return err
	}
	switch job.Status {
	case StatusPending, StatusRunning, StatusPassed, StatusFailed:
	default:
		return ErrInvalid
	}
	if (job.Status == StatusPassed || job.Status == StatusFailed) != (job.CompletedAt != nil) {
		return ErrInvalid
	}
	if job.Status == StatusPassed {
		if len(job.Results) != len(job.Commands) {
			return ErrInvalid
		}
		for _, result := range job.Results {
			if result.ExitCode != 0 {
				return ErrInvalid
			}
		}
	}
	if len(job.Error) > 1000 {
		return ErrInvalid
	}
	if len(job.Results) > len(job.Commands) {
		return ErrInvalid
	}
	for index, result := range job.Results {
		if result.Command != job.Commands[index] || result.DurationMS < 0 ||
			len(result.Output) > 256*1024 {
			return ErrInvalid
		}
	}
	return nil
}
