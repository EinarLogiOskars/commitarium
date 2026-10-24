package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// BlockerKind says what kind of failure blocked a run (ADR-032).
type BlockerKind string

const (
	// BlockerAgent: the agent gave up, broke something, or misunderstood.
	BlockerAgent BlockerKind = "agent"
	// BlockerCheck: a coordinator check of git or Forgejo state failed.
	BlockerCheck BlockerKind = "check"
	// BlockerEnvironment: a worker, the network, or a login failed.
	BlockerEnvironment BlockerKind = "environment"
	// BlockerLimit: an account reached its usage limit.
	BlockerLimit BlockerKind = "limit"
	// BlockerOther: anything not yet named.
	BlockerOther BlockerKind = "other"
)

// Blocker is what a run's latest blocker is.
type Blocker struct {
	RunID string
	Kind  BlockerKind
	// Check names the coordinator check that failed, when one did.
	Check string
	// Detail is the full error or the agent's own words.
	Detail string
	// Reason is the run's reason when it was blocked.
	Reason      string
	Fingerprint string
	RecordedAt  time.Time
}

// BlockerFingerprint recognises the same failure when it repeats.
func BlockerFingerprint(kind BlockerKind, check, detail string) string {
	digest := sha256.Sum256([]byte(string(kind) + "\x00" + check + "\x00" + strings.TrimSpace(detail)))
	return hex.EncodeToString(digest[:8])
}

// RecoveryHow is a way to recover a blocked run.
type RecoveryHow string

const (
	RecoveryRecheck           RecoveryHow = "recheck"
	RecoveryContinue          RecoveryHow = "continue"
	RecoveryMessage           RecoveryHow = "message"
	RecoveryFreshConversation RecoveryHow = "fresh_conversation"
	RecoveryBackToReady       RecoveryHow = "back_to_ready"
)

// Valid reports whether how is a recovery.
func (how RecoveryHow) Valid() bool {
	switch how {
	case RecoveryRecheck, RecoveryContinue, RecoveryMessage, RecoveryFreshConversation, RecoveryBackToReady:
		return true
	}
	return false
}

// Recovery is one recovery tried on a blocked run.
type Recovery struct {
	ID            string
	RunID         string
	Fingerprint   string
	How           RecoveryHow
	Guidance      string
	FromAssistant bool
	CreatedAt     time.Time
}

// AgentMessage is one message an agent wrote in a run.
type AgentMessage struct {
	Role       string
	Text       string
	OccurredAt time.Time
}

// Recovery limits on the assistant's choices (ADR-032). The user's own
// buttons are never limited.
const (
	// MaxRecoveriesPerFailure is how many recoveries one failure (blocker
	// fingerprint) gets before the user hears of it.
	MaxRecoveriesPerFailure = 4
	// MaxRecoveriesPerRun bounds a run whose failures keep changing.
	MaxRecoveriesPerRun = 8
)

// RecoveryExhausted reports whether recovering a run's blocker has run out:
// its failure had as many recoveries as it gets, the run as many in total,
// or the same failure came back after a fresh conversation (ADR-032).
func RecoveryExhausted(blocker Blocker, recoveries []Recovery) bool {
	onFailure, freshTried := 0, false
	for _, recovery := range recoveries {
		if recovery.Fingerprint == blocker.Fingerprint {
			onFailure++
			freshTried = freshTried || recovery.How == RecoveryFreshConversation
		}
	}
	return onFailure >= MaxRecoveriesPerFailure || len(recoveries) >= MaxRecoveriesPerRun || freshTried
}

// BlockerStore keeps what blocked a run and the recoveries tried on it.
type BlockerStore interface {
	RecordRunBlocker(context.Context, Blocker) error
	RunBlocker(context.Context, string) (Blocker, bool, error)
	RecordRunRecovery(context.Context, Recovery) error
	RunRecoveries(context.Context, string) ([]Recovery, error)
}

// RecordRunBlocker stores a run's latest blocker, when the store keeps them.
func (s *Service) RecordRunBlocker(ctx context.Context, blocker Blocker) error {
	if store, ok := s.store.(BlockerStore); ok {
		return store.RecordRunBlocker(ctx, blocker)
	}
	return nil
}

// RunBlocker returns a run's latest blocker; false when none was recorded.
func (s *Service) RunBlocker(ctx context.Context, runID string) (Blocker, bool, error) {
	if store, ok := s.store.(BlockerStore); ok {
		return store.RunBlocker(ctx, runID)
	}
	return Blocker{}, false, nil
}

// RecordRunRecovery stores a recovery tried on a blocked run, when the store
// keeps them.
func (s *Service) RecordRunRecovery(ctx context.Context, recovery Recovery) error {
	if store, ok := s.store.(BlockerStore); ok {
		return store.RecordRunRecovery(ctx, recovery)
	}
	return nil
}

// RunRecoveries lists the recoveries tried on a run.
func (s *Service) RunRecoveries(ctx context.Context, runID string) ([]Recovery, error) {
	if store, ok := s.store.(BlockerStore); ok {
		return store.RunRecoveries(ctx, runID)
	}
	return nil, nil
}

// NewestRun is the run started last; runs must not be empty.
func NewestRun(runs []Run) Run {
	newest := runs[0]
	for _, candidate := range runs[1:] {
		if candidate.StartedAt.After(newest.StartedAt) ||
			(candidate.StartedAt.Equal(newest.StartedAt) && candidate.ID < newest.ID) {
			newest = candidate
		}
	}
	return newest
}
