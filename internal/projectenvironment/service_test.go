package projectenvironment

import (
	"context"
	"testing"
	"time"
)

type memoryStore struct{ requests map[string]Request }

func (store *memoryStore) Create(_ context.Context, request Request) (Request, bool, error) {
	if stored, ok := store.requests[request.ID]; ok {
		return stored, false, nil
	}
	store.requests[request.ID] = request
	return request, true, nil
}

func (store *memoryStore) Get(_ context.Context, id string) (Request, error) {
	request, ok := store.requests[id]
	if !ok {
		return Request{}, ErrNotFound
	}
	return request, nil
}

func (store *memoryStore) ListByProject(_ context.Context, projectID string) ([]Request, error) {
	requests := make([]Request, 0)
	for _, request := range store.requests {
		if request.ProjectID == projectID {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

func (store *memoryStore) Transition(_ context.Context, id string, from, to Status, resolved map[string]string, detail string, occurredAt time.Time) (Request, bool, error) {
	request, ok := store.requests[id]
	if !ok {
		return Request{}, false, ErrNotFound
	}
	if request.Status == to {
		return request, false, nil
	}
	if request.Status != from {
		return Request{}, false, ErrConflict
	}
	request.Status, request.ResolvedPackages, request.Error, request.UpdatedAt = to, resolved, detail, occurredAt
	if to == StatusReady || to == StatusRejected {
		request.CompletedAt = &occurredAt
	}
	store.requests[id] = request
	return request, true, nil
}

func (store *memoryStore) ListApprovedPackages(context.Context) ([]string, error) {
	return nil, nil
}

func newTestRequest(id string) Request {
	return Request{
		ID: id, ProjectID: "project", FeatureID: "feature", RunID: "run",
		SessionID: "session", AttemptID: "attempt-" + id,
		SystemPackages: []string{"libvips-dev"}, Reason: "Image support needs libvips.",
	}
}

func TestFailedApprovedRequestCanBeRejected(t *testing.T) {
	store := &memoryStore{requests: map[string]Request{}}
	service := NewService(store)
	request, _, err := service.Request(t.Context(), newTestRequest("failed"))
	if err != nil {
		t.Fatal(err)
	}
	if request, _, err = service.Approve(t.Context(), request.ID); err != nil {
		t.Fatal(err)
	}
	if request, _, err = service.BeginProvisioning(t.Context(), request.ID); err != nil {
		t.Fatal(err)
	}
	if request, _, err = service.Fail(t.Context(), request.ID, "package not found"); err != nil {
		t.Fatal(err)
	}
	request, changed, err := service.Reject(t.Context(), request.ID, "Use a different dependency.")
	if err != nil || !changed || request.Status != StatusRejected || request.CompletedAt == nil {
		t.Fatalf("Reject failed request = %#v, %t, %v", request, changed, err)
	}
}

func TestReadyProvisioningRequestCanReplay(t *testing.T) {
	store := &memoryStore{requests: map[string]Request{}}
	service := NewService(store)
	request, _, err := service.Request(t.Context(), newTestRequest("ready"))
	if err != nil {
		t.Fatal(err)
	}
	if request, _, err = service.Approve(t.Context(), request.ID); err != nil {
		t.Fatal(err)
	}
	if request, _, err = service.BeginProvisioning(t.Context(), request.ID); err != nil {
		t.Fatal(err)
	}
	if request, _, err = service.Complete(t.Context(), request.ID, map[string]string{"libvips-dev": "8.16.1-1"}); err != nil {
		t.Fatal(err)
	}
	replayed, changed, err := service.BeginProvisioning(t.Context(), request.ID)
	if err != nil || changed || replayed.Status != StatusReady {
		t.Fatalf("replay ready provisioning = %#v, %t, %v", replayed, changed, err)
	}
}
