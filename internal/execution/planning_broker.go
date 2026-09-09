package execution

import "sync"

// planningMessageBroker delivers newly committed cross-session planning
// messages. SQLite remains authoritative; clients reconnect through history.
type planningMessageBroker struct {
	mu         sync.Mutex
	bufferSize int
	nextID     uint64
	byRun      map[string]map[uint64]chan PlanningMessage
}

func newPlanningMessageBroker(bufferSize int) *planningMessageBroker {
	return &planningMessageBroker{
		bufferSize: bufferSize,
		byRun:      make(map[string]map[uint64]chan PlanningMessage),
	}
}

func (b *planningMessageBroker) subscribe(runID string) (<-chan PlanningMessage, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	subscriberID := b.nextID
	messages := make(chan PlanningMessage, b.bufferSize)
	if b.byRun[runID] == nil {
		b.byRun[runID] = make(map[uint64]chan PlanningMessage)
	}
	b.byRun[runID][subscriberID] = messages

	var once sync.Once
	return messages, func() {
		once.Do(func() { b.remove(runID, subscriberID) })
	}
}

func (b *planningMessageBroker) publish(message PlanningMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for subscriberID, messages := range b.byRun[message.RunID] {
		select {
		case messages <- message:
		default:
			delete(b.byRun[message.RunID], subscriberID)
			close(messages)
		}
	}
	if len(b.byRun[message.RunID]) == 0 {
		delete(b.byRun, message.RunID)
	}
}

func (b *planningMessageBroker) remove(runID string, subscriberID uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	messages, exists := b.byRun[runID][subscriberID]
	if !exists {
		return
	}
	delete(b.byRun[runID], subscriberID)
	close(messages)
	if len(b.byRun[runID]) == 0 {
		delete(b.byRun, runID)
	}
}
