package database

import (
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func TestWorkflowStorePersistsArtifactRevisionAndEventAtomically(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	document := `{"goal":"Ship export.","open_questions":[]}`
	mutation := workflow.FeatureArtifactMutation{
		EventID: "evt_artifact_1", FeatureID: created.ID, Kind: featureartifact.KindGoalDraft,
		ExpectedRevision: 0, Document: document, DocumentDigest: workflow.DigestArtifactDocument(document),
		Actor:          workflow.Actor{Kind: workflow.ActorKindAgent, ID: "ses_lead"},
		OccurredAt:     time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC),
		IdempotencyKey: "attempt-1:goal-draft",
	}
	artifact, event, err := store.PutFeatureArtifact(t.Context(), mutation)
	if err != nil {
		t.Fatalf("put artifact: %v", err)
	}
	if artifact.Revision != 1 || artifact.Document != document || event.Type != workflow.EventTypeArtifactUpdated || event.Sequence != 1 {
		t.Fatalf("artifact=%+v event=%+v", artifact, event)
	}
	loaded, err := store.GetFeatureArtifact(t.Context(), created.ID, featureartifact.KindGoalDraft)
	if err != nil || loaded != artifact {
		t.Fatalf("loaded=%+v error=%v", loaded, err)
	}
	retried, retriedEvent, err := store.PutFeatureArtifact(t.Context(), mutation)
	if err != nil || retried != artifact || retriedEvent != event {
		t.Fatalf("retry artifact=%+v event=%+v error=%v", retried, retriedEvent, err)
	}
	conflict := mutation
	conflict.EventID = "evt_artifact_2"
	conflict.IdempotencyKey = "attempt-2:goal-draft"
	if _, _, err := store.PutFeatureArtifact(t.Context(), conflict); !errors.Is(err, workflow.ErrArtifactConflict) {
		t.Fatalf("stale revision error=%v", err)
	}
}
