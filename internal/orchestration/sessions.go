package orchestration

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

type SessionRegistry interface {
	Register(sessionID string, session worker.Session) (func(), error)
	Get(sessionID string) (worker.Session, bool)
}

type ActiveSessions struct {
	mu      sync.RWMutex
	nextKey uint64
	entries map[string]activeSession
}

type activeSession struct {
	key     uint64
	session worker.Session
}

var ErrInvalidActiveSession = errors.New("invalid active session")
var ErrSessionAlreadyActive = errors.New("session is already active")
var ErrSessionNotActive = errors.New("session is not active")

func NewActiveSessions() *ActiveSessions {
	return &ActiveSessions{entries: make(map[string]activeSession)}
}

// Register returns an idempotent cleanup function. The private registration
// key prevents an old cleanup from deleting a newer session with the same ID.
func (s *ActiveSessions) Register(
	sessionID string,
	session worker.Session,
) (func(), error) {
	if strings.TrimSpace(sessionID) == "" || session == nil {
		return nil, ErrInvalidActiveSession
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[sessionID]; exists {
		return nil, fmt.Errorf("%w: %q", ErrSessionAlreadyActive, sessionID)
	}
	s.nextKey++
	entry := activeSession{key: s.nextKey, session: session}
	s.entries[sessionID] = entry

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		current, exists := s.entries[sessionID]
		if exists && current.key == entry.key {
			delete(s.entries, sessionID)
		}
	}, nil
}

func (s *ActiveSessions) Get(sessionID string) (worker.Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, exists := s.entries[sessionID]
	return entry.session, exists
}
