package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type Role string

const (
	RoleLead     Role = "lead"
	RoleReviewer Role = "reviewer"
)

var (
	ErrCatalogUnavailable = errors.New("model catalog unavailable")
	ErrModelUnavailable   = errors.New("model is not available from the selected worker")
)

type Catalog struct {
	Provider  project.AgentProvider
	Role      Role
	Models    []workerhttp.Model
	FetchedAt *time.Time
	LastError string
}

type Store interface {
	Load(context.Context) ([]Catalog, error)
	SaveSuccess(context.Context, Catalog) error
	SaveFailure(context.Context, project.AgentProvider, Role, string) error
}

type Source interface {
	Models(context.Context) (workerhttp.ModelsResponse, error)
}

type SourceKey struct {
	Provider project.AgentProvider
	Role     Role
}

type Service struct {
	store     Store
	sources   map[SourceKey]Source
	mu        sync.RWMutex
	cache     map[SourceKey]Catalog
	refreshMu sync.Mutex
}

func New(store Store, sources map[SourceKey]Source) (*Service, error) {
	if store == nil || len(sources) == 0 {
		return nil, errors.New("model catalog store and sources are required")
	}
	service := &Service{store: store, sources: sources, cache: make(map[SourceKey]Catalog)}
	return service, nil
}

func (service *Service) Load(ctx context.Context) error {
	catalogs, err := service.store.Load(ctx)
	if err != nil {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, catalog := range catalogs {
		catalog.Models = slices.Clone(catalog.Models)
		service.cache[SourceKey{Provider: catalog.Provider, Role: catalog.Role}] = catalog
	}
	return nil
}

func (service *Service) Refresh(ctx context.Context) ([]Catalog, error) {
	service.refreshMu.Lock()
	defer service.refreshMu.Unlock()
	var refreshErrors []error
	for key, source := range service.sources {
		response, err := source.Models(ctx)
		if err == nil {
			expected := workerhttp.ProviderCodex
			if key.Provider == project.AgentProviderClaude {
				expected = workerhttp.ProviderClaudeCode
			}
			if response.Provider != expected {
				err = fmt.Errorf("worker reported provider %q, expected %q", response.Provider, expected)
			}
		}
		if err != nil {
			message := strings.TrimSpace(err.Error())
			_ = service.store.SaveFailure(ctx, key.Provider, key.Role, message)
			service.mu.Lock()
			catalog := service.cache[key]
			catalog.Provider, catalog.Role, catalog.LastError = key.Provider, key.Role, message
			service.cache[key] = catalog
			service.mu.Unlock()
			refreshErrors = append(refreshErrors, fmt.Errorf("refresh %s %s models: %w", key.Provider, key.Role, err))
			continue
		}
		catalog := Catalog{
			Provider: key.Provider, Role: key.Role, Models: slices.Clone(response.Models),
			FetchedAt: &response.FetchedAt,
		}
		if err := service.store.SaveSuccess(ctx, catalog); err != nil {
			refreshErrors = append(refreshErrors, err)
			continue
		}
		service.mu.Lock()
		service.cache[key] = catalog
		service.mu.Unlock()
	}
	return service.List(ctx), errors.Join(refreshErrors...)
}

func (service *Service) List(context.Context) []Catalog {
	service.mu.RLock()
	defer service.mu.RUnlock()
	result := make([]Catalog, 0, len(service.sources))
	for key := range service.sources {
		catalog := service.cache[key]
		catalog.Provider, catalog.Role = key.Provider, key.Role
		catalog.Models = slices.Clone(catalog.Models)
		if catalog.Models == nil {
			catalog.Models = make([]workerhttp.Model, 0)
		}
		result = append(result, catalog)
	}
	slices.SortFunc(result, func(a, b Catalog) int {
		if compared := strings.Compare(string(a.Provider), string(b.Provider)); compared != 0 {
			return compared
		}
		return strings.Compare(string(a.Role), string(b.Role))
	})
	return result
}

func (service *Service) ValidateSelection(_ context.Context, providers project.AgentProviders, models project.AgentModels) error {
	if err := models.ValidateRequired(); err != nil {
		return err
	}
	selections := []struct {
		key   SourceKey
		model string
	}{
		{SourceKey{providers.Lead, RoleLead}, models.Lead},
		{SourceKey{providers.Reviewer, RoleReviewer}, models.Reviewer},
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	for _, selection := range selections {
		catalog, exists := service.cache[selection.key]
		if !exists || catalog.FetchedAt == nil || len(catalog.Models) == 0 {
			return fmt.Errorf("%w: %s %s", ErrCatalogUnavailable, selection.key.Provider, selection.key.Role)
		}
		if !slices.ContainsFunc(catalog.Models, func(model workerhttp.Model) bool { return model.ID == selection.model }) {
			return fmt.Errorf("%w: %s is not offered by %s %s", ErrModelUnavailable, selection.model, selection.key.Provider, selection.key.Role)
		}
	}
	return nil
}

func (service *Service) Run(ctx context.Context, interval time.Duration, report func(error)) {
	if report == nil {
		report = func(error) {}
	}
	if _, err := service.Refresh(ctx); err != nil {
		report(err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := service.Refresh(ctx); err != nil {
				report(err)
			}
		}
	}
}
