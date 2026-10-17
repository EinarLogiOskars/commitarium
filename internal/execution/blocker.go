package execution

import (
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
