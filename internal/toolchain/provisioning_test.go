package toolchain

import (
	"path/filepath"
	"testing"
	"time"
)

func TestProvisioningStateRoundTrip(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mise.toml")
	missing, err := ReadProvisioningState(configPath)
	if err != nil || missing.Status != ProvisioningPending {
		t.Fatalf("missing state=%+v err=%v", missing, err)
	}

	writtenAt := time.Date(2026, time.September, 15, 13, 0, 0, 0, time.UTC)
	written := ProvisioningState{
		Status: ProvisioningInstalling, Message: "Installing exact project runtimes.", UpdatedAt: writtenAt,
	}
	if err := WriteProvisioningState(configPath, written); err != nil {
		t.Fatalf("write provisioning state: %v", err)
	}
	loaded, err := ReadProvisioningState(configPath)
	if err != nil || loaded != written {
		t.Fatalf("loaded state=%+v err=%v", loaded, err)
	}
}

func TestProvisioningStateRejectsInvalidValues(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "mise.toml")
	if err := WriteProvisioningState(configPath, ProvisioningState{Status: "unknown"}); err == nil {
		t.Fatal("expected invalid status to be rejected")
	}
}
