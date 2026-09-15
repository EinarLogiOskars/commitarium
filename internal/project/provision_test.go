package project

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordingRepositoryInitializer struct {
	calls      int
	spec       RepositoryInitializationSpec
	repository ForgejoRepository
	err        error
}

func (initializer *recordingRepositoryInitializer) InitializeRepository(
	_ context.Context,
	spec RepositoryInitializationSpec,
) (ForgejoRepository, error) {
	initializer.calls++
	initializer.spec = spec
	return initializer.repository, initializer.err
}

func newProvisioningService(store Store, initializer RepositoryInitializer) *Service {
	return NewServiceWithRepositoryServices(store, nil, nil, nil, initializer)
}

func TestServiceCreatesProjectWithReadyForgejoRepository(t *testing.T) {
	store := NewMemoryStore()
	initializer := &recordingRepositoryInitializer{repository: ForgejoRepository{
		Owner: "commitarium", Name: "demo", DefaultBranch: "main",
	}}
	service := newProvisioningService(store, initializer)
	fixedTime := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedTime }

	created, err := service.CreateProvisionedWithAgentModels(
		t.Context(), "desktop-create-1", " Demo ", "", DefaultDialogueLimits(),
		DefaultAgentProviders(), AgentModels{}, "",
	)
	if err != nil {
		t.Fatalf("create provisioned project: %v", err)
	}
	if created.ID != projectIDForCreateKey("desktop-create-1") || created.Name != "Demo" {
		t.Fatalf("unexpected project %+v", created)
	}
	if created.ForgejoRepository == nil || created.ForgejoRepository.BoundAt != fixedTime {
		t.Fatalf("repository was not bound: %+v", created.ForgejoRepository)
	}
	if initializer.calls != 1 || initializer.spec.ProjectID != created.ID ||
		initializer.spec.Repository != importRepositoryName("Demo", created.ID) ||
		initializer.spec.DefaultBranch != "main" {
		t.Fatalf("unexpected initializer request calls=%d spec=%+v", initializer.calls, initializer.spec)
	}
}

func TestServiceProvisionedCreateRetryDoesNotInitializeAgain(t *testing.T) {
	store := NewMemoryStore()
	initializer := &recordingRepositoryInitializer{repository: ForgejoRepository{
		Owner: "commitarium", Name: "demo", DefaultBranch: "main",
	}}
	service := newProvisioningService(store, initializer)
	create := func() Project {
		created, err := service.CreateProvisionedWithAgentModels(
			t.Context(), "same-key", "Demo", "", DefaultDialogueLimits(),
			DefaultAgentProviders(), AgentModels{}, "",
		)
		if err != nil {
			t.Fatalf("create provisioned project: %v", err)
		}
		return created
	}
	first := create()
	second := create()
	if first.ID != second.ID || initializer.calls != 1 {
		t.Fatalf("retry duplicated work: first=%q second=%q initializer calls=%d", first.ID, second.ID, initializer.calls)
	}
}

func TestServiceProvisionedCreateRejectsConflictingRetry(t *testing.T) {
	store := NewMemoryStore()
	initializer := &recordingRepositoryInitializer{repository: ForgejoRepository{
		Owner: "commitarium", Name: "demo", DefaultBranch: "main",
	}}
	service := newProvisioningService(store, initializer)
	if _, err := service.CreateProvisionedWithAgentModels(
		t.Context(), "same-key", "Demo", "", DefaultDialogueLimits(),
		DefaultAgentProviders(), AgentModels{}, "",
	); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := service.CreateProvisionedWithAgentModels(
		t.Context(), "same-key", "Different", "", DefaultDialogueLimits(),
		DefaultAgentProviders(), AgentModels{}, "",
	)
	if !errors.Is(err, ErrProjectCreateConflict) || initializer.calls != 1 {
		t.Fatalf("expected idempotency conflict without external work, got %v calls=%d", err, initializer.calls)
	}
}

func TestServiceRepairsUnboundProjectAndLeavesBoundProjectAlone(t *testing.T) {
	store := NewMemoryStore()
	created := Project{
		ID: "prj_old", Name: "Old project", RecoveryPolicy: RecoveryPolicyApprovalRequired,
		MergePolicy: DefaultMergePolicy(), AutonomyPolicy: DefaultAutonomyPolicy(),
		DialogueLimits: DefaultDialogueLimits(), AgentProviders: DefaultAgentProviders(),
		CreatedAt: time.Now().UTC(),
	}
	if err := store.Create(t.Context(), created); err != nil {
		t.Fatalf("store old project: %v", err)
	}
	initializer := &recordingRepositoryInitializer{repository: ForgejoRepository{
		Owner: "commitarium", Name: "old-project", DefaultBranch: "main",
	}}
	service := newProvisioningService(store, initializer)
	repaired, err := service.ProvisionForgejoRepository(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("repair repository: %v", err)
	}
	second, err := service.ProvisionForgejoRepository(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("repeat repair: %v", err)
	}
	if repaired.ForgejoRepository == nil || second.ForgejoRepository == nil || initializer.calls != 1 {
		t.Fatalf("repair was not idempotent: repaired=%+v second=%+v calls=%d", repaired, second, initializer.calls)
	}
}
