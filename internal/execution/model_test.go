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
		PlanVersion: 1, StartedAt: now, UpdatedAt: now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate run: %v", err)
	}
	invalidLimits := valid
	invalidLimits.PlanningRoundLimit = -1
	if err := invalidLimits.Validate(); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("expected negative planning limit error %v, got %v", ErrInvalidRun, err)
	}
	invalidVersion := valid
	invalidVersion.PlanVersion = 0
	if err := invalidVersion.Validate(); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("expected invalid plan version error %v, got %v", ErrInvalidRun, err)
	}
	waiting := valid
	waiting.Status = RunStatusWaitingForUser
	waiting.WaitKind = RunWaitKindPhaseCheckpoint
	if err := waiting.Validate(); err != nil {
		t.Fatalf("validate waiting checkpoint: %v", err)
	}
	waiting.Paused = true
	waiting.WaitKind = RunWaitKindPaused
	waiting.PausedFromWaitKind = RunWaitKindPhaseCheckpoint
	if err := waiting.Validate(); err != nil {
		t.Fatalf("validate paused checkpoint: %v", err)
	}
	waiting.PausedFromWaitKind = RunWaitKindPaused
	if err := waiting.Validate(); !errors.Is(err, ErrInvalidRun) {
		t.Fatalf("expected invalid paused return kind, got %v", err)
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

func TestPlanRevisionValidate(t *testing.T) {
	now := time.Date(2026, time.September, 12, 15, 0, 0, 0, time.UTC)
	valid := PlanRevision{
		RunID: "run_test", Version: 2, InterventionID: "int_scope_change",
		PreviousPlanEventID: "sev_plan_v1",
		EffectiveGoal:       "Original accepted goal\n\nApproved scope amendment for plan version 2:\nAdd export support.",
		BaselineCommitID:    "0123456789abcdef0123456789abcdef01234567",
		CreatedAt:           now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate plan revision: %v", err)
	}
	invalidVersion := valid
	invalidVersion.Version = 1
	if err := invalidVersion.Validate(); !errors.Is(err, ErrInvalidPlanningMessage) {
		t.Fatalf("expected revised-version error, got %v", err)
	}
	invalidCommit := valid
	invalidCommit.BaselineCommitID = "not-a-commit"
	if err := invalidCommit.Validate(); !errors.Is(err, ErrInvalidPlanningMessage) {
		t.Fatalf("expected baseline commit error, got %v", err)
	}
}

func TestInterventionValidate(t *testing.T) {
	now := time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC)
	valid := Intervention{
		ID: "int_test", RunID: "run_test", SessionID: "ses_lead",
		Target: worker.RoleLead, Message: "Please explain this choice.",
		Status: InterventionStatusQueued, RequestedAt: now, UpdatedAt: now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate intervention: %v", err)
	}
	invalidTarget := valid
	invalidTarget.Target = worker.RoleCoder
	if err := invalidTarget.Validate(); !errors.Is(err, ErrInvalidIntervention) {
		t.Fatalf("expected invalid target error, got %v", err)
	}
	answeredWithoutTime := valid
	answeredWithoutTime.Status = InterventionStatusAnswered
	if err := answeredWithoutTime.Validate(); !errors.Is(err, ErrInvalidIntervention) {
		t.Fatalf("expected missing answer time error, got %v", err)
	}
	answeredAt := now.Add(time.Second)
	resolvedAt := answeredAt.Add(time.Second)
	answered := valid
	answered.Status = InterventionStatusAnswered
	answered.AttemptID = "ses_lead:intervention:test"
	answered.Effect = worker.InterventionEffectGuidanceApplied
	answered.AnsweredAt = &answeredAt
	answered.ResolvedAt = &resolvedAt
	answered.ResolutionActionID = "resume_test"
	answered.UpdatedAt = resolvedAt
	if err := answered.Validate(); err != nil {
		t.Fatalf("validate resolved intervention: %v", err)
	}
	resolvedBeforeAnswer := answered
	tooEarly := now
	resolvedBeforeAnswer.ResolvedAt = &tooEarly
	if err := resolvedBeforeAnswer.Validate(); !errors.Is(err, ErrInvalidIntervention) {
		t.Fatalf("expected invalid resolution time, got %v", err)
	}
	resolvedWithoutAction := answered
	resolvedWithoutAction.ResolutionActionID = ""
	if err := resolvedWithoutAction.Validate(); !errors.Is(err, ErrInvalidIntervention) {
		t.Fatalf("expected missing resolution action error, got %v", err)
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
	ended := now.Add(time.Second)
	failed := Session{
		ID: "ses_failed", RunID: "run_test", AgentID: "agt_test",
		Role: worker.RoleCoder, Status: SessionStatusFailed,
		Outcome: worker.OutcomeFailed, Summary: "provider failed",
		StartedAt: now, UpdatedAt: ended, EndedAt: &ended,
	}
	if err := failed.Validate(); err != nil {
		t.Fatalf("validate provider-reported failure: %v", err)
	}
	failed.Outcome = worker.OutcomeCompleted
	failed.Disposition = worker.DispositionSucceeded
	if err := failed.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("expected invalid failed result, got %v", err)
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
	valid.WorkerAttemptID = "att_test"
	valid.WorkerEventSequence = 1
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate worker-sourced event: %v", err)
	}
	valid.WorkerEventSequence = 0
	if err := valid.Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected paired worker source error %v, got %v", ErrInvalidEvent, err)
	}
	valid.WorkerAttemptID = ""
	valid.Sequence = 0
	if err := valid.Validate(); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected error %v, got %v", ErrInvalidEvent, err)
	}
}

func TestWorkerAttemptCheckpointValidate(t *testing.T) {
	now := time.Date(2026, time.September, 8, 22, 0, 0, 0, time.UTC)
	valid := WorkerAttemptCheckpoint{
		SessionID: "ses_test", AttemptID: "att_test", CreatedAt: now, UpdatedAt: now,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate worker attempt checkpoint: %v", err)
	}
	valid.LastEventSequence = -1
	if err := valid.Validate(); !errors.Is(err, ErrInvalidWorkerAttempt) {
		t.Fatalf("expected error %v, got %v", ErrInvalidWorkerAttempt, err)
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

func TestRunStatusTransitions(t *testing.T) {
	allowed := [][2]RunStatus{
		{RunStatusRunning, RunStatusWaitingForUser},
		{RunStatusWaitingForUser, RunStatusRunning},
		{RunStatusRunning, RunStatusSucceeded},
		{RunStatusRunning, RunStatusStopped},
		{RunStatusRunning, RunStatusFailed},
		{RunStatusWaitingForUser, RunStatusStopped},
		{RunStatusWaitingForUser, RunStatusFailed},
	}
	for _, transition := range allowed {
		if !transition[0].CanTransitionTo(transition[1]) {
			t.Errorf("expected %q to transition to %q", transition[0], transition[1])
		}
	}
	if RunStatusSucceeded.CanTransitionTo(RunStatusRunning) {
		t.Error("expected terminal run status to reject transitions")
	}
	if RunStatusRunning.CanTransitionTo(RunStatusRunning) {
		t.Error("expected repeated run status to be rejected")
	}
}

func TestSessionStatusTransitions(t *testing.T) {
	allowed := [][2]SessionStatus{
		{SessionStatusStarting, SessionStatusRunning},
		{SessionStatusRunning, SessionStatusWaitingForUser},
		{SessionStatusWaitingForUser, SessionStatusRunning},
		{SessionStatusRunning, SessionStatusPauseRequested},
		{SessionStatusPauseRequested, SessionStatusPaused},
		{SessionStatusPauseRequested, SessionStatusWaitingForUser},
		{SessionStatusPauseRequested, SessionStatusRunning},
		{SessionStatusPaused, SessionStatusRunning},
		{SessionStatusRunning, SessionStatusCompleted},
		{SessionStatusPaused, SessionStatusStopped},
	}
	for _, transition := range allowed {
		if !transition[0].CanTransitionTo(transition[1]) {
			t.Errorf("expected %q to transition to %q", transition[0], transition[1])
		}
	}
	if SessionStatusCompleted.CanTransitionTo(SessionStatusRunning) {
		t.Error("expected terminal session status to reject transitions")
	}
	if SessionStatusRunning.CanTransitionTo(SessionStatusRunning) {
		t.Error("expected repeated session status to be rejected")
	}
	if SessionStatusWaitingForUser.IsTerminal() {
		t.Error("expected waiting session to preserve the provider conversation")
	}
}
