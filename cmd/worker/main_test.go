package main

import (
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

func TestNormalizeSimulatedEventPreservesRecoveryAssessment(t *testing.T) {
	normalized, err := normalizeSimulatedEvent(t.Context(), worker.Event{
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
	if _, err := normalizeSimulatedEvent(t.Context(), worker.Event{Type: "unknown", Text: "bad"}); err == nil {
		t.Fatal("expected unknown simulated event type to fail")
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
