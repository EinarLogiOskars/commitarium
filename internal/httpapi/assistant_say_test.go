package httpapi

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/conversations"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// set_say is applied at once: its level goes through the same path as the
// Project settings switcher, and her words are kept with the project
// (ADR-041).
func TestHerSayIsAppliedAtOnce(t *testing.T) {
	stub := &conversationStub{action: conversations.Action{
		ID: "act_1", Status: conversations.ActionProposed,
		AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantSetSay, Reason: "The user said to just go", Take: true,
			Level: featureartifact.SayBuildEverything, Words: "Deploy after each change.",
		},
	}}
	projects := &assistantModeProjects{recordingProjectService: &recordingProjectService{
		getByIDResult: project.Project{ID: "prj_test", Name: "Sleep Log"},
	}}
	handler := NewWithWorkspaceRealWorkflowDeletionAndModels(projects, &recordingFeatureService{}, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, stub, testAgents())
	if err := AssistantActionApplier(handler)(t.Context(), "prj_test", "act_1"); err != nil {
		t.Fatal(err)
	}
	if projects.received != project.AssistantModeBuilds || stub.say != "Deploy after each change." ||
		stub.resolvedStatus != conversations.ActionApplied ||
		stub.resolvedResult != "Mayka builds everything here. Her words: Deploy after each change." {
		t.Fatalf("mode %q, say %q, resolved %s %q", projects.received, stub.say, stub.resolvedStatus, stub.resolvedResult)
	}

	// Words alone keep the level.
	projects.received, stub.resolvedStatus = "", ""
	stub.action.Status, stub.action.Level, stub.action.Words = conversations.ActionProposed, "", "Advise only."
	if err := AssistantActionApplier(handler)(t.Context(), "prj_test", "act_1"); err != nil {
		t.Fatal(err)
	}
	if projects.received != "" || stub.say != "Advise only." || !strings.HasPrefix(stub.resolvedResult, "Noted: Advise only.") {
		t.Fatalf("mode %q, say %q, result %q", projects.received, stub.say, stub.resolvedResult)
	}
}
