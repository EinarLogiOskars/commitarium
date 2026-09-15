package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequirementUsesNarrowAllowlistAndExactVersions(t *testing.T) {
	tool, version, err := parseRequirement("Python@3.13.7")
	if err != nil || tool != "python" || version != "3.13.7" {
		t.Fatalf("parse requirement = %q %q err=%v", tool, version, err)
	}
	tool, version, err = parseRequirement("java@temurin-25.0.4+7.0.LTS")
	if err != nil || tool != "java" || version != "temurin-25.0.4+7.0.LTS" {
		t.Fatalf("parse Java requirement = %q %q err=%v", tool, version, err)
	}
	tool, version, err = parseRequirement("java@temurin-21.0.12+8.0.LTS")
	if err != nil || tool != "java" || version != "temurin-21.0.12+8.0.LTS" {
		t.Fatalf("parse Java 21 requirement = %q %q err=%v", tool, version, err)
	}
	for _, value := range []string{"gradle@9.7.1", "maven@3.9.16"} {
		if _, _, err := parseRequirement(value); err != nil {
			t.Fatalf("expected %q to be accepted: %v", value, err)
		}
	}
	for _, value := range []string{"python@latest", "terraform@1.2.3", "python", "node@20;bad"} {
		if _, _, err := parseRequirement(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}

func TestGeneratedConfigRoundTripRejectsExecutableSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mise.toml")
	if err := writeGeneratedConfig(path, map[string]string{"python": "3.13.7", "node": "24.8.0"}); err != nil {
		t.Fatalf("write config: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(contents), "node = \"24.8.0\"") {
		t.Fatalf("unexpected config %q err=%v", contents, err)
	}
	tools, err := readGeneratedConfig(path)
	if err != nil || tools["python"] != "3.13.7" || tools["node"] != "24.8.0" {
		t.Fatalf("round trip tools=%v err=%v", tools, err)
	}
	if err := os.WriteFile(path, []byte("[tools]\npython = \"3.13.7\"\n[env]\nTOKEN = \"bad\"\n"), 0o600); err != nil {
		t.Fatalf("write unsafe config: %v", err)
	}
	if _, err := readGeneratedConfig(path); err == nil {
		t.Fatal("expected executable config section to be rejected")
	}
}
