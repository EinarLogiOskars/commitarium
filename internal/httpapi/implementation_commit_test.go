package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

func TestPublishImplementationHandlerReturnsExactCommitReceipt(t *testing.T) {
	now := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	completedAt := now.Add(time.Second)
	publication := workspace.Publication{
		ID: "pub_one", RunID: "run_implementation", WorkspaceID: "wsp_fea_test",
		IdempotencyKey: "commit-one", CommitMessage: "feat: publish implementation",
		RemoteCommitIDBefore: "0123456789abcdef0123456789abcdef01234567",
		LocalCommitIDBefore:  "0123456789abcdef0123456789abcdef01234567",
		CommitID:             "abcdefabcdefabcdefabcdefabcdefabcdefabcd",
		Status:               workspace.PublicationStatusCompleted, CreatedAt: now, CompletedAt: &completedAt,
	}
	starter := &planningStarterStub{publication: publication, publicationCreated: true}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, starter,
	)
	request := httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run_implementation/implementation/commit",
		strings.NewReader(`{"message":"feat: publish implementation"}`),
	)
	request.Header.Set("Idempotency-Key", "commit-one")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if starter.receivedRunID != publication.RunID || starter.receivedKey != "commit-one" ||
		starter.receivedCommitMessage != publication.CommitMessage {
		t.Fatalf("unexpected publication call: %+v", starter)
	}
	var response implementationPublicationResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.CommitID != publication.CommitID || response.Status != workspace.PublicationStatusCompleted {
		t.Fatalf("unexpected publication response %+v", response)
	}
}

func TestPublishImplementationHandlerRequiresDeliberateValidRequest(t *testing.T) {
	starter := &planningStarterStub{}
	handler := NewWithWorkspaceAndRealWorkflowService(
		nil, nil, nil, planningExecutionStub{}, nil, nil, nil, starter,
	)
	missingKey := httptest.NewRecorder()
	handler.ServeHTTP(missingKey, httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run/implementation/commit",
		strings.NewReader(`{"message":"feat: test"}`),
	))
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("expected missing key 400, got %d", missingKey.Code)
	}
	badMessage := httptest.NewRequest(
		http.MethodPost, "/api/v1/runs/run/implementation/commit",
		strings.NewReader(`{"message":"first\nsecond"}`),
	)
	badMessage.Header.Set("Idempotency-Key", "commit-one")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, badMessage)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected multiline message 400, got %d", recorder.Code)
	}
}
