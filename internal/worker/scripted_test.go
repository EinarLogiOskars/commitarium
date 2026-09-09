package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScriptedAdapterRunsDeterministicSession(t *testing.T) {
	adapter := testScriptedAdapter()
	request := testSessionRequest()
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	if err := adapter.Advance(t.Context(), request.SessionID); err != nil {
		t.Fatalf("advance session: %v", err)
	}
	first := <-session.Events()
	if first.Text != "inspect the accepted plan" {
		t.Errorf("unexpected first event %+v", first)
	}

	if err := adapter.Advance(t.Context(), request.SessionID); err != nil {
		t.Fatalf("advance session: %v", err)
	}
	second := <-session.Events()
	if second.Text != "implementation complete" {
		t.Errorf("unexpected second event %+v", second)
	}

	result, err := session.Wait(t.Context())
	if err != nil {
		t.Fatalf("wait for session: %v", err)
	}
	if result.Outcome != OutcomeCompleted ||
		result.Disposition != DispositionSucceeded ||
		result.ProviderSessionID != "fake-codex:coder:0:ses_test" ||
		result.Summary != "completed scripted coding work" {
		t.Errorf("unexpected result %+v", result)
	}
	if _, ok := <-session.Events(); ok {
		t.Fatal("expected event stream to close after completion")
	}
}

func TestRepeatingScriptedAdapterServesIndependentLogicalSessions(t *testing.T) {
	script := Script{
		Events:      []Event{{Type: EventActivity, Text: "deterministic work"}},
		Disposition: DispositionSucceeded,
		Summary:     "completed repeated script",
	}
	adapter := NewRepeatingScriptedAdapter("codex", map[Role]Script{RoleCoder: script})
	request := testSessionRequest()
	request.SessionID = "ses_first"
	first, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start first repeated session: %v", err)
	}
	request.SessionID = "ses_second"
	second, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start second repeated session: %v", err)
	}
	if first.ProviderSessionID() == second.ProviderSessionID() {
		t.Fatalf("repeated sessions share provider identity %q", first.ProviderSessionID())
	}
	if err := adapter.Advance(t.Context(), "ses_first"); err != nil {
		t.Fatalf("advance first repeated session: %v", err)
	}
	if err := adapter.Advance(t.Context(), "ses_second"); err != nil {
		t.Fatalf("advance second repeated session: %v", err)
	}
	firstResult, err := first.Wait(t.Context())
	if err != nil || firstResult.Summary != script.Summary {
		t.Fatalf("first repeated result=%+v error=%v", firstResult, err)
	}
	secondResult, err := second.Wait(t.Context())
	if err != nil || secondResult.Summary != script.Summary {
		t.Fatalf("second repeated result=%+v error=%v", secondResult, err)
	}
}

func TestAutomaticScriptedAdapterAdvancesSession(t *testing.T) {
	adapter := NewAutomaticScriptedAdapter(testScriptedAdapter(), time.Millisecond)
	session, err := adapter.Start(t.Context(), testSessionRequest())
	if err != nil {
		t.Fatalf("start automatic session: %v", err)
	}

	events := make([]Event, 0, 2)
	for event := range session.Events() {
		events = append(events, event)
	}
	result, err := session.Wait(t.Context())
	if err != nil {
		t.Fatalf("wait for automatic session: %v", err)
	}
	if len(events) != 2 || result.Outcome != OutcomeCompleted {
		t.Fatalf("unexpected automatic result %+v with events %+v", result, events)
	}
}

func TestScriptedSessionCanPauseReceiveGuidanceAndContinue(t *testing.T) {
	adapter := testScriptedAdapter()
	request := testSessionRequest()
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	pause := Command{ID: "cmd_pause", Type: CommandPause}
	if err := session.Send(t.Context(), pause); err != nil {
		t.Fatalf("pause session: %v", err)
	}
	if event := <-session.Events(); event.Type != EventPauseAcknowledged {
		t.Errorf("expected pause acknowledgement, got %+v", event)
	}

	cancelledContext, cancel := context.WithCancel(t.Context())
	cancel()
	if err := adapter.Advance(cancelledContext, request.SessionID); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected paused advance to block until cancellation, got %v", err)
	}

	message := Command{ID: "cmd_message", Type: CommandMessage, Message: "Use the simpler approach."}
	if err := session.Send(t.Context(), message); err != nil {
		t.Fatalf("send guidance: %v", err)
	}
	if err := session.Send(t.Context(), message); err != nil {
		t.Fatalf("retry guidance: %v", err)
	}

	if err := session.Send(t.Context(), Command{ID: "cmd_continue", Type: CommandContinue}); err != nil {
		t.Fatalf("continue session: %v", err)
	}
	if event := <-session.Events(); event.Type != EventContinued {
		t.Errorf("expected continued event, got %+v", event)
	}
	if err := adapter.Advance(t.Context(), request.SessionID); err != nil {
		t.Fatalf("advance continued session: %v", err)
	}
	if event := <-session.Events(); event.Text != "inspect the accepted plan" {
		t.Errorf("unexpected event after continue %+v", event)
	}
	if err := session.Send(t.Context(), Command{ID: "cmd_stop", Type: CommandStop}); err != nil {
		t.Fatalf("stop session: %v", err)
	}
	if _, err := session.Wait(t.Context()); err != nil {
		t.Fatalf("wait for stopped session: %v", err)
	}
}

func TestScriptedSessionStopIsIdempotent(t *testing.T) {
	adapter := testScriptedAdapter()
	request := testSessionRequest()
	session, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	stop := Command{ID: "cmd_stop", Type: CommandStop}
	if err := session.Send(t.Context(), stop); err != nil {
		t.Fatalf("stop session: %v", err)
	}
	if err := session.Send(t.Context(), stop); err != nil {
		t.Fatalf("retry stop: %v", err)
	}

	result, err := session.Wait(t.Context())
	if err != nil {
		t.Fatalf("wait for stopped session: %v", err)
	}
	if result.Outcome != OutcomeStopped {
		t.Errorf("expected stopped outcome, got %+v", result)
	}
	if err := session.Send(
		t.Context(),
		Command{ID: "cmd_late", Type: CommandMessage, Message: "too late"},
	); !errors.Is(err, ErrSessionFinished) {
		t.Fatalf("expected error %v, got %v", ErrSessionFinished, err)
	}
}

func TestScriptedSessionRejectsCommandIDConflict(t *testing.T) {
	adapter := testScriptedAdapter()
	session, err := adapter.Start(t.Context(), testSessionRequest())
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	if err := session.Send(t.Context(), Command{ID: "cmd_same", Type: CommandPause}); err != nil {
		t.Fatalf("pause session: %v", err)
	}
	if err := session.Send(
		t.Context(),
		Command{ID: "cmd_same", Type: CommandContinue},
	); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("expected error %v, got %v", ErrCommandConflict, err)
	}
	if err := session.Send(t.Context(), Command{ID: "cmd_stop", Type: CommandStop}); err != nil {
		t.Fatalf("stop session: %v", err)
	}
}

func TestScriptedAdapterStartAndResumeAreIdempotent(t *testing.T) {
	adapter := testScriptedAdapter()
	request := testSessionRequest()
	first, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	second, err := adapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("retry start session: %v", err)
	}
	if first != second {
		t.Fatal("expected retried start to return existing session")
	}

	resumed, err := adapter.Resume(t.Context(), ResumeRequest{
		SessionRequest:    request,
		ProviderSessionID: first.ProviderSessionID(),
	})
	if err != nil {
		t.Fatalf("resume session: %v", err)
	}
	if resumed != first {
		t.Fatal("expected resume to return existing session")
	}
	if err := first.Send(t.Context(), Command{ID: "cmd_stop", Type: CommandStop}); err != nil {
		t.Fatalf("stop session: %v", err)
	}
}

func TestQueuedScriptedAdapterUsesScriptsInOrder(t *testing.T) {
	adapter := NewQueuedScriptedAdapter("fake-claude", map[Role][]Script{
		RoleReviewer: {
			{Events: []Event{{Type: EventMessage, Text: "changes requested"}}, Disposition: DispositionChangesRequested, Summary: "one issue"},
			{Events: []Event{{Type: EventMessage, Text: "approved"}}, Disposition: DispositionSucceeded, Summary: "approved"},
		},
	})

	for index, expected := range []Disposition{DispositionChangesRequested, DispositionSucceeded} {
		request := SessionRequest{
			SessionID:    "ses_review_" + string(rune('1'+index)),
			AttemptID:    "att_review_" + string(rune('1'+index)),
			FeatureID:    "fea_test",
			Role:         RoleReviewer,
			Instructions: "Review the implementation.",
		}
		session, err := adapter.Start(t.Context(), request)
		if err != nil {
			t.Fatalf("start review session %d: %v", index+1, err)
		}
		if err := adapter.Advance(t.Context(), request.SessionID); err != nil {
			t.Fatalf("advance review session %d: %v", index+1, err)
		}
		<-session.Events()
		result, err := session.Wait(t.Context())
		if err != nil {
			t.Fatalf("wait for review session %d: %v", index+1, err)
		}
		if result.Disposition != expected {
			t.Errorf("expected disposition %q, got %q", expected, result.Disposition)
		}
	}
}

func TestScriptedAdapterReconstructsInterruptedSessionBeforeContinuing(t *testing.T) {
	request := SessionRequest{
		SessionID: "run_test:implementation", AttemptID: "att_implementation", FeatureID: "fea_test",
		Role: RoleCoder, Instructions: "Implement the plan.",
	}
	firstAdapter := testScriptedAdapter()
	first, err := firstAdapter.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start original session: %v", err)
	}
	if err := firstAdapter.Advance(t.Context(), request.SessionID); err != nil {
		t.Fatalf("advance original session: %v", err)
	}
	completed := <-first.Events()

	replacement := testScriptedAdapter()
	resumed, err := replacement.Resume(t.Context(), ResumeRequest{
		SessionRequest: SessionRequest{
			SessionID: request.SessionID, AttemptID: request.AttemptID, FeatureID: request.FeatureID,
			Role: request.Role, Instructions: "Inspect and reconcile before continuing.",
		},
		ProviderSessionID: first.ProviderSessionID(),
		Recovery: RecoveryContext{
			CompletedEvents: []Event{completed},
			PreviousState:   "running",
			WorkflowPhase:   "implementing",
		},
	})
	if err != nil {
		t.Fatalf("resume replacement session: %v", err)
	}
	assessment := <-resumed.Events()
	if assessment.Type != EventRecoveryAssessment ||
		assessment.RecoveryAssessment == nil ||
		!assessment.RecoveryAssessment.Consistent {
		t.Fatalf("unexpected recovery assessment %+v", assessment)
	}
	if err := resumed.Send(t.Context(), Command{ID: "approve", Type: CommandContinue}); err != nil {
		t.Fatalf("continue recovered session: %v", err)
	}
	if event := <-resumed.Events(); event.Type != EventContinued {
		t.Fatalf("expected continued event, got %+v", event)
	}
	if err := replacement.Advance(t.Context(), request.SessionID); err != nil {
		t.Fatalf("advance recovered session: %v", err)
	}
	if event := <-resumed.Events(); event.Text != "implementation complete" {
		t.Fatalf("expected only unfinished script event, got %+v", event)
	}
	result, err := resumed.Wait(t.Context())
	if err != nil || result.Outcome != OutcomeCompleted {
		t.Fatalf("unexpected recovered result %+v, err=%v", result, err)
	}
}

func testScriptedAdapter() *ScriptedAdapter {
	return NewScriptedAdapter("fake-codex", map[Role]Script{
		RoleCoder: {
			Events: []Event{
				{Type: EventActivity, Text: "inspect the accepted plan"},
				{Type: EventMessage, Text: "implementation complete"},
			},
			Disposition: DispositionSucceeded,
			Summary:     "completed scripted coding work",
		},
	})
}

func testSessionRequest() SessionRequest {
	return SessionRequest{
		SessionID:    "ses_test",
		AttemptID:    "att_test",
		FeatureID:    "fea_test",
		Role:         RoleCoder,
		Instructions: "Implement the accepted plan.",
	}
}
