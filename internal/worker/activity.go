package worker

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

var ErrInvalidActivity = errors.New("invalid worker activity")

type ActivityKind string

const (
	ActivityKindCommand    ActivityKind = "command"
	ActivityKindFileChange ActivityKind = "file_change"
	ActivityKindNarration  ActivityKind = "narration"
)

type FileOperation string

const (
	FileOperationCreated  FileOperation = "created"
	FileOperationModified FileOperation = "modified"
	FileOperationDeleted  FileOperation = "deleted"
	FileOperationRenamed  FileOperation = "renamed"
)

// Activity is the provider-neutral, deliberately small description of an
// observable coding step. Output is excluded: commands retain only execution
// facts, while file changes retain only workspace-relative paths and counts.
type Activity struct {
	Kind       ActivityKind
	Command    string
	ExitCode   *int
	DurationMS *int64
	Operation  FileOperation
	Path       string
	OldPath    string
	Additions  *int
	Deletions  *int
}

func (activity Activity) Validate() error {
	switch activity.Kind {
	case ActivityKindNarration:
		if activity.Command != "" || activity.ExitCode != nil || activity.DurationMS != nil ||
			activity.Operation != "" || activity.Path != "" || activity.OldPath != "" ||
			activity.Additions != nil || activity.Deletions != nil {
			return fmt.Errorf("%w: narration cannot include command or file-change fields", ErrInvalidActivity)
		}
	case ActivityKindCommand:
		if strings.TrimSpace(activity.Command) == "" {
			return fmt.Errorf("%w: command is required", ErrInvalidActivity)
		}
		if activity.DurationMS != nil && *activity.DurationMS < 0 {
			return fmt.Errorf("%w: command duration cannot be negative", ErrInvalidActivity)
		}
		if activity.Operation != "" || activity.Path != "" || activity.OldPath != "" ||
			activity.Additions != nil || activity.Deletions != nil {
			return fmt.Errorf("%w: command cannot include file-change fields", ErrInvalidActivity)
		}
	case ActivityKindFileChange:
		if !activity.Operation.IsValid() {
			return fmt.Errorf("%w: file operation %q is not recognized", ErrInvalidActivity, activity.Operation)
		}
		if strings.TrimSpace(activity.Path) == "" {
			return fmt.Errorf("%w: file path is required", ErrInvalidActivity)
		}
		if !isWorkspaceRelativePath(activity.Path) {
			return fmt.Errorf("%w: file path must be workspace-relative", ErrInvalidActivity)
		}
		if activity.Operation == FileOperationRenamed {
			if strings.TrimSpace(activity.OldPath) == "" {
				return fmt.Errorf("%w: renamed file requires its old path", ErrInvalidActivity)
			}
			if !isWorkspaceRelativePath(activity.OldPath) {
				return fmt.Errorf("%w: old file path must be workspace-relative", ErrInvalidActivity)
			}
		} else if activity.OldPath != "" {
			return fmt.Errorf("%w: old path is valid only for a rename", ErrInvalidActivity)
		}
		if activity.Additions != nil && *activity.Additions < 0 {
			return fmt.Errorf("%w: additions cannot be negative", ErrInvalidActivity)
		}
		if activity.Deletions != nil && *activity.Deletions < 0 {
			return fmt.Errorf("%w: deletions cannot be negative", ErrInvalidActivity)
		}
		if activity.Command != "" || activity.ExitCode != nil || activity.DurationMS != nil {
			return fmt.Errorf("%w: file change cannot include command fields", ErrInvalidActivity)
		}
	default:
		return fmt.Errorf("%w: kind %q is not recognized", ErrInvalidActivity, activity.Kind)
	}
	return nil
}

func isWorkspaceRelativePath(value string) bool {
	cleaned := path.Clean(value)
	return strings.TrimSpace(value) == value && cleaned == value && cleaned != "." && cleaned != ".." &&
		!path.IsAbs(cleaned) && !strings.HasPrefix(cleaned, "../")
}

func (operation FileOperation) IsValid() bool {
	switch operation {
	case FileOperationCreated, FileOperationModified, FileOperationDeleted, FileOperationRenamed:
		return true
	default:
		return false
	}
}
