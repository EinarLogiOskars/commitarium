package main

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agentworker"
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
		loaded.workerRequestTimeout != defaultWorkerRequestTimeout ||
		loaded.attemptStartTimeout != defaultAttemptStartTimeout ||
		loaded.forgejoURL != defaultForgejoURL ||
		loaded.forgejoOwner != "commitarium_admin" ||
		loaded.forgejoHostURL != defaultForgejoHostURL ||
		loaded.forgejoTokenFile != defaultForgejoTokenFile ||
		loaded.forgejoViewerLogin != defaultForgejoViewerLogin ||
		loaded.forgejoTimeout != defaultForgejoTimeout ||
		loaded.workspaceRoot != defaultWorkspaceRoot || loaded.gitExecutable != "git" {
		t.Fatalf("unexpected default config %+v", loaded)
	}
}

func TestLoadConfigAcceptsForgejoOverrides(t *testing.T) {
	values := map[string]string{
		"COMMITARIUM_DATABASE_PATH":           "/state/coordinator.db",
		"COMMITARIUM_FORGEJO_URL":             "http://forgejo-test:4000/",
		"COMMITARIUM_FORGEJO_OWNER":           "coordinator-test",
		"COMMITARIUM_FORGEJO_TOKEN_FILE":      "/private/forgejo-token",
		"COMMITARIUM_FORGEJO_VIEWER_LOGIN":    "audit-viewer",
		"COMMITARIUM_FORGEJO_REQUEST_TIMEOUT": "3s",
		"COMMITARIUM_FORGEJO_HOST_URL":        "http://localhost:4001",
		"COMMITARIUM_WORKSPACE_ROOT":          "/managed-workspaces",
		"COMMITARIUM_GIT_EXECUTABLE":          "/usr/bin/git",
	}
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load Forgejo config: %v", err)
	}
	if loaded.forgejoURL != "http://forgejo-test:4000/" ||
		loaded.forgejoOwner != "coordinator-test" ||
		loaded.forgejoHostURL != "http://localhost:4001" ||
		loaded.forgejoTokenFile != "/private/forgejo-token" ||
		loaded.forgejoViewerLogin != "audit-viewer" ||
		loaded.forgejoTimeout != 3*time.Second ||
		loaded.workspaceRoot != "/managed-workspaces" || loaded.gitExecutable != "/usr/bin/git" {
		t.Fatalf("unexpected Forgejo config %+v", loaded)
	}
}

func TestLoadConfigAcceptsRealAgentRunner(t *testing.T) {
	values := realAgentConfigValues(t)
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load real-agent coordinator config: %v", err)
	}
	if loaded.runnerMode != realAgentsRunnerMode ||
		loaded.agentWorkerURLTemplate != "http://{agent}.workers:8081" ||
		loaded.agentWorkerTokenDir != "/private/agent-workers" ||
		loaded.workerRequestTimeout != 4*time.Second || loaded.attemptStartTimeout != 5*time.Minute {
		t.Fatalf("unexpected real-agent config %+v", loaded)
	}
}

func TestLoadConfigDefaultsAgentWorkerAddressing(t *testing.T) {
	values := realAgentConfigValues(t)
	delete(values, "COMMITARIUM_AGENT_WORKER_URL_TEMPLATE")
	delete(values, "COMMITARIUM_AGENT_WORKER_TOKEN_DIR")
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load real-agent coordinator config: %v", err)
	}
	if loaded.agentWorkerURLTemplate != agentworker.DefaultURLTemplate ||
		loaded.agentWorkerTokenDir != defaultAgentWorkerTokenDir {
		t.Fatalf("unexpected agent worker defaults %+v", loaded)
	}
}

func TestLoadConfigNormalizesLegacyRealCodexLeadRunner(t *testing.T) {
	values := realAgentConfigValues(t)
	values["COMMITARIUM_RUNNER_MODE"] = legacyRealCodexLeadRunnerMode

	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load legacy real-agent coordinator config: %v", err)
	}
	if loaded.runnerMode != realAgentsRunnerMode {
		t.Fatalf("legacy runner mode normalized to %q; expected %q", loaded.runnerMode, realAgentsRunnerMode)
	}
}

func TestLoadConfigRejectsInvalidAttemptStartTimeout(t *testing.T) {
	values := realAgentConfigValues(t)
	values["COMMITARIUM_WORKER_ATTEMPT_START_TIMEOUT"] = "zero"
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("accepted invalid worker attempt start timeout")
	}
}

func realAgentConfigValues(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"COMMITARIUM_DATABASE_PATH":                "/state/coordinator.db",
		"COMMITARIUM_RUNNER_MODE":                  realAgentsRunnerMode,
		"COMMITARIUM_AGENT_WORKER_URL_TEMPLATE":    "http://{agent}.workers:8081",
		"COMMITARIUM_AGENT_WORKER_TOKEN_DIR":       "/private/agent-workers",
		"COMMITARIUM_WORKER_REQUEST_TIMEOUT":       "4s",
		"COMMITARIUM_WORKER_ATTEMPT_START_TIMEOUT": "5m",
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
			"COMMITARIUM_DATABASE_PATH":          "/state/coordinator.db",
			"COMMITARIUM_RUNNER_MODE":            realAgentsRunnerMode,
			"COMMITARIUM_WORKER_REQUEST_TIMEOUT": "zero",
		},
		{
			"COMMITARIUM_DATABASE_PATH":           "/state/coordinator.db",
			"COMMITARIUM_FORGEJO_REQUEST_TIMEOUT": "zero",
		},
	}
	for index, values := range tests {
		if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
			t.Errorf("case %d accepted invalid config %+v", index, values)
		}
	}
}
