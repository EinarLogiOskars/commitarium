package httpapi

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/workflow"
)

func previewSetupAPI(tree []project.RepositoryTreeEntry, existing []feature.Feature) (*API, *recordingFeatureService, *recordingRunStarter) {
	created := feature.Feature{
		ID: "fea_setup", ProjectID: "prj_test", Title: previewSetupTitle, Description: "rules",
		State: feature.StateDraft, Background: true, AgentProviders: project.DefaultAgentProviders(),
		MergePolicy: project.MergePolicyAutoAfterGates, AutonomyPolicy: project.AutonomyPolicyRunToCompletion,
	}
	features := &recordingFeatureService{createResult: created, getResult: created, listResult: existing}
	starter := &recordingRunStarter{}
	workflowService := firstBriefWorkflowService{&artifactWorkflowService{
		recordingWorkflowService: &recordingWorkflowService{}, artifactErr: workflow.ErrArtifactNotFound,
	}}
	return &API{
		projects: &recordingProjectService{overviewResult: project.RepositoryOverview{Tree: tree}},
		features: features, workflow: workflowService, artifacts: workflowService,
		starter: starter, realWorkflow: &planningStarterStub{},
		// Background work runs on the assistant's agent (ADR-027).
		conversations: &conversationStub{settings: conversations.Settings{Agent: "codex", Model: "gpt-6-luna"}},
		agents:        testAgents(),
	}, features, starter
}

func TestPreviewSetupStartsABackgroundQuickChangeWithoutACompose(t *testing.T) {
	api, features, starter := previewSetupAPI(
		[]project.RepositoryTreeEntry{{Path: "package.json", Type: "blob"}}, nil,
	)
	started, err := api.ensurePreviewSetup(t.Context(), "prj_test")
	if err != nil || started == nil || started.ID != "fea_setup" {
		t.Fatalf("setup = %+v err=%v", started, err)
	}
	overrides := features.receivedOverrides
	if !overrides.Background || overrides.Steps == nil || !overrides.Steps.SkipPlan || !overrides.Steps.SkipReviewer ||
		overrides.MergePolicy == nil || *overrides.MergePolicy != project.MergePolicyAutoAfterGates ||
		!strings.Contains(features.receivedDescription, "x-commitarium-preview") ||
		!strings.Contains(features.receivedDescription, "live reload") {
		t.Fatalf("unexpected background work order: %+v %q", overrides, features.receivedDescription)
	}
	if starter.featureID != "fea_setup" || starter.mergePolicy != project.MergePolicyAutoAfterGates {
		t.Fatalf("the setup did not start: %+v", starter)
	}
	if overrides.AgentProviders == nil || overrides.AgentProviders.LeadAgent != "codex" ||
		overrides.AgentModels == nil || overrides.AgentModels.Lead != "gpt-6-luna" {
		t.Fatalf("the setup does not run on the assistant's agent: %+v", overrides)
	}
}

// Without an agent for the assistant, background work says what to set up
// and creates nothing (ADR-027).
func TestPreviewSetupWaitsForTheAssistantsAgent(t *testing.T) {
	api, features, _ := previewSetupAPI([]project.RepositoryTreeEntry{{Path: "package.json", Type: "blob"}}, nil)
	api.conversations = &conversationStub{}
	if _, err := api.ensurePreviewSetup(t.Context(), "prj_test"); err != errAssistantNotSetUp || features.createCalls != 0 {
		t.Fatalf("setup without the assistant's agent: err=%v creates=%d", err, features.createCalls)
	}
	api.agents = &recordingAgentService{agents: map[string]agent.Agent{}}
	if _, err := api.ensurePreviewSetup(t.Context(), "prj_test"); err != errNoAgents || features.createCalls != 0 {
		t.Fatalf("setup without agents: err=%v creates=%d", err, features.createCalls)
	}
}

func TestPreviewSetupLeavesAnExistingComposeAndARunningSetup(t *testing.T) {
	api, features, _ := previewSetupAPI([]project.RepositoryTreeEntry{{Path: "compose.yaml", Type: "blob"}}, nil)
	if _, err := api.ensurePreviewSetup(t.Context(), "prj_test"); !errors.Is(err, errPreviewHasCompose) || features.createCalls != 0 {
		t.Fatalf("a compose file was set up again: err=%v creates=%d", err, features.createCalls)
	}
	running := feature.Feature{ID: "fea_running", Background: true, State: feature.StateImplementing}
	api, features, _ = previewSetupAPI(nil, []feature.Feature{running})
	if active, err := api.ensurePreviewSetup(t.Context(), "prj_test"); err != nil || active == nil ||
		active.ID != "fea_running" || features.createCalls != 0 {
		t.Fatalf("a second setup started: %+v err=%v creates=%d", active, err, features.createCalls)
	}
}

func TestPreviewFixesStopAfterTheAttemptsSinceTheLastSetup(t *testing.T) {
	now := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	done := func(title string, minutes int) feature.Feature {
		return feature.Feature{
			Title: title, Background: true, State: feature.StateCompleted,
			CreatedAt: now.Add(time.Duration(minutes) * time.Minute),
		}
	}
	history := []feature.Feature{
		done(previewFixTitle, 1), done(previewSetupTitle, 2), done(previewFixTitle, 3),
		{Title: "My feature", State: feature.StateCompleted, CreatedAt: now.Add(4 * time.Minute)},
	}
	if fixes := previewFixesSinceSetup(history); fixes != 1 {
		t.Fatalf("fixes since setup = %d", fixes)
	}
	api, features, _ := previewSetupAPI(nil, append(history, done(previewFixTitle, 5)))
	if _, err := api.fixPreview(t.Context(), "prj_test", "boom"); !errors.Is(err, errPreviewFixesExhausted) || features.createCalls != 0 {
		t.Fatalf("a third fix started: err=%v creates=%d", err, features.createCalls)
	}
	api, features, _ = previewSetupAPI(nil, history)
	if _, err := api.fixPreview(t.Context(), "prj_test", "port 5173 is not published"); err != nil ||
		features.receivedTitle != previewFixTitle || !strings.Contains(features.receivedDescription, "port 5173 is not published") {
		t.Fatalf("the fix did not start: err=%v %q", err, features.receivedDescription)
	}
}

func TestProjectToolchainSavesAConfidentDetection(t *testing.T) {
	toolchains := &recordingToolchainService{
		manifest:   toolchain.Manifest{ProjectID: "prj_test", Status: toolchain.StatusNeedsSetup},
		suggestion: toolchain.Suggestion{Tools: map[string]string{"node": "24.21.0"}, Confidence: "high"},
	}
	api := &API{toolchains: toolchains}
	if _, err := api.projectToolchain(t.Context(), "prj_test"); err != nil {
		t.Fatalf("read toolchain: %v", err)
	}
	if toolchains.configured.Source != toolchain.SourceDetected || toolchains.configured.Tools["node"] != "24.21.0" {
		t.Fatalf("the detected stack was not saved: %+v", toolchains.configured)
	}
	unsure := &recordingToolchainService{
		manifest:   toolchain.Manifest{ProjectID: "prj_test", Status: toolchain.StatusNeedsSetup},
		suggestion: toolchain.Suggestion{Tools: map[string]string{}, Confidence: "low"},
	}
	api = &API{toolchains: unsure}
	if manifest, err := api.projectToolchain(t.Context(), "prj_test"); err != nil ||
		manifest.Status != toolchain.StatusNeedsSetup || unsure.configured.Source != "" {
		t.Fatalf("an unsure detection was saved: %+v %+v err=%v", manifest, unsure.configured, err)
	}
}

func TestPreviewSetupWaitsForTheFirstCode(t *testing.T) {
	api, features, starter := previewSetupAPI([]project.RepositoryTreeEntry{
		{Path: "README.md", Type: "blob"}, {Path: "LICENSE", Type: "blob"}, {Path: ".gitignore", Type: "blob"},
	}, nil)
	started, err := api.ensurePreviewSetup(t.Context(), "prj_test")
	if err != nil || started != nil || features.createCalls != 0 || starter.featureID != "" {
		t.Fatalf("an empty repository started a preview setup: %+v err=%v", started, err)
	}
}
