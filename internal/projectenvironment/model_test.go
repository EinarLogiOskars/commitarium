package projectenvironment

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestNormalizePackagesAllowsOnlyDeterministicDebianNames(t *testing.T) {
	packages, err := NormalizePackages([]string{"libvips-dev", "build-essential", "libvips-dev"})
	if err != nil || !reflect.DeepEqual(packages, []string{"build-essential", "libvips-dev"}) {
		t.Fatalf("normalize packages = %v, %v", packages, err)
	}
	for _, invalid := range [][]string{{}, {"curl;id"}, {"LibVips"}, {" libvips-dev"}} {
		if _, err := NormalizePackages(invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("NormalizePackages(%q) error = %v, want ErrInvalid", invalid, err)
		}
	}
}

func TestReadyRequestValidatesResolvedPackageEvidence(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	request := Request{
		ID: "request", ProjectID: "project", FeatureID: "feature", RunID: "run",
		SessionID: "session", AttemptID: "attempt", SystemPackages: []string{"libvips-dev"},
		Reason: "Image support needs libvips.", Status: StatusReady,
		ResolvedPackages: map[string]string{"libvips-dev": "8.16.1-1"},
		RequestedAt:      now, UpdatedAt: now, CompletedAt: &now,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid ready request: %v", err)
	}
	request.ResolvedPackages["unsafe;name"] = "1"
	if err := request.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe resolved package error = %v", err)
	}
}
