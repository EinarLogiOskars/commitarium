package workflow

import "testing"

func TestEventBrokerPublishesOnlyToMatchingFeature(t *testing.T) {
	broker := newEventBroker(1)
	events, cancel := broker.subscribe("fea_one")
	defer cancel()

	expected := Event{ID: "evt_one", AggregateID: "fea_one"}
	broker.publish(Event{ID: "evt_other", AggregateID: "fea_two"})
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
	events, cancel := broker.subscribe("fea_test")
	defer cancel()

	broker.publish(Event{ID: "evt_one", AggregateID: "fea_test"})
	broker.publish(Event{ID: "evt_two", AggregateID: "fea_test"})

	first, ok := <-events
	if !ok || first.ID != "evt_one" {
		t.Fatalf("expected buffered first event, got %+v, open %t", first, ok)
	}
	if _, ok := <-events; ok {
		t.Fatal("expected slow subscriber channel to be closed")
	}
}
