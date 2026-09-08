package execution

import "sync"

const defaultSubscriberBuffer = 32

type eventBroker struct {
	mu         sync.Mutex
	bufferSize int
	nextID     uint64
	bySession  map[string]map[uint64]chan Event
}

func newEventBroker(bufferSize int) *eventBroker {
	return &eventBroker{
		bufferSize: bufferSize,
		bySession:  make(map[string]map[uint64]chan Event),
	}
}

func (b *eventBroker) subscribe(sessionID string) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	subscriberID := b.nextID
	events := make(chan Event, b.bufferSize)
	if b.bySession[sessionID] == nil {
		b.bySession[sessionID] = make(map[uint64]chan Event)
	}
	b.bySession[sessionID][subscriberID] = events

	var once sync.Once
	return events, func() {
		once.Do(func() {
			b.remove(sessionID, subscriberID)
		})
	}
}

func (b *eventBroker) publish(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for subscriberID, events := range b.bySession[event.SessionID] {
		select {
		case events <- event:
		default:
			delete(b.bySession[event.SessionID], subscriberID)
			close(events)
		}
	}
	if len(b.bySession[event.SessionID]) == 0 {
		delete(b.bySession, event.SessionID)
	}
}

func (b *eventBroker) remove(sessionID string, subscriberID uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subscribers := b.bySession[sessionID]
	events, exists := subscribers[subscriberID]
	if !exists {
		return
	}
	delete(subscribers, subscriberID)
	close(events)
	if len(subscribers) == 0 {
		delete(b.bySession, sessionID)
	}
}
