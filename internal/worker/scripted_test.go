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
		result.ProviderSessionID != "fake-codex:ses_test" ||
		result.Summary != "completed scripted coding work" {
		t.Errorf("unexpected result %+v", result)
	}
	if _, ok := <-session.Events(); ok {
		t.Fatal("expected event stream to close after completion")
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
		ProviderSessionID: "fake-codex:ses_test",
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
		FeatureID:    "fea_test",
		Role:         RoleCoder,
		Instructions: "Implement the accepted plan.",
	}
}
