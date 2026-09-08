// Package workerjournal stores the small durable record a provider worker
// needs for restart-safe inspection, fencing, idempotency, and event replay.
package workerjournal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

var (
	ErrInvalidRecord    = errors.New("invalid worker journal record")
	ErrNotFound         = errors.New("worker journal record not found")
	ErrAttemptConflict  = errors.New("worker attempt identity was reused for different launch data")
	ErrAttemptActive    = errors.New("another worker attempt for the session may still be active")
	ErrMutationConflict = errors.New("worker mutation idempotency key was reused for different input")
	ErrStateConflict    = errors.New("worker journal record is not in the expected state")
	ErrEventConflict    = errors.New("worker event sequence was reused for different content")
	ErrEventSequence    = errors.New("worker event is not the next expected sequence")
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type AttemptCreation struct {
	Attempt        workerhttp.Attempt
	IdempotencyKey string
	RequestDigest  string
}

type AttemptTransition struct {
	Reference         workerhttp.AttemptReference
	Expected          workerhttp.AttemptState
	State             workerhttp.AttemptState
	ProviderSessionID string
	Result            *workerhttp.TerminalResult
	OccurredAt        time.Time
}

type EventAppend struct {
	Event      workerhttp.Event
	AcceptedAt time.Time
}

type RecoveryResult struct {
	AttemptsMarked  int64
	MutationsMarked int64
}

type MutationKind string

const (
	MutationMessage   MutationKind = "message"
	MutationPause     MutationKind = "pause"
	MutationContinue  MutationKind = "continue"
	MutationStop      MutationKind = "stop"
	MutationForceStop MutationKind = "force_stop"
)

type MutationStatus string

const (
	MutationPending       MutationStatus = "pending"
	MutationApplied       MutationStatus = "applied"
	MutationRejected      MutationStatus = "rejected"
	MutationIndeterminate MutationStatus = "indeterminate"
)

type Mutation struct {
	workerhttp.MutationIdentity
	Kind          MutationKind
	RequestDigest string
	Status        MutationStatus
	RequestedAt   time.Time
	UpdatedAt     time.Time
}

type MutationResolution struct {
	workerhttp.MutationIdentity
	Status     MutationStatus
	OccurredAt time.Time
}

func DigestRequest(request any) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode worker mutation for digest: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (creation AttemptCreation) Validate() error {
	identity := workerhttp.MutationIdentity{
		AttemptReference: creation.Attempt.AttemptReference,
		IdempotencyKey:   creation.IdempotencyKey,
	}
	switch {
	case identity.Validate() != nil:
		return fmt.Errorf("%w: invalid launch identity", ErrInvalidRecord)
	case creation.Attempt.Validate() != nil:
		return fmt.Errorf("%w: invalid attempt", ErrInvalidRecord)
	case creation.Attempt.State != workerhttp.AttemptStateStarting:
		return fmt.Errorf("%w: new attempt must start in starting state", ErrInvalidRecord)
	case creation.Attempt.LatestEventSequence != 0:
		return fmt.Errorf("%w: new attempt cannot already have events", ErrInvalidRecord)
	case !digestPattern.MatchString(creation.RequestDigest):
		return fmt.Errorf("%w: request digest must be a lowercase sha256 digest", ErrInvalidRecord)
	default:
		return nil
	}
}

func (transition AttemptTransition) Validate() error {
	switch {
	case transition.Reference.Validate() != nil:
		return fmt.Errorf("%w: invalid attempt reference", ErrInvalidRecord)
	case !transition.Expected.CanTransitionTo(transition.State):
		return fmt.Errorf(
			"%w: attempt cannot transition from %q to %q",
			ErrInvalidRecord,
			transition.Expected,
			transition.State,
		)
	case transition.OccurredAt.IsZero():
		return fmt.Errorf("%w: transition time is required", ErrInvalidRecord)
	case transition.State == workerhttp.AttemptStateTerminal && transition.Result == nil:
		return fmt.Errorf("%w: terminal transition requires a result", ErrInvalidRecord)
	case transition.State != workerhttp.AttemptStateTerminal && transition.Result != nil:
		return fmt.Errorf("%w: nonterminal transition cannot include a result", ErrInvalidRecord)
	default:
		return nil
	}
}

func (kind MutationKind) IsValid() bool {
	switch kind {
	case MutationMessage, MutationPause, MutationContinue, MutationStop, MutationForceStop:
		return true
	default:
		return false
	}
}

func (status MutationStatus) IsValid() bool {
	switch status {
	case MutationPending, MutationApplied, MutationRejected, MutationIndeterminate:
		return true
	default:
		return false
	}
}

func (status MutationStatus) IsTerminal() bool {
	return status == MutationApplied || status == MutationRejected || status == MutationIndeterminate
}

func (mutation Mutation) Validate() error {
	switch {
	case mutation.MutationIdentity.Validate() != nil:
		return fmt.Errorf("%w: invalid mutation identity", ErrInvalidRecord)
	case !mutation.Kind.IsValid():
		return fmt.Errorf("%w: mutation kind %q is not recognized", ErrInvalidRecord, mutation.Kind)
	case !digestPattern.MatchString(mutation.RequestDigest):
		return fmt.Errorf("%w: request digest must be a lowercase sha256 digest", ErrInvalidRecord)
	case !mutation.Status.IsValid():
		return fmt.Errorf("%w: mutation status %q is not recognized", ErrInvalidRecord, mutation.Status)
	case mutation.RequestedAt.IsZero():
		return fmt.Errorf("%w: mutation request time is required", ErrInvalidRecord)
	case mutation.UpdatedAt.Before(mutation.RequestedAt):
		return fmt.Errorf("%w: mutation update precedes request", ErrInvalidRecord)
	default:
		return nil
	}
}

func (resolution MutationResolution) Validate() error {
	switch {
	case resolution.MutationIdentity.Validate() != nil:
		return fmt.Errorf("%w: invalid mutation identity", ErrInvalidRecord)
	case !resolution.Status.IsTerminal():
		return fmt.Errorf("%w: mutation resolution must be terminal", ErrInvalidRecord)
	case resolution.OccurredAt.IsZero():
		return fmt.Errorf("%w: mutation resolution time is required", ErrInvalidRecord)
	default:
		return nil
	}
}

func (appendRequest EventAppend) Validate() error {
	switch {
	case appendRequest.Event.Validate() != nil:
		return fmt.Errorf("%w: invalid worker event", ErrInvalidRecord)
	case appendRequest.AcceptedAt.IsZero():
		return fmt.Errorf("%w: event acceptance time is required", ErrInvalidRecord)
	default:
		return nil
	}
}
