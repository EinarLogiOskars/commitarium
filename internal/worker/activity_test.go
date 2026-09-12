package worker

import (
	"errors"
	"testing"
)

func TestActivityValidate(t *testing.T) {
	exitCode := 0
	duration := int64(25)
	additions := 4
	deletions := 1
	valid := []Activity{
		{Kind: ActivityKindNarration},
		{
			Kind: ActivityKindCommand, Command: "go test ./...",
			ExitCode: &exitCode, DurationMS: &duration,
		},
		{
			Kind: ActivityKindFileChange, Operation: FileOperationModified,
			Path: "internal/worker/activity.go", Additions: &additions, Deletions: &deletions,
		},
		{
			Kind: ActivityKindFileChange, Operation: FileOperationRenamed,
			Path: "docs/new.md", OldPath: "docs/old.md",
		},
	}
	for _, activity := range valid {
		if err := activity.Validate(); err != nil {
			t.Errorf("validate %+v: %v", activity, err)
		}
	}

	invalid := []Activity{
		{Kind: ActivityKindNarration, Command: "unexpected"},
		{Kind: ActivityKindCommand},
		{Kind: ActivityKindCommand, Command: "test", Path: "unexpected"},
		{Kind: ActivityKindFileChange, Operation: "unknown", Path: "file.go"},
		{Kind: ActivityKindFileChange, Operation: FileOperationModified, Path: "/private/file.go"},
		{Kind: ActivityKindFileChange, Operation: FileOperationModified, Path: "../file.go"},
		{Kind: ActivityKindFileChange, Operation: FileOperationRenamed, Path: "new.go"},
	}
	for _, activity := range invalid {
		if err := activity.Validate(); !errors.Is(err, ErrInvalidActivity) {
			t.Errorf("activity %+v error = %v, want ErrInvalidActivity", activity, err)
		}
	}
}
