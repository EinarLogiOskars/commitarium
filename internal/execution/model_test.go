package execution

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestRunValidate(t *testing.T) {
	now := time.Date(2026, time.September, 8, 22, 0, 0, 0, time.UTC)
	valid := Run{
		ID: "run_test", FeatureID: "fea_test", Status: RunStatusRunning,
		StartedAt: now, UpdatedAt: now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate run: %v", err)
	}
	ended := now.Add(time.Minute)
	valid.Status = RunStatusSucceeded
	valid.UpdatedAt = ended
	valid.EndedAt = &ended
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate completed run: %v", err)
	}
	valid.EndedAt = nil
	if err := valid.Validate(); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("expected error %v, got %v", ErrInvalidRun, err)
	}
}

func TestSessionValidate(t *testing.T) {
	now := time.Date(2026, time.September, 8, 22, 0, 0, 0, time.UTC)
	valid := Session{
		ID: "ses_test", RunID: "run_test", AgentID: "agt_test",
		Role: worker.RoleCoder, Status: SessionStatusRunning,
		StartedAt: now, UpdatedAt: now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate session: %v", err)
	}
	valid.Role = "unknown"
	if err := valid.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expected error %v, got %v", ErrInvalidSession, err)
	}
}

func TestEventValidate(t *testing.T) {
	valid := Event{
		ID: "sev_test", SessionID: "ses_test", Sequence: 1,
		Type: worker.EventMessage, Text: "observable message",
		OccurredAt: time.Date(2026, time.September, 8, 22, 0, 0, 0, time.UTC),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate event: %v", err)
	}
	valid.Sequence = 0
	if err := valid.Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected error %v, got %v", ErrInvalidEvent, err)
	}
}

func TestCommandValidate(t *testing.T) {
	now := time.Date(2026, time.September, 8, 22, 0, 0, 0, time.UTC)
	pending := Command{
		ID: "cmd_test", SessionID: "ses_test", Type: worker.CommandPause,
		Status: CommandStatusPending, RequestedAt: now,
	}
	if err := pending.Validate(); err != nil {
		t.Fatalf("validate pending command: %v", err)
	}
	appliedAt := now.Add(time.Second)
	pending.Status = CommandStatusApplied
	pending.AppliedAt = &appliedAt
	if err := pending.Validate(); err != nil {
		t.Fatalf("validate applied command: %v", err)
	}
	pending.Status = CommandStatusRejected
	if err := pending.Validate(); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("expected error %v, got %v", ErrInvalidCommand, err)
	}
}
