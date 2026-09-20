package execution

import "sync"

type previewBroker struct {
	mu        sync.Mutex
	nextID    uint64
	bySession map[string]map[uint64]chan MessagePreview
}

func newPreviewBroker() *previewBroker {
	return &previewBroker{bySession: make(map[string]map[uint64]chan MessagePreview)}
}

func (b *previewBroker) subscribe(sessionID string) (<-chan MessagePreview, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	subscriberID := b.nextID
	previews := make(chan MessagePreview, 1)
	if b.bySession[sessionID] == nil {
		b.bySession[sessionID] = make(map[uint64]chan MessagePreview)
	}
	b.bySession[sessionID][subscriberID] = previews
	var once sync.Once
	return previews, func() {
		once.Do(func() { b.remove(sessionID, subscriberID) })
	}
}

func (b *previewBroker) publish(preview MessagePreview) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, previews := range b.bySession[preview.SessionID] {
		select {
		case previews <- preview:
			continue
		default:
		}
		select {
		case <-previews:
		default:
		}
		select {
		case previews <- preview:
		default:
		}
	}
}

func (b *previewBroker) remove(sessionID string, subscriberID uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	previews, exists := b.bySession[sessionID][subscriberID]
	if !exists {
		return
	}
	delete(b.bySession[sessionID], subscriberID)
	close(previews)
	if len(b.bySession[sessionID]) == 0 {
		delete(b.bySession, sessionID)
	}
}
