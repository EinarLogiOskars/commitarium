package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestLoadConfigUsesRequiredValuesAndDefaults(t *testing.T) {
	tokenFile := writeWorkerToken(t, "test-token")
	values := map[string]string{
		"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db",
		"COMMITARIUM_WORKER_TOKEN_FILE":    tokenFile,
	}
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load worker config: %v", err)
	}
	if loaded.databasePath != "/state/worker.db" ||
		loaded.bearerToken != "test-token" ||
		loaded.listenAddress != defaultListenAddress ||
		loaded.stepDelay != defaultStepDelay {
		t.Fatalf("worker config = %+v", loaded)
	}
}

func TestLoadConfigAcceptsRuntimeOverrides(t *testing.T) {
	tokenFile := writeWorkerToken(t, "test-token")
	values := map[string]string{
		"COMMITARIUM_WORKER_DATABASE_PATH":  "/state/worker.db",
		"COMMITARIUM_WORKER_TOKEN_FILE":     tokenFile,
		"COMMITARIUM_WORKER_LISTEN_ADDRESS": "127.0.0.1:9090",
		"COMMITARIUM_WORKER_STEP_DELAY":     "3s",
	}
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load overridden worker config: %v", err)
	}
	if loaded.listenAddress != "127.0.0.1:9090" || loaded.stepDelay != 3*time.Second {
		t.Fatalf("overridden worker config = %+v", loaded)
	}
}

func TestLoadConfigRejectsUnsafeOrMissingValues(t *testing.T) {
	validToken := writeWorkerToken(t, "test-token")
	unsafeToken := writeWorkerToken(t, "token with spaces")
	tests := []map[string]string{
		{"COMMITARIUM_WORKER_TOKEN_FILE": validToken},
		{"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db"},
		{
			"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db",
			"COMMITARIUM_WORKER_TOKEN_FILE":    unsafeToken,
		},
		{
			"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db",
			"COMMITARIUM_WORKER_TOKEN_FILE":    validToken,
			"COMMITARIUM_WORKER_STEP_DELAY":    "not-a-duration",
		},
		{
			"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db",
			"COMMITARIUM_WORKER_TOKEN_FILE":    validToken,
			"COMMITARIUM_WORKER_STEP_DELAY":    "-1s",
		},
	}
	for index, values := range tests {
		if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
			t.Errorf("case %d accepted invalid config %+v", index, values)
		}
	}
}

func TestNormalizeObservableEventPreservesRecoveryAssessment(t *testing.T) {
	normalized, err := normalizeObservableEvent(t.Context(), worker.Event{
		Type: worker.EventRecoveryAssessment,
		Text: "durable state is consistent",
		RecoveryAssessment: &worker.RecoveryAssessment{
			Consistent: true, RequiresUserReview: true,
		},
	})
	if err != nil {
		t.Fatalf("normalize simulated recovery event: %v", err)
	}
	if normalized.Type != workerhttp.EventRecoveryAssessment ||
		normalized.Text != "durable state is consistent" ||
		normalized.RecoveryAssessment == nil ||
		!normalized.RecoveryAssessment.Consistent ||
		!normalized.RecoveryAssessment.RequiresUserReview {
		t.Fatalf("normalized recovery event = %+v", normalized)
	}
	if _, err := normalizeObservableEvent(t.Context(), worker.Event{Type: "unknown", Text: "bad"}); err == nil {
		t.Fatal("expected unknown simulated event type to fail")
	}
	submitted, err := normalizeObservableEvent(t.Context(), worker.Event{
		Type: worker.EventPlanSubmitted, Text: "Final agreed plan",
	})
	if err != nil || submitted.Type != workerhttp.EventPlanSubmitted ||
		submitted.Text != "Final agreed plan" {
		t.Fatalf("normalized plan submission = %+v, error=%v", submitted, err)
	}
}

func TestLoadConfigBuildsCodexRuntimeSettings(t *testing.T) {
	values := validCodexConfig(t.TempDir())
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load Codex worker config: %v", err)
	}
	if loaded.adapter != "codex" ||
		loaded.codex.profileID != "profile_test" ||
		loaded.codex.forgejoRole != worker.RoleLead ||
		loaded.codex.sandbox != "danger-full-access" {
		t.Fatalf("Codex worker config = %+v", loaded)
	}
}

func TestLoadConfigRejectsIncompleteCodexConfiguration(t *testing.T) {
	values := validCodexConfig(t.TempDir())
	delete(values, "COMMITARIUM_CODEX_PROFILE_ID")
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected incomplete Codex configuration to fail")
	}
	values = validCodexConfig(t.TempDir())
	values["COMMITARIUM_CODEX_SANDBOX"] = "host-write"
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected unsupported Codex sandbox to fail")
	}
	values = validCodexConfig(t.TempDir())
	values["COMMITARIUM_CODEX_FORGEJO_ROLE"] = "coder"
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected unsupported Codex Forgejo role to fail")
	}
}

func TestCodexRuntimeUsesPrivateProfileAndRealCapabilities(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace_test")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	values := validCodexConfig(workspace)
	values["COMMITARIUM_CODEX_WORKSPACE_ROOT"] = root
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load Codex worker config: %v", err)
	}
	workerRuntime, err := newRuntime(loaded)
	if err != nil {
		t.Fatalf("create Codex runtime: %v", err)
	}
	assignment := workerhttp.Assignment{
		AgentProfileID: "profile_test",
		ProjectID:      "prj_test",
		FeatureID:      "fea_test",
		Role:           workerhttp.RoleLead,
		WorkspaceID:    "workspace_test",
	}
	resolved, err := workerRuntime.environmentResolver.Resolve(t.Context(), assignment)
	if err != nil {
		t.Fatalf("resolve Codex launch environment: %v", err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("canonicalize workspace: %v", err)
	}
	if resolved.WorkingDirectory != canonicalWorkspace || !slices.Contains(
		resolved.Variables,
		"CODEX_HOME="+root,
	) {
		t.Fatalf("Codex launch environment = %+v", resolved)
	}
	if !slices.Contains(resolved.Variables, "COMMITARIUM_FORGEJO_LOGIN=codex-lead") ||
		!slices.Contains(resolved.Variables, "GIT_AUTHOR_NAME=Commitarium Codex Lead") ||
		!slices.Contains(resolved.Variables, "GIT_CONFIG_VALUE_0=Authorization: token codex-forgejo-test-token") {
		t.Fatalf("lead launch environment omitted its scoped Forgejo identity: %+v", resolved)
	}
	assignment.Role = workerhttp.RoleReviewer
	reviewerEnvironment, err := workerRuntime.environmentResolver.Resolve(t.Context(), assignment)
	if err != nil {
		t.Fatalf("resolve reviewer assignment through lead worker: %v", err)
	}
	for _, variable := range reviewerEnvironment.Variables {
		if strings.HasPrefix(variable, "COMMITARIUM_FORGEJO_") ||
			strings.Contains(variable, "Authorization: token") {
			t.Fatalf("wrong-role assignment received lead credential variable %q", variable)
		}
	}
	if !slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityForceStop) ||
		slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityPause) ||
		slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityContinue) {
		t.Fatalf("Codex capabilities = %v", workerRuntime.capabilities)
	}
	if workerRuntime.providerKind != workerhttp.ProviderCodex {
		t.Fatalf("Codex provider kind = %q", workerRuntime.providerKind)
	}
}

func TestLoadConfigBuildsClaudeRuntimeSettings(t *testing.T) {
	values := validClaudeConfig(t.TempDir())
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load Claude worker config: %v", err)
	}
	if loaded.adapter != "claude_code" ||
		loaded.claude.profileID != "profile_claude_test" ||
		loaded.claude.forgejoRole != worker.RoleReviewer ||
		loaded.claude.permissionMode != "bypassPermissions" {
		t.Fatalf("Claude worker config = %+v", loaded)
	}
}

func TestLoadConfigRejectsIncompleteClaudeConfiguration(t *testing.T) {
	values := validClaudeConfig(t.TempDir())
	delete(values, "COMMITARIUM_CLAUDE_PROFILE_ID")
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected incomplete Claude configuration to fail")
	}
	values = validClaudeConfig(t.TempDir())
	values["COMMITARIUM_CLAUDE_PERMISSION_MODE"] = "host-write"
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected unsupported Claude permission mode to fail")
	}
	values = validClaudeConfig(t.TempDir())
	values["COMMITARIUM_CLAUDE_FORGEJO_ROLE"] = "consultant"
	if _, err := loadConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("expected unsupported Claude Forgejo role to fail")
	}
}

func TestClaudeRuntimeUsesPrivateProfileAndBoundedTurnCapabilities(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace_claude_test")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	values := validClaudeConfig(workspace)
	values["COMMITARIUM_CLAUDE_WORKSPACE_ROOT"] = root
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load Claude worker config: %v", err)
	}
	workerRuntime, err := newRuntime(loaded)
	if err != nil {
		t.Fatalf("create Claude runtime: %v", err)
	}
	assignment := workerhttp.Assignment{
		AgentProfileID: "profile_claude_test",
		ProjectID:      "prj_test",
		FeatureID:      "fea_test",
		Role:           workerhttp.RoleReviewer,
		WorkspaceID:    "workspace_claude_test",
	}
	resolved, err := workerRuntime.environmentResolver.Resolve(t.Context(), assignment)
	if err != nil {
		t.Fatalf("resolve Claude launch environment: %v", err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("canonicalize workspace: %v", err)
	}
	if resolved.WorkingDirectory != canonicalWorkspace ||
		!slices.Contains(resolved.Variables, "CLAUDE_CONFIG_DIR="+root) ||
		!slices.Contains(resolved.Variables, "HOME="+root) ||
		!slices.Contains(resolved.Variables, "COMMITARIUM_FORGEJO_LOGIN=claude-reviewer") ||
		!slices.Contains(resolved.Variables, "GIT_AUTHOR_NAME=Commitarium Claude Reviewer") ||
		!slices.Contains(resolved.Variables, "GIT_CONFIG_VALUE_0=Authorization: token claude-forgejo-test-token") {
		t.Fatalf("Claude launch environment = %+v", resolved)
	}
	assignment.Role = workerhttp.RoleLead
	leadEnvironment, err := workerRuntime.environmentResolver.Resolve(t.Context(), assignment)
	if err != nil {
		t.Fatalf("resolve lead assignment through reviewer worker: %v", err)
	}
	for _, variable := range leadEnvironment.Variables {
		if strings.HasPrefix(variable, "COMMITARIUM_FORGEJO_") ||
			strings.Contains(variable, "Authorization: token") {
			t.Fatalf("wrong-role assignment received reviewer credential variable %q", variable)
		}
	}
	if workerRuntime.providerKind != workerhttp.ProviderClaudeCode ||
		!slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityResume) ||
		!slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityForceStop) ||
		slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityMessage) ||
		slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityPause) ||
		slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityContinue) {
		t.Fatalf("Claude provider/capabilities = %q %v", workerRuntime.providerKind, workerRuntime.capabilities)
	}
}

func TestSimulatedEnvironmentResolverPreservesAssignmentWithoutInheritingVariables(t *testing.T) {
	assignment := workerhttp.Assignment{
		AgentProfileID: "profile_test", ProjectID: "prj_test", FeatureID: "fea_test",
		Role: workerhttp.RoleCoder, WorkspaceID: "workspace_test",
	}
	directory := t.TempDir()
	resolved, err := simulatedEnvironmentResolver(directory).Resolve(t.Context(), assignment)
	if err != nil {
		t.Fatalf("resolve simulated environment: %v", err)
	}
	if resolved.AgentProfileID != assignment.AgentProfileID ||
		resolved.ProjectID != assignment.ProjectID ||
		resolved.FeatureID != assignment.FeatureID ||
		resolved.Role != worker.RoleCoder ||
		resolved.WorkspaceID != assignment.WorkspaceID ||
		resolved.WorkingDirectory != directory || resolved.Variables == nil ||
		len(resolved.Variables) != 0 {
		t.Fatalf("simulated launch environment = %+v", resolved)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := simulatedEnvironmentResolver(directory).Resolve(cancelled, assignment); err == nil {
		t.Fatal("expected cancelled simulated resolution to fail")
	}
}

func TestSimulatedScriptsCoverEveryWorkerRole(t *testing.T) {
	scripts := simulatedScripts()
	for _, role := range []worker.Role{
		worker.RoleLead,
		worker.RoleConsultant,
		worker.RoleCoder,
		worker.RoleReviewer,
	} {
		script, ok := scripts[role]
		if !ok || len(script.Events) == 0 || script.Summary == "" {
			t.Errorf("missing complete deterministic script for role %q: %+v", role, script)
		}
	}
}

func TestCheckHealthRejectsUnreachableWorker(t *testing.T) {
	if err := checkHealth(t.Context(), "http://127.0.0.1:1/internal/v1/health"); err == nil {
		t.Fatal("expected unreachable worker health check to fail")
	}
}

func validCodexConfig(workspace string) map[string]string {
	tokenFile := filepath.Join(filepath.Dir(workspace), "codex-forgejo-token")
	if err := os.WriteFile(tokenFile, []byte("codex-forgejo-test-token\n"), 0o600); err != nil {
		panic(err)
	}
	return map[string]string{
		"COMMITARIUM_WORKER_DATABASE_PATH":      "/state/worker.db",
		"COMMITARIUM_WORKER_TOKEN_FILE":         writeTokenBesideWorkspace(workspace, "worker-api-token", "test-token"),
		"COMMITARIUM_WORKER_ADAPTER":            "codex",
		"COMMITARIUM_CODEX_PROVIDER_STATE_PATH": filepath.Dir(workspace),
		"COMMITARIUM_CODEX_WORKSPACE_ROOT":      filepath.Dir(workspace),
		"COMMITARIUM_CODEX_PROFILE_ID":          "profile_test",
		"COMMITARIUM_CODEX_SANDBOX":             "danger-full-access",
		"COMMITARIUM_CODEX_FORGEJO_URL":         "http://forgejo:3000",
		"COMMITARIUM_CODEX_FORGEJO_TOKEN_FILE":  tokenFile,
		"COMMITARIUM_CODEX_FORGEJO_LOGIN":       "codex-lead",
		"COMMITARIUM_CODEX_FORGEJO_ROLE":        "lead",
		"COMMITARIUM_CODEX_GIT_AUTHOR_NAME":     "Commitarium Codex Lead",
		"COMMITARIUM_CODEX_GIT_AUTHOR_EMAIL":    "codex-lead@commitarium.local",
	}
}

func validClaudeConfig(workspace string) map[string]string {
	tokenFile := filepath.Join(filepath.Dir(workspace), "claude-forgejo-token")
	if err := os.WriteFile(tokenFile, []byte("claude-forgejo-test-token\n"), 0o600); err != nil {
		panic(err)
	}
	return map[string]string{
		"COMMITARIUM_WORKER_DATABASE_PATH":       "/state/worker.db",
		"COMMITARIUM_WORKER_TOKEN_FILE":          writeTokenBesideWorkspace(workspace, "worker-api-token", "test-token"),
		"COMMITARIUM_WORKER_ADAPTER":             "claude_code",
		"COMMITARIUM_CLAUDE_PROVIDER_STATE_PATH": filepath.Dir(workspace),
		"COMMITARIUM_CLAUDE_WORKSPACE_ROOT":      filepath.Dir(workspace),
		"COMMITARIUM_CLAUDE_PROFILE_ID":          "profile_claude_test",
		"COMMITARIUM_CLAUDE_MODEL":               "test-model",
		"COMMITARIUM_CLAUDE_PERMISSION_MODE":     "bypassPermissions",
		"COMMITARIUM_CLAUDE_FORGEJO_URL":         "http://forgejo:3000",
		"COMMITARIUM_CLAUDE_FORGEJO_TOKEN_FILE":  tokenFile,
		"COMMITARIUM_CLAUDE_FORGEJO_LOGIN":       "claude-reviewer",
		"COMMITARIUM_CLAUDE_FORGEJO_ROLE":        "reviewer",
		"COMMITARIUM_CLAUDE_GIT_AUTHOR_NAME":     "Commitarium Claude Reviewer",
		"COMMITARIUM_CLAUDE_GIT_AUTHOR_EMAIL":    "claude-reviewer@commitarium.local",
	}
}

func writeWorkerToken(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker-token")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("write worker token: %v", err)
	}
	return path
}

func writeTokenBesideWorkspace(workspace string, name string, value string) string {
	path := filepath.Join(workspace, name)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		panic(err)
	}
	return path
}
