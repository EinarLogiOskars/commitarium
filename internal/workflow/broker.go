package workflow

import "sync"

const defaultSubscriberBuffer = 16

type eventBroker struct {
	mu         sync.Mutex
	bufferSize int
	nextID     uint64
	byFeature  map[string]map[uint64]chan Event
}

func newEventBroker(bufferSize int) *eventBroker {
	return &eventBroker{
		bufferSize: bufferSize,
		byFeature:  make(map[string]map[uint64]chan Event),
	}
}

func (b *eventBroker) subscribe(featureID string) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	subscriberID := b.nextID
	events := make(chan Event, b.bufferSize)
	if b.byFeature[featureID] == nil {
		b.byFeature[featureID] = make(map[uint64]chan Event)
	}
	b.byFeature[featureID][subscriberID] = events

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.remove(featureID, subscriberID)
		})
	}
	return events, cancel
}

func (b *eventBroker) publish(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for subscriberID, events := range b.byFeature[event.AggregateID] {
		select {
		case events <- event:
		default:
			delete(b.byFeature[event.AggregateID], subscriberID)
			close(events)
		}
	}
	if len(b.byFeature[event.AggregateID]) == 0 {
		delete(b.byFeature, event.AggregateID)
	}
}

func (b *eventBroker) remove(featureID string, subscriberID uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subscribers := b.byFeature[featureID]
	events, ok := subscribers[subscriberID]
	if !ok {
		return
	}
	delete(subscribers, subscriberID)
	close(events)
	if len(subscribers) == 0 {
		delete(b.byFeature, featureID)
	}
}
