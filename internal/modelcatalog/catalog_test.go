package modelcatalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type memoryStore struct{ catalogs map[SourceKey]Catalog }

func (store *memoryStore) Load(context.Context) ([]Catalog, error) {
	result := make([]Catalog, 0, len(store.catalogs))
	for _, catalog := range store.catalogs {
		result = append(result, catalog)
	}
	return result, nil
}

func (store *memoryStore) SaveSuccess(_ context.Context, catalog Catalog) error {
	store.catalogs[SourceKey{catalog.Provider, catalog.Role}] = catalog
	return nil
}

func (store *memoryStore) SaveFailure(_ context.Context, provider project.AgentProvider, role Role, message string) error {
	key := SourceKey{provider, role}
	catalog := store.catalogs[key]
	catalog.Provider, catalog.Role, catalog.LastError = provider, role, message
	store.catalogs[key] = catalog
	return nil
}

type sourceFunc func(context.Context) (workerhttp.ModelsResponse, error)

func (source sourceFunc) Models(ctx context.Context) (workerhttp.ModelsResponse, error) {
	return source(ctx)
}

func TestRefreshPersistsCatalogAndValidatesExactRoleSelections(t *testing.T) {
	now := time.Now().UTC()
	store := &memoryStore{catalogs: make(map[SourceKey]Catalog)}
	sources := map[SourceKey]Source{
		{project.AgentProviderCodex, RoleLead}: sourceFunc(func(context.Context) (workerhttp.ModelsResponse, error) {
			return workerhttp.ModelsResponse{Provider: workerhttp.ProviderCodex, Models: []workerhttp.Model{{ID: "gpt-test-1", DisplayName: "GPT Test 1"}}, FetchedAt: now}, nil
		}),
		{project.AgentProviderClaude, RoleReviewer}: sourceFunc(func(context.Context) (workerhttp.ModelsResponse, error) {
			return workerhttp.ModelsResponse{Provider: workerhttp.ProviderClaudeCode, Models: []workerhttp.Model{{ID: "claude-test-20260914", DisplayName: "Claude Test"}}, FetchedAt: now}, nil
		}),
	}
	service, err := New(store, sources)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateSelection(t.Context(), project.AgentProviders{
		Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude,
	}, project.AgentModels{Lead: "gpt-test-1", Reviewer: "claude-test-20260914"}); err != nil {
		t.Fatalf("validate catalog selection: %v", err)
	}
	if err := service.ValidateSelection(t.Context(), project.AgentProviders{
		Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude,
	}, project.AgentModels{Lead: "gpt-missing-1", Reviewer: "claude-test-20260914"}); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("missing model error = %v", err)
	}
}

func TestRefreshFailureKeepsLastSuccessfulCatalog(t *testing.T) {
	fetched := time.Now().UTC().Add(-time.Hour)
	key := SourceKey{project.AgentProviderCodex, RoleLead}
	store := &memoryStore{catalogs: map[SourceKey]Catalog{key: {
		Provider: key.Provider, Role: key.Role,
		Models: []workerhttp.Model{{ID: "gpt-pinned-1", DisplayName: "Pinned"}}, FetchedAt: &fetched,
	}}}
	service, err := New(store, map[SourceKey]Source{key: sourceFunc(func(context.Context) (workerhttp.ModelsResponse, error) {
		return workerhttp.ModelsResponse{}, errors.New("worker offline")
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	catalogs, err := service.Refresh(t.Context())
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if len(catalogs) != 1 || len(catalogs[0].Models) != 1 || catalogs[0].Models[0].ID != "gpt-pinned-1" || catalogs[0].LastError == "" {
		t.Fatalf("catalog after failure = %#v", catalogs)
	}
}
