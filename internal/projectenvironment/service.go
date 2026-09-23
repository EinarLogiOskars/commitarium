package projectenvironment

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

type Store interface {
	Create(context.Context, Request) (Request, bool, error)
	Get(context.Context, string) (Request, error)
	ListByProject(context.Context, string) ([]Request, error)
	Transition(context.Context, string, Status, Status, map[string]string, string, time.Time) (Request, bool, error)
	ListApprovedPackages(context.Context) ([]string, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (service *Service) Request(ctx context.Context, request Request) (Request, bool, error) {
	request.SystemPackages, _ = NormalizePackages(request.SystemPackages)
	request.Reason = strings.TrimSpace(request.Reason)
	request.Status = StatusRequested
	request.ResolvedPackages = map[string]string{}
	request.RequestedAt = service.now()
	request.UpdatedAt = request.RequestedAt
	request.CompletedAt = nil
	request.Error = ""
	if err := request.Validate(); err != nil {
		return Request{}, false, err
	}
	return service.store.Create(ctx, request)
}

func (service *Service) Get(ctx context.Context, id string) (Request, error) {
	if strings.TrimSpace(id) == "" {
		return Request{}, ErrInvalid
	}
	return service.store.Get(ctx, id)
}

func (service *Service) ListByProject(ctx context.Context, projectID string) ([]Request, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, ErrInvalid
	}
	return service.store.ListByProject(ctx, projectID)
}

func (service *Service) Approve(ctx context.Context, id string) (Request, bool, error) {
	return service.store.Transition(ctx, id, StatusRequested, StatusApproved, map[string]string{}, "", service.now())
}

func (service *Service) Reject(ctx context.Context, id, reason string) (Request, bool, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 1000 {
		return Request{}, false, ErrInvalid
	}
	request, err := service.Get(ctx, id)
	if err != nil {
		return Request{}, false, err
	}
	if request.Status == StatusRejected {
		return request, false, nil
	}
	if request.Status != StatusRequested && request.Status != StatusApproved && request.Status != StatusFailed {
		return Request{}, false, ErrConflict
	}
	return service.store.Transition(ctx, id, request.Status, StatusRejected, map[string]string{}, reason, service.now())
}

func (service *Service) BeginProvisioning(ctx context.Context, id string) (Request, bool, error) {
	request, err := service.Get(ctx, id)
	if err != nil {
		return Request{}, false, err
	}
	if request.Status == StatusProvisioning {
		return request, false, nil
	}
	// Provisioning may have completed durably before the coordinator could
	// resume the provider conversation. Let the trusted native command replay
	// the completion path so that fixed continuation command can be retried.
	if request.Status == StatusReady {
		return request, false, nil
	}
	if request.Status == StatusFailed {
		return service.store.Transition(ctx, id, StatusFailed, StatusProvisioning, map[string]string{}, "", service.now())
	}
	return service.store.Transition(ctx, id, StatusApproved, StatusProvisioning, map[string]string{}, "", service.now())
}

func (service *Service) Complete(ctx context.Context, id string, resolved map[string]string) (Request, bool, error) {
	request, err := service.Get(ctx, id)
	if err != nil {
		return Request{}, false, err
	}
	if request.Status == StatusReady {
		return request, false, nil
	}
	if len(resolved) == 0 {
		return Request{}, false, ErrInvalid
	}
	for name, version := range resolved {
		if !packageName.MatchString(name) || strings.TrimSpace(version) == "" ||
			len(version) > 256 || strings.ContainsRune(version, '\x00') {
			return Request{}, false, ErrInvalid
		}
	}
	for _, name := range request.SystemPackages {
		if strings.TrimSpace(resolved[name]) == "" {
			return Request{}, false, ErrInvalid
		}
	}
	return service.store.Transition(ctx, id, StatusProvisioning, StatusReady, resolved, "", service.now())
}

func (service *Service) Fail(ctx context.Context, id, detail string) (Request, bool, error) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "Environment provisioning failed."
	}
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	return service.store.Transition(ctx, id, StatusProvisioning, StatusFailed, map[string]string{}, detail, service.now())
}

func (service *Service) ApprovedPackages(ctx context.Context) ([]string, error) {
	packages, err := service.store.ListApprovedPackages(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(packages)
	return packages, nil
}

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
