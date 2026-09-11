package orchestration

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

func TestControllerPersistsAndRoutesSessionCommands(t *testing.T) {
	executionService, registry, session := activeControlSession(t)
	controller := NewController(executionService, registry)
	runner := NewRunner(nil, executionService, registry)

	pause, err := controller.SendCommand(t.Context(), "ses_control", worker.Command{
		ID: "cmd_pause", Type: worker.CommandPause,
	})
	if err != nil {
		t.Fatalf("pause session: %v", err)
	}
	if pause.Status != execution.CommandStatusApplied || pause.AppliedAt == nil {
		t.Errorf("unexpected pause command %+v", pause)
	}
	pauseEvent := nextWorkerEvent(t, session)
	if pauseEvent.Type != worker.EventPauseAcknowledged {
		t.Fatalf("expected pause acknowledgement, got %+v", pauseEvent)
	}
	if err := runner.applySessionEvent(t.Context(), "ses_control", pauseEvent); err != nil {
		t.Fatalf("apply pause event: %v", err)
	}
	storedSession, err := executionService.GetSession(t.Context(), "ses_control")
	if err != nil {
		t.Fatalf("get paused session: %v", err)
	}
	if storedSession.Status != execution.SessionStatusPaused {
		t.Errorf("expected paused session, got %+v", storedSession)
	}

	retried, err := controller.SendCommand(t.Context(), "ses_control", worker.Command{
		ID: "cmd_pause", Type: worker.CommandPause,
	})
	if err != nil {
		t.Fatalf("retry pause command: %v", err)
	}
	if retried.ID != pause.ID ||
		retried.Status != pause.Status ||
		retried.AppliedAt == nil ||
		pause.AppliedAt == nil ||
		!retried.AppliedAt.Equal(*pause.AppliedAt) {
		t.Errorf("expected original pause command %+v, got %+v", pause, retried)
	}

	continued, err := controller.SendCommand(t.Context(), "ses_control", worker.Command{
		ID: "cmd_continue", Type: worker.CommandContinue,
	})
	if err != nil {
		t.Fatalf("continue session: %v", err)
	}
	if continued.Status != execution.CommandStatusApplied {
		t.Errorf("unexpected continue command %+v", continued)
	}
	continueEvent := nextWorkerEvent(t, session)
	if continueEvent.Type != worker.EventContinued {
		t.Fatalf("expected continued event, got %+v", continueEvent)
	}
	if err := runner.applySessionEvent(t.Context(), "ses_control", continueEvent); err != nil {
		t.Fatalf("apply continue event: %v", err)
	}

	message, err := controller.SendCommand(t.Context(), "ses_control", worker.Command{
		ID: "cmd_message", Type: worker.CommandMessage, Message: "Use the simpler option.",
	})
	if err != nil {
		t.Fatalf("message session: %v", err)
	}
	if message.Status != execution.CommandStatusApplied {
		t.Errorf("unexpected message command %+v", message)
	}

	stopped, err := controller.SendCommand(t.Context(), "ses_control", worker.Command{
		ID: "cmd_stop", Type: worker.CommandStop,
	})
	if err != nil {
		t.Fatalf("stop session: %v", err)
	}
	if stopped.Status != execution.CommandStatusApplied {
		t.Errorf("unexpected stop command %+v", stopped)
	}
	result, err := session.Wait(t.Context())
	if err != nil {
		t.Fatalf("wait for stopped session: %v", err)
	}
	if result.Outcome != worker.OutcomeStopped {
		t.Errorf("expected stopped worker, got %+v", result)
	}
}

func TestControllerRejectsNewPauseWhilePaused(t *testing.T) {
	executionService, registry, session := activeControlSession(t)
	controller := NewController(executionService, registry)
	runner := NewRunner(nil, executionService, registry)

	if _, err := controller.SendCommand(t.Context(), "ses_control", worker.Command{
		ID: "cmd_pause", Type: worker.CommandPause,
	}); err != nil {
		t.Fatalf("pause session: %v", err)
	}
	if err := runner.applySessionEvent(
		t.Context(),
		"ses_control",
		nextWorkerEvent(t, session),
	); err != nil {
		t.Fatalf("apply pause event: %v", err)
	}

	rejected, err := controller.SendCommand(t.Context(), "ses_control", worker.Command{
		ID: "cmd_second_pause", Type: worker.CommandPause,
	})
	if !errors.Is(err, ErrCommandNotAllowed) {
		t.Fatalf("expected error %v, got %v", ErrCommandNotAllowed, err)
	}
	if rejected.Status != execution.CommandStatusRejected ||
		rejected.AppliedAt == nil ||
		rejected.Error == "" {
		t.Errorf("unexpected rejected command %+v", rejected)
	}
}

func TestControllerRejectsCommandForInactiveSession(t *testing.T) {
	executionService, _, _ := activeControlSession(t)
	controller := NewController(executionService, NewActiveSessions())

	rejected, err := controller.SendCommand(t.Context(), "ses_control", worker.Command{
		ID: "cmd_message", Type: worker.CommandMessage, Message: "Hello?",
	})
	if !errors.Is(err, ErrSessionNotActive) {
		t.Fatalf("expected error %v, got %v", ErrSessionNotActive, err)
	}
	if rejected.Status != execution.CommandStatusRejected || rejected.Error == "" {
		t.Errorf("unexpected rejected command %+v", rejected)
	}
}

func activeControlSession(
	t *testing.T,
) (*execution.Service, *ActiveSessions, worker.Session) {
	t.Helper()
	_, _, executionService := orchestrationDatabase(t)
	if _, created, err := executionService.CreateRun(
		t.Context(),
		"run_control",
		"fea_test",
		2,
		3,
		project.DefaultAgentProviders(),
	); err != nil {
		t.Fatalf("create control run: %v", err)
	} else if !created {
		t.Fatal("expected control run to be created")
	}
	if _, created, err := executionService.CreateSession(
		t.Context(),
		"ses_control",
		"run_control",
		"agt_codex",
		worker.RoleCoder,
	); err != nil {
		t.Fatalf("create control session: %v", err)
	} else if !created {
		t.Fatal("expected control session to be created")
	}
	if _, err := executionService.TransitionSession(
		t.Context(),
		"ses_control",
		execution.SessionStatusStarting,
		execution.SessionStatusRunning,
		"",
	); err != nil {
		t.Fatalf("mark control session running: %v", err)
	}

	adapter := worker.NewScriptedAdapter("fake-codex", map[worker.Role]worker.Script{
		worker.RoleCoder: {
			Events:      []worker.Event{{Type: worker.EventActivity, Text: "working"}},
			Disposition: worker.DispositionSucceeded,
			Summary:     "done",
		},
	})
	session, err := adapter.Start(t.Context(), worker.SessionRequest{
		SessionID: "ses_control", AttemptID: "ses_control", FeatureID: "fea_test", Role: worker.RoleCoder,
		Instructions: "Implement the accepted plan.",
	})
	if err != nil {
		t.Fatalf("start control worker: %v", err)
	}
	registry := NewActiveSessions()
	remove, err := registry.Register("ses_control", session)
	if err != nil {
		t.Fatalf("register control worker: %v", err)
	}
	t.Cleanup(remove)
	return executionService, registry, session
}

func nextWorkerEvent(t *testing.T, session worker.Session) worker.Event {
	t.Helper()
	select {
	case event := <-session.Events():
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker event")
		return worker.Event{}
	}
}
