package execution

import "testing"

func TestEventBrokerPublishesOnlyToMatchingSession(t *testing.T) {
	broker := newEventBroker(1)
	events, cancel := broker.subscribe("ses_one")
	defer cancel()
	expected := Event{ID: "sev_one", SessionID: "ses_one"}
	broker.publish(Event{ID: "sev_other", SessionID: "ses_two"})
	broker.publish(expected)

	select {
	case actual := <-events:
		if actual != expected {
			t.Errorf("expected event %+v, got %+v", expected, actual)
		}
	default:
		t.Fatal("expected matching event to be published")
	}
}

func TestEventBrokerDisconnectsSlowSubscriber(t *testing.T) {
	broker := newEventBroker(1)
	events, cancel := broker.subscribe("ses_test")
	defer cancel()
	broker.publish(Event{ID: "sev_one", SessionID: "ses_test"})
	broker.publish(Event{ID: "sev_two", SessionID: "ses_test"})

	first, open := <-events
	if !open || first.ID != "sev_one" {
		t.Fatalf("expected buffered first event, got %+v, open %t", first, open)
	}
	if _, open := <-events; open {
		t.Fatal("expected slow subscriber channel to be closed")
	}
}
