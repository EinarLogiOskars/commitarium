package toolchain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ProvisioningState struct {
	Status    ProvisioningStatus `json:"status"`
	Message   string             `json:"message,omitempty"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func ReadProvisioningState(configPath string) (ProvisioningState, error) {
	contents, err := os.ReadFile(provisioningPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return ProvisioningState{Status: ProvisioningPending}, nil
	}
	if err != nil {
		return ProvisioningState{}, err
	}
	var state ProvisioningState
	if err := json.Unmarshal(contents, &state); err != nil || !state.Status.IsValid() {
		return ProvisioningState{}, errors.New("invalid provisioning state")
	}
	return state, nil
}

func WriteProvisioningState(configPath string, state ProvisioningState) error {
	if !state.Status.IsValid() {
		return errors.New("invalid provisioning status")
	}
	state.Message = strings.TrimSpace(state.Message)
	if len(state.Message) > 512 {
		return errors.New("provisioning message is too long")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := atomicWrite(provisioningPath(configPath), append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write provisioning state: %w", err)
	}
	return nil
}

func (status ProvisioningStatus) IsValid() bool {
	switch status {
	case ProvisioningPending, ProvisioningInstalling, ProvisioningReady, ProvisioningFailed:
		return true
	default:
		return false
	}
}

func provisioningPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "provisioning.json")
}
