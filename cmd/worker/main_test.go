package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestLoadConfigUsesRequiredValuesAndDefaults(t *testing.T) {
	values := map[string]string{
		"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db",
		"COMMITARIUM_WORKER_TOKEN":         "test-token",
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
	values := map[string]string{
		"COMMITARIUM_WORKER_DATABASE_PATH":  "/state/worker.db",
		"COMMITARIUM_WORKER_TOKEN":          "test-token",
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
	tests := []map[string]string{
		{"COMMITARIUM_WORKER_TOKEN": "test-token"},
		{"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db"},
		{
			"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db",
			"COMMITARIUM_WORKER_TOKEN":         "token with spaces",
		},
		{
			"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db",
			"COMMITARIUM_WORKER_TOKEN":         "test-token",
			"COMMITARIUM_WORKER_STEP_DELAY":    "not-a-duration",
		},
		{
			"COMMITARIUM_WORKER_DATABASE_PATH": "/state/worker.db",
			"COMMITARIUM_WORKER_TOKEN":         "test-token",
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
}

func TestLoadConfigBuildsCodexRuntimeSettings(t *testing.T) {
	values := validCodexConfig(t.TempDir())
	loaded, err := loadConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("load Codex worker config: %v", err)
	}
	if loaded.adapter != "codex" ||
		loaded.codex.profileID != "profile_test" ||
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
		Role:           workerhttp.RoleReviewer,
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
	if !slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityForceStop) ||
		slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityPause) ||
		slices.Contains(workerRuntime.capabilities, workerhttp.CapabilityContinue) {
		t.Fatalf("Codex capabilities = %v", workerRuntime.capabilities)
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
	return map[string]string{
		"COMMITARIUM_WORKER_DATABASE_PATH":      "/state/worker.db",
		"COMMITARIUM_WORKER_TOKEN":              "test-token",
		"COMMITARIUM_WORKER_ADAPTER":            "codex",
		"COMMITARIUM_CODEX_PROVIDER_STATE_PATH": filepath.Dir(workspace),
		"COMMITARIUM_CODEX_WORKSPACE_ROOT":      filepath.Dir(workspace),
		"COMMITARIUM_CODEX_PROFILE_ID":          "profile_test",
		"COMMITARIUM_CODEX_SANDBOX":             "danger-full-access",
	}
}
