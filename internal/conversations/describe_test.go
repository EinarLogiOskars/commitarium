package conversations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type threeAgents struct{}

func (threeAgents) List(context.Context) ([]agent.Agent, error) {
	return []agent.Agent{
		{ID: "claude-max", Name: "Claude Max", Provider: project.AgentProviderClaude},
		{ID: "codex", Name: "Codex", Provider: project.AgentProviderCodex},
		{ID: "codex-work", Name: "Codex (work)", Provider: project.AgentProviderCodex},
	}, nil
}

// The assistant chooses agents and models itself (ADR-027), so its state
// lists each agent's models and how much usage it has left.
func TestTheStateListsEachAgentsUsageAndModels(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	used, full := 35.0, 100.0
	resets, later := now.Add(3*time.Hour), now.Add(48*time.Hour)
	describer := StateDescriber{
		Projects: projectsStub{}, Agents: threeAgents{},
		Models: func(context.Context) []ModelChoice {
			return []ModelChoice{
				{Provider: project.AgentProviderClaude, ID: "claude-sonnet-5-5", Display: "Sonnet 5.5"},
				{Provider: project.AgentProviderCodex, ID: "gpt-6-luna", Display: "Luna"},
			}
		},
		Usage: func(context.Context) []agent.Usage {
			return []agent.Usage{
				{Agent: "claude-max", ObservedAt: &now, Windows: []workerhttp.UsageWindow{
					{Name: "5h", UsedPercent: &used, ResetsAt: &resets},
				}},
				{Agent: "codex", ObservedAt: &now, Windows: []workerhttp.UsageWindow{
					{Name: "weekly", UsedPercent: &full, ResetsAt: &later, Limited: true},
				}},
			}
		},
		Settings: func(context.Context) (Settings, error) {
			return Settings{Agent: "claude-max", Model: "claude-sonnet-5-5"}, nil
		},
		Now: func() time.Time { return now },
	}
	state, err := describer.Describe(t.Context(), TopLevel)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"agent claude-max (Claude Max, claude): usage 65% left (5h 35% used, resets Oct 13 15:00 UTC)",
		"models: claude-sonnet-5-5 (Sonnet 5.5)",
		"agent codex (Codex, codex): at its usage limit until Oct 15 12:00 UTC, do not choose it; models: gpt-6-luna (Luna)",
		"agent codex-work (Codex (work), codex): not connected right now",
		"a lead and a reviewer of different providers",
		"You, the assistant, run on agent claude-max with claude-sonnet-5-5",
	} {
		if !strings.Contains(state, want) {
			t.Errorf("the state lacks %q:\n%s", want, state)
		}
	}

	describer.Agents = noAgents{}
	state, err = describer.Describe(t.Context(), TopLevel)
	if err != nil || !strings.Contains(state, "the user has not added an agent yet") {
		t.Fatalf("the state without agents = %q err=%v", state, err)
	}
}

// A project lends its work orders no agents; one without a choice says so.
func TestAWorkOrderWithoutAgentsSaysSo(t *testing.T) {
	describer := StateDescriber{
		Projects: projectsStub{}, Agents: agentsStub{},
		Features: &featuresStub{features: []feature.Feature{
			{ID: "fea_new", Title: "New", State: feature.StateDraft},
			{ID: "fea_set", Title: "Set", State: feature.StateReady,
				AgentProviders: project.AgentProviders{Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude,
					LeadAgent: "codex", ReviewerAgent: "claude"},
				AgentModels: project.AgentModels{Lead: "gpt-6-luna", Reviewer: "claude-sonnet-5-5"}},
		}},
	}
	state, err := describer.Describe(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state, `"New" (fea_new): draft, steps full, agents not chosen yet`) ||
		!strings.Contains(state, "lead codex on gpt-6-luna, reviewer claude on claude-sonnet-5-5") ||
		strings.Contains(state, "Defaults: lead") {
		t.Fatalf("work orders in the state:\n%s", state)
	}
}

// A refused autopilot action wakes the assistant with the reason, at most
// twice in a row, so it can choose again without spending turns forever.
func TestARefusedAutopilotActionWakesTheAssistantAtMostTwice(t *testing.T) {
	service, _ := newTestService(t)
	action := Action{ID: "act_1", AssistantAction: featureartifact.AssistantAction{
		Type: featureartifact.AssistantCreateWorkOrder, Title: "Show the log",
	}}
	refused := errors.New(`agent "codex" has reached its usage limit`)
	for range 3 {
		if err := service.autopilotOutcome("prj_test", action, refused); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := service.read("prj_test")
	if err != nil || stored.Refusals != 3 || len(stored.PendingEvents) != 2 ||
		!strings.Contains(stored.PendingEvents[0], "usage limit") ||
		!strings.Contains(stored.Messages[len(stored.Messages)-1].Text, "waits for your approval") {
		t.Fatalf("after three refusals = %+v err=%v", stored, err)
	}
	if err := service.autopilotOutcome("prj_test", action, nil); err != nil {
		t.Fatal(err)
	}
	if stored, _ := service.read("prj_test"); stored.Refusals != 0 {
		t.Fatalf("an applied action did not reset the refusals: %d", stored.Refusals)
	}
}
