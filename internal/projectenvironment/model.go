package projectenvironment

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Status string

const (
	StatusRequested    Status = "requested"
	StatusApproved     Status = "approved"
	StatusProvisioning Status = "provisioning"
	StatusReady        Status = "ready"
	StatusRejected     Status = "rejected"
	StatusFailed       Status = "failed"
)

var (
	ErrInvalid  = errors.New("invalid project environment request")
	ErrNotFound = errors.New("project environment request not found")
	ErrConflict = errors.New("project environment request state conflict")
	packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{0,127}$`)
)

type Request struct {
	ID               string            `json:"id"`
	ProjectID        string            `json:"project_id"`
	FeatureID        string            `json:"feature_id"`
	RunID            string            `json:"run_id"`
	SessionID        string            `json:"session_id"`
	AttemptID        string            `json:"attempt_id"`
	SystemPackages   []string          `json:"system_packages"`
	Reason           string            `json:"reason"`
	Status           Status            `json:"status"`
	ResolvedPackages map[string]string `json:"resolved_packages"`
	Error            string            `json:"error,omitempty"`
	RequestedAt      time.Time         `json:"requested_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	CompletedAt      *time.Time        `json:"completed_at,omitempty"`
}

func NormalizePackages(packages []string) ([]string, error) {
	if len(packages) == 0 || len(packages) > 32 {
		return nil, ErrInvalid
	}
	seen := make(map[string]struct{}, len(packages))
	normalized := make([]string, 0, len(packages))
	for _, item := range packages {
		name := strings.TrimSpace(item)
		if name != item || !packageName.MatchString(name) {
			return nil, ErrInvalid
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func (request Request) Validate() error {
	packages, err := NormalizePackages(request.SystemPackages)
	if err != nil || len(packages) != len(request.SystemPackages) {
		return ErrInvalid
	}
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.ProjectID) == "" ||
		strings.TrimSpace(request.FeatureID) == "" || strings.TrimSpace(request.RunID) == "" ||
		strings.TrimSpace(request.SessionID) == "" || strings.TrimSpace(request.AttemptID) == "" ||
		strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 1000 ||
		request.RequestedAt.IsZero() || request.UpdatedAt.Before(request.RequestedAt) {
		return ErrInvalid
	}
	switch request.Status {
	case StatusRequested, StatusApproved, StatusProvisioning, StatusReady, StatusRejected, StatusFailed:
	default:
		return ErrInvalid
	}
	if request.ResolvedPackages == nil {
		return ErrInvalid
	}
	for name, version := range request.ResolvedPackages {
		if !packageName.MatchString(name) || strings.TrimSpace(version) == "" ||
			len(version) > 256 || strings.ContainsRune(version, '\x00') {
			return ErrInvalid
		}
	}
	if len(request.Error) > 1000 || strings.ContainsRune(request.Error, '\x00') {
		return ErrInvalid
	}
	if (request.Status == StatusReady || request.Status == StatusRejected) != (request.CompletedAt != nil) {
		return ErrInvalid
	}
	return nil
}
