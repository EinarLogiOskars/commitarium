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

func TestWorkflowStorePersistsAcceptanceTestArtifactKind(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	document := `{"plan_version":1,"test_commit_id":"0123456789abcdef0123456789abcdef01234567","implementation_commit_id":"","tests":[{"id":"exports-csv","position":1,"title":"Exports visible rows","status":"pending","note":""}]}`
	mutation := workflow.FeatureArtifactMutation{
		EventID: "evt_acceptance_1", FeatureID: created.ID, Kind: featureartifact.KindAcceptanceTests,
		ExpectedRevision: 0, Document: document, DocumentDigest: workflow.DigestArtifactDocument(document),
		Actor:          workflow.Actor{Kind: workflow.ActorKindAgent, ID: "ses_reviewer"},
		OccurredAt:     time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC),
		IdempotencyKey: "attempt-1:acceptance-tests",
	}
	artifact, _, err := store.PutFeatureArtifact(t.Context(), mutation)
	if err != nil {
		t.Fatalf("put acceptance tests: %v", err)
	}
	if artifact.Kind != featureartifact.KindAcceptanceTests || artifact.Document != document {
		t.Fatalf("unexpected acceptance artifact: %+v", artifact)
	}
}

func TestWorkflowStorePersistsHandoffBriefArtifactKind(t *testing.T) {
	store, features := newTestWorkflowStore(t)
	created := createWorkflowTestFeature(t, features)
	document := `{"goal":"Add due dates.","areas":["backend/app/models"],"considerations":["Keep existing rows valid."],"open_questions":[],"base_commit_id":"0123456789abcdef0123456789abcdef01234567"}`
	mutation := workflow.FeatureArtifactMutation{
		EventID: "evt_brief_1", FeatureID: created.ID, Kind: featureartifact.KindHandoffBrief,
		ExpectedRevision: 0, Document: document, DocumentDigest: workflow.DigestArtifactDocument(document),
		Actor:          workflow.Actor{Kind: workflow.ActorKindAgent, ID: "ast_test"},
		OccurredAt:     time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC),
		IdempotencyKey: "assistant-1:brief",
	}
	if _, _, err := store.PutFeatureArtifact(t.Context(), mutation); err != nil {
		t.Fatalf("put handoff brief: %v", err)
	}
	loaded, err := store.GetFeatureArtifact(t.Context(), created.ID, featureartifact.KindHandoffBrief)
	if err != nil || loaded.Document != document {
		t.Fatalf("loaded=%+v error=%v", loaded, err)
	}
}
