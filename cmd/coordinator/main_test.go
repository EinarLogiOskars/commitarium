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
		loaded.workerRequestTimeout != defaultWorkerRequestTimeout ||
		loaded.forgejoURL != defaultForgejoURL ||
		loaded.forgejoOwner != "commitarium_admin" ||
		loaded.forgejoHostURL != defaultForgejoHostURL ||
		loaded.forgejoTokenFile != defaultForgejoTokenFile ||
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
		loaded.forgejoTimeout != 3*time.Second ||
		loaded.workspaceRoot != "/managed-workspaces" || loaded.gitExecutable != "/usr/bin/git" {
		t.Fatalf("unexpected Forgejo config %+v", loaded)
	}
}

func TestLoadConfigAcceptsRealCodexLeadRunner(t *testing.T) {
	values := map[string]string{
		"COMMITARIUM_DATABASE_PATH":                "/state/coordinator.db",
		"COMMITARIUM_RUNNER_MODE":                  realCodexLeadRunnerMode,
		"COMMITARIUM_CODEX_WORKER_URL":             "http://codex-worker:8081",
		"COMMITARIUM_CODEX_WORKER_TOKEN":           "test-token",
		"COMMITARIUM_CODEX_PROFILE_ID":             "profile_test",
		"COMMITARIUM_CODEX_REVIEWER_WORKER_URL":    "http://codex-reviewer-worker:8081",
		"COMMITARIUM_CODEX_REVIEWER_WORKER_TOKEN":  "reviewer-test-token",
		"COMMITARIUM_CODEX_REVIEWER_PROFILE_ID":    "reviewer_profile_test",
		"COMMITARIUM_CLAUDE_WORKER_URL":            "http://claude-worker:8081",
		"COMMITARIUM_CLAUDE_WORKER_TOKEN":          "claude-test-token",
		"COMMITARIUM_CLAUDE_PROFILE_ID":            "claude_profile_test",
		"COMMITARIUM_CLAUDE_REVIEWER_WORKER_URL":   "http://claude-reviewer-worker:8081",
		"COMMITARIUM_CLAUDE_REVIEWER_WORKER_TOKEN": "claude-reviewer-test-token",
		"COMMITARIUM_CLAUDE_REVIEWER_PROFILE_ID":   "claude_reviewer_profile_test",
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
		loaded.codexReviewerWorkerURL != "http://codex-reviewer-worker:8081" ||
		loaded.codexReviewerWorkerToken != "reviewer-test-token" ||
		loaded.codexReviewerAgentProfileID != "reviewer_profile_test" ||
		loaded.claudeWorkerURL != "http://claude-worker:8081" ||
		loaded.claudeWorkerToken != "claude-test-token" ||
		loaded.claudeAgentProfileID != "claude_profile_test" ||
		loaded.claudeReviewerWorkerURL != "http://claude-reviewer-worker:8081" ||
		loaded.claudeReviewerWorkerToken != "claude-reviewer-test-token" ||
		loaded.claudeReviewerAgentProfileID != "claude_reviewer_profile_test" ||
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
			"COMMITARIUM_CODEX_REVIEWER_WORKER_URL":    "http://codex-reviewer-worker:8081",
			"COMMITARIUM_CODEX_REVIEWER_WORKER_TOKEN":  "reviewer-test-token",
			"COMMITARIUM_CODEX_REVIEWER_PROFILE_ID":    "reviewer_profile_test",
			"COMMITARIUM_CODEX_WORKER_REQUEST_TIMEOUT": "zero",
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
