package projectdeletion

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workorder"
)

type deletionStoreStub struct {
	deletion Deletion
	err      error
	features []string
	released bool
	finished bool
}

func (stub *deletionStoreStub) BeginDeletion(context.Context, string, string, bool) (Deletion, error) {
	return stub.deletion, stub.err
}
func (stub *deletionStoreStub) ReleaseDeletion(context.Context, string, string) error {
	stub.released = true
	return nil
}
func (stub *deletionStoreStub) ListFeatureIDs(context.Context, string) ([]string, error) {
	return append([]string(nil), stub.features...), nil
}
func (stub *deletionStoreStub) FinishDeletion(context.Context, string, string) error {
	stub.finished = true
	return nil
}

type featureDeleterStub struct {
	store *deletionStoreStub
	order *[]string
}

func (stub featureDeleterStub) Delete(_ context.Context, projectID, featureID string) (workorder.Result, error) {
	*stub.order = append(*stub.order, "feature:"+featureID)
	for index, id := range stub.store.features {
		if id == featureID {
			stub.store.features = append(stub.store.features[:index], stub.store.features[index+1:]...)
			break
		}
	}
	return workorder.Result{ProjectID: projectID, FeatureID: featureID, Deleted: true}, nil
}

type runStopperStub struct{ order *[]string }

func (stub runStopperStub) StopFeatureRuns(_ context.Context, featureID, _ string) error {
	*stub.order = append(*stub.order, "stop:"+featureID)
	return nil
}

type cleanerStub struct{ order *[]string }

func (stub cleanerStub) DeleteProject(_ context.Context, projectID string) error {
	*stub.order = append(*stub.order, "toolchain:"+projectID)
	return nil
}

type assistantCleanerStub struct {
	order  *[]string
	active bool
}

func (stub assistantCleanerStub) DeleteProject(_ context.Context, projectID, _ string, force bool) error {
	*stub.order = append(*stub.order, "assistants:"+projectID)
	if stub.active && !force {
		return ErrActive
	}
	return nil
}

type repositoryCleanerStub struct {
	order    *[]string
	failures int
	deleted  [][2]string
}

func (stub *repositoryCleanerStub) DeleteRepository(_ context.Context, owner, name string) error {
	*stub.order = append(*stub.order, "repository:"+owner+"/"+name)
	stub.deleted = append(stub.deleted, [2]string{owner, name})
	if stub.failures > 0 {
		stub.failures--
		return project.ErrForgejoUnavailable
	}
	return nil
}

func TestDeleteUsesFeatureTeardownAndFinishesAfterRepository(t *testing.T) {
	order := []string{}
	store := &deletionStoreStub{features: []string{"fea_one", "fea_two"}, deletion: Deletion{
		ProjectID: "prj_test", IdempotencyKey: "delete-1",
		Repository: &project.ForgejoRepository{Owner: "owner", Name: "repo", DefaultBranch: "main"},
	}}
	repositories := &repositoryCleanerStub{order: &order}
	service := NewService(store, featureDeleterStub{store: store, order: &order}, runStopperStub{&order},
		cleanerStub{&order}, assistantCleanerStub{order: &order}, repositories)
	result, err := service.Delete(t.Context(), "prj_test", "delete-1", false)
	if err != nil || !result.Deleted || !store.finished {
		t.Fatalf("delete result=%+v finished=%t err=%v", result, store.finished, err)
	}
	want := []string{"assistants:prj_test", "feature:fea_one", "feature:fea_two", "toolchain:prj_test", "repository:owner/repo"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("cleanup order=%v want=%v", order, want)
	}
}

func TestForcedDeleteStopsRunsBeforeFeatureTeardown(t *testing.T) {
	order := []string{}
	store := &deletionStoreStub{features: []string{"fea_one"}, deletion: Deletion{
		ProjectID: "prj_test", IdempotencyKey: "delete-force", Force: true,
	}}
	service := NewService(store, featureDeleterStub{store: store, order: &order}, runStopperStub{&order},
		cleanerStub{&order}, assistantCleanerStub{order: &order, active: true}, nil)
	if _, err := service.Delete(t.Context(), "prj_test", "delete-force", true); err != nil {
		t.Fatalf("force delete: %v", err)
	}
	want := []string{"stop:fea_one", "assistants:prj_test", "feature:fea_one", "toolchain:prj_test"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("cleanup order=%v want=%v", order, want)
	}
}

func TestActiveAssistantRefusesWithoutDeletingAndReleasesClaim(t *testing.T) {
	order := []string{}
	store := &deletionStoreStub{features: []string{"fea_one"}, deletion: Deletion{
		ProjectID: "prj_test", IdempotencyKey: "delete-1",
	}}
	service := NewService(store, featureDeleterStub{store: store, order: &order}, runStopperStub{&order},
		cleanerStub{&order}, assistantCleanerStub{order: &order, active: true}, nil)
	if _, err := service.Delete(t.Context(), "prj_test", "delete-1", false); !errors.Is(err, ErrActive) {
		t.Fatalf("expected active error, got %v", err)
	}
	if !store.released || store.finished || len(order) != 1 || order[0] != "assistants:prj_test" {
		t.Fatalf("active delete mutated artifacts: released=%t finished=%t order=%v", store.released, store.finished, order)
	}
}

func TestRetryAfterRepositoryFailureResumesAndExactCompletedRetrySucceeds(t *testing.T) {
	order := []string{}
	store := &deletionStoreStub{features: []string{"fea_one"}, deletion: Deletion{
		ProjectID: "prj_test", IdempotencyKey: "delete-1",
		Repository: &project.ForgejoRepository{Owner: "owner", Name: "repo", DefaultBranch: "main"},
	}}
	repositories := &repositoryCleanerStub{order: &order, failures: 1}
	service := NewService(store, featureDeleterStub{store: store, order: &order}, runStopperStub{&order},
		cleanerStub{&order}, assistantCleanerStub{order: &order}, repositories)
	if _, err := service.Delete(t.Context(), "prj_test", "delete-1", false); !errors.Is(err, project.ErrForgejoUnavailable) {
		t.Fatalf("expected transient repository failure, got %v", err)
	}
	if store.finished || len(store.features) != 0 {
		t.Fatalf("partial failure did not retain resumable state: finished=%t features=%v", store.finished, store.features)
	}
	if _, err := service.Delete(t.Context(), "prj_test", "delete-1", false); err != nil || !store.finished {
		t.Fatalf("resume delete: finished=%t err=%v", store.finished, err)
	}
	store.deletion.Completed = true
	orderBefore := len(order)
	if _, err := service.Delete(t.Context(), "prj_test", "delete-1", false); err != nil {
		t.Fatalf("completed retry: %v", err)
	}
	if len(order) != orderBefore {
		t.Fatal("completed retry repeated external cleanup")
	}
}
