package httpapi

import (
	"errors"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

func TestProjectSetupSavesTheStackAndStartsAVisibleScaffold(t *testing.T) {
	api, features, starter := previewSetupAPI([]project.RepositoryTreeEntry{{Path: "README.md", Type: "blob"}}, nil)
	toolchains := &recordingToolchainService{}
	api.toolchains = toolchains
	started, _, created, err := api.setUpProject(t.Context(), "prj_test", []string{"node-lts", "python"}, "React with Vite; FastAPI")
	if err != nil || !created || started.ID != "fea_setup" {
		t.Fatalf("setup = %+v created=%t err=%v", started, created, err)
	}
	if toolchains.configured.Tools["node"] == "" || toolchains.configured.Tools["python"] == "" ||
		toolchains.configured.Source != toolchain.SourcePicker {
		t.Fatalf("stack = %+v", toolchains.configured)
	}
	if features.receivedOverrides.Background || features.receivedTitle != projectSetupTitle ||
		!strings.Contains(features.receivedDescription, "React with Vite; FastAPI") ||
		!strings.Contains(features.receivedDescription, "x-commitarium-preview") {
		t.Fatalf("setup work order = %+v %q", features.receivedOverrides, features.receivedDescription)
	}
	if starter.featureID != "fea_setup" {
		t.Fatalf("the setup did not start: %+v", starter)
	}
}

func TestProjectSetupRefusesCodeAndUnknownRuntimes(t *testing.T) {
	api, _, _ := previewSetupAPI([]project.RepositoryTreeEntry{{Path: "package.json", Type: "blob"}}, nil)
	api.toolchains = &recordingToolchainService{}
	if _, _, _, err := api.setUpProject(t.Context(), "prj_test", []string{"node-lts"}, ""); !errors.Is(err, errProjectHasCode) {
		t.Fatalf("a repository with code was set up again: %v", err)
	}
	api, _, _ = previewSetupAPI(nil, nil)
	api.toolchains = &recordingToolchainService{}
	if _, _, _, err := api.setUpProject(t.Context(), "prj_test", []string{"cobol"}, ""); !errors.Is(err, errInvalidStack) {
		t.Fatalf("an unknown runtime was accepted: %v", err)
	}
	if _, _, _, err := api.setUpProject(t.Context(), "prj_test", nil, ""); !errors.Is(err, errSetupNeedsStack) {
		t.Fatalf("a setup without a runtime was accepted: %v", err)
	}
}

func TestProjectSetupAnswersTheRunningSetup(t *testing.T) {
	running := feature.Feature{ID: "fea_running", ProjectID: "prj_test", Title: projectSetupTitle, State: feature.StateImplementing}
	api, features, _ := previewSetupAPI(nil, []feature.Feature{running})
	api.toolchains = &recordingToolchainService{}
	started, _, created, err := api.setUpProject(t.Context(), "prj_test", []string{"go"}, "")
	if err != nil || created || started.ID != "fea_running" || features.createCalls != 0 {
		t.Fatalf("second setup = %+v created=%t err=%v", started, created, err)
	}
}
