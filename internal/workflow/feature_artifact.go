package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

const FeatureArtifactUpdatedPayloadVersion = 1

type FeatureArtifact struct {
	FeatureID string
	Kind      featureartifact.Kind
	Revision  int
	Document  string
	Actor     Actor
	UpdatedAt time.Time
}

type FeatureArtifactMutation struct {
	EventID          string
	FeatureID        string
	Kind             featureartifact.Kind
	ExpectedRevision int
	Document         string
	DocumentDigest   string
	Actor            Actor
	OccurredAt       time.Time
	IdempotencyKey   string
}

type FeatureArtifactUpdatedPayload struct {
	Kind             featureartifact.Kind `json:"kind"`
	Revision         int                  `json:"revision"`
	ExpectedRevision int                  `json:"expected_revision"`
	SHA256           string               `json:"sha256"`
}

var (
	ErrArtifactNotFound = errors.New("feature artifact not found")
	ErrArtifactConflict = errors.New("feature artifact revision conflict")
)

func (artifact FeatureArtifact) Validate() error {
	if strings.TrimSpace(artifact.FeatureID) == "" || !artifact.Kind.IsValid() || artifact.Revision < 1 ||
		!json.Valid([]byte(artifact.Document)) || !artifact.Actor.Kind.IsValid() ||
		strings.TrimSpace(artifact.Actor.ID) == "" || artifact.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: stored feature artifact is incomplete", featureartifact.ErrInvalidArtifact)
	}
	return nil
}

func (mutation FeatureArtifactMutation) Validate() error {
	if strings.TrimSpace(mutation.EventID) == "" || strings.TrimSpace(mutation.FeatureID) == "" ||
		!mutation.Kind.IsValid() || mutation.ExpectedRevision < 0 || !json.Valid([]byte(mutation.Document)) ||
		mutation.DocumentDigest != DigestArtifactDocument(mutation.Document) || !mutation.Actor.Kind.IsValid() ||
		strings.TrimSpace(mutation.Actor.ID) == "" || mutation.OccurredAt.IsZero() ||
		strings.TrimSpace(mutation.IdempotencyKey) == "" {
		return fmt.Errorf("%w: artifact mutation is incomplete", featureartifact.ErrInvalidArtifact)
	}
	return nil
}

func DigestArtifactDocument(document string) string {
	digest := sha256.Sum256([]byte(document))
	return hex.EncodeToString(digest[:])
}

func EncodeFeatureArtifactUpdatedPayload(kind featureartifact.Kind, revision, expectedRevision int, digest string) (string, error) {
	payload := FeatureArtifactUpdatedPayload{
		Kind: kind, Revision: revision, ExpectedRevision: expectedRevision, SHA256: digest,
	}
	if !kind.IsValid() || revision < 1 || expectedRevision < 0 || len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("%w: invalid feature artifact event", ErrInvalidPayload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode feature artifact update: %w", err)
	}
	return string(encoded), nil
}

func DecodeFeatureArtifactUpdatedPayload(version int, payload string) (FeatureArtifactUpdatedPayload, error) {
	if version != FeatureArtifactUpdatedPayloadVersion {
		return FeatureArtifactUpdatedPayload{}, fmt.Errorf(
			"%w: feature artifact version %d", ErrUnsupportedPayloadVersion, version,
		)
	}
	decoded := FeatureArtifactUpdatedPayload{}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return FeatureArtifactUpdatedPayload{}, fmt.Errorf("%w: decode feature artifact update: %v", ErrInvalidPayload, err)
	}
	if !decoded.Kind.IsValid() || decoded.Revision < 1 || decoded.ExpectedRevision < 0 || len(decoded.SHA256) != sha256.Size*2 {
		return FeatureArtifactUpdatedPayload{}, fmt.Errorf("%w: invalid feature artifact update", ErrInvalidPayload)
	}
	return decoded, nil
}
