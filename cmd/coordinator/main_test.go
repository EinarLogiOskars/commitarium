package main

import (
	"testing"
	"time"
)

func TestLoadConfigDefaultsToSimulatedRunner(t *testing.T) {
	loaded, err := loadConfig(func(name string) string {
		if name == "COMMITARIUM_DATABASE_PATH" {
			return "/state/coordinator.db"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("load coordinator config: %v", err)
	}
	if loaded.runnerMode != defaultRunnerMode ||
		loaded.simulatedStepDelay != 250*time.Millisecond ||
		loaded.workerRequestTimeout != defaultWorkerRequestTimeout {
		t.Fatalf("unexpected default config %+v", loaded)
	}
}

func TestLoadConfigAcceptsRealCodexLeadRunner(t *testing.T) {
	values := map[string]string{
		"COMMITARIUM_DATABASE_PATH":                "/state/coordinator.db",
		"COMMITARIUM_RUNNER_MODE":                  realCodexLeadRunnerMode,
		"COMMITARIUM_CODEX_WORKER_URL":             "http://codex-worker:8081",
		"COMMITARIUM_CODEX_WORKER_TOKEN":           "test-token",
		"COMMITARIUM_CODEX_PROFILE_ID":             "profile_test",
		"COMMITARIUM_CODEX_WORKSPACE_ID":           "workspace_test",
		"COMMITARIUM_CODEX_WORKER_REQUEST_TIMEOUT": "4s",
	}
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load real Codex coordinator config: %v", err)
	}
	if loaded.runnerMode != realCodexLeadRunnerMode ||
		loaded.codexWorkerURL != "http://codex-worker:8081" ||
		loaded.codexWorkerToken != "test-token" ||
		loaded.codexAgentProfileID != "profile_test" ||
		loaded.codexWorkspaceID != "workspace_test" ||
		loaded.workerRequestTimeout != 4*time.Second {
		t.Fatalf("unexpected real Codex config %+v", loaded)
	}
}

func TestLoadConfigRejectsIncompleteOrUnknownRunner(t *testing.T) {
	tests := []map[string]string{
		{},
		{
			"COMMITARIUM_DATABASE_PATH": "/state/coordinator.db",
			"COMMITARIUM_RUNNER_MODE":   "automatic-magic",
		},
		{
			"COMMITARIUM_DATABASE_PATH": "/state/coordinator.db",
			"COMMITARIUM_RUNNER_MODE":   realCodexLeadRunnerMode,
		},
		{
			"COMMITARIUM_DATABASE_PATH":                "/state/coordinator.db",
			"COMMITARIUM_RUNNER_MODE":                  realCodexLeadRunnerMode,
			"COMMITARIUM_CODEX_WORKER_URL":             "http://codex-worker:8081",
			"COMMITARIUM_CODEX_WORKER_TOKEN":           "test-token",
			"COMMITARIUM_CODEX_PROFILE_ID":             "profile_test",
			"COMMITARIUM_CODEX_WORKSPACE_ID":           "workspace_test",
			"COMMITARIUM_CODEX_WORKER_REQUEST_TIMEOUT": "zero",
		},
	}
	for index, values := range tests {
		if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
			t.Errorf("case %d accepted invalid config %+v", index, values)
		}
	}
}
