package conversations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
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
// Each work order says whose it is, and the project how the assistant works.
func TestAWorkOrderWithoutAgentsSaysSo(t *testing.T) {
	describer := StateDescriber{
		Projects: projectsStub{}, Agents: agentsStub{},
		Features: &featuresStub{features: []feature.Feature{
			{ID: "fea_new", Title: "New", State: feature.StateDraft},
			{ID: "fea_set", Title: "Set", State: feature.StateReady, CreatedBy: feature.CreatedByAssistant,
				AgentProviders: project.AgentProviders{Lead: project.AgentProviderCodex, Reviewer: project.AgentProviderClaude,
					LeadAgent: "codex", ReviewerAgent: "claude"},
				AgentModels: project.AgentModels{Lead: "gpt-6-luna", Reviewer: "claude-sonnet-5-5"}},
		}},
	}
	state, err := describer.Describe(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state, `"New" (fea_new, the user's): draft, steps full, agents not chosen yet`) ||
		!strings.Contains(state, `"Set" (fea_set, yours): ready`) ||
		!strings.Contains(state, "How you work here: you assist.") ||
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

// The state rates each offered model by the model guide, with its efforts,
// so the assistant chooses by tier (ADR-031).
func TestTheStateRatesOfferedModels(t *testing.T) {
	guide, err := modelguide.NewService(&guideStore{overrides: map[string]modelguide.Override{
		"claude-opus-5": {ModelID: "claude-opus-5", Tier: modelguide.TierFrontier, OfficialTier: modelguide.TierAvoid},
	}})
	if err != nil {
		t.Fatal(err)
	}
	describer := StateDescriber{
		Projects: projectsStub{}, Agents: agentsStub{}, Features: &featuresStub{}, Guide: guide,
		Models: func(context.Context) []ModelChoice {
			return []ModelChoice{
				{Provider: project.AgentProviderCodex, ID: "gpt-6.1-sol", Display: "Sol", Efforts: []string{"low", "high"}},
				{Provider: project.AgentProviderClaude, ID: "claude-opus-5", Display: "Opus 5"},
				{Provider: project.AgentProviderClaude, ID: "claude-new-9", Display: "New"},
			}
		},
	}
	state, err := describer.Describe(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Model guide (as of ", "- gpt-6.1-sol: strong; ", "efforts: low, high",
		"- claude-opus-5: frontier (the user's rating)", "- claude-new-9: unrated (needs approval)",
	} {
		if !strings.Contains(state, want) {
			t.Errorf("state lacks %q:\n%s", want, state)
		}
	}
}

type blockersStub struct{}

func (blockersStub) RunBlocker(context.Context, string) (execution.Blocker, bool, error) {
	return execution.Blocker{Kind: execution.BlockerAgent, Check: "verify lead publication before review",
		Detail: "the agent's audit comment with its marker is not on the Forgejo pull request", Fingerprint: "fp1"}, true, nil
}

func (blockersStub) RunRecoveries(context.Context, string) ([]execution.Recovery, error) {
	return []execution.Recovery{
		{How: execution.RecoveryContinue, Fingerprint: "fp1", FromAssistant: true, Guidance: "git fetch origin"},
		{How: execution.RecoveryRecheck, Fingerprint: "fp0"},
	}, nil
}

func (blockersStub) LatestAgentMessages(context.Context, string, int) ([]execution.AgentMessage, error) {
	return []execution.AgentMessage{{Role: "lead", Text: "Pushed 056d768; the comment API returned 404."}}, nil
}

// A blocked work order shows the assistant the whole blocker: what failed,
// the agents' last words, and what was tried on the same failure (ADR-032).
func TestABlockedRunShowsTheWholeBlocker(t *testing.T) {
	describer := StateDescriber{
		Projects: projectsStub{}, Agents: agentsStub{}, Blockers: blockersStub{},
		Features: &featuresStub{features: []feature.Feature{
			{ID: "fea_audio", Title: "Add audio", State: feature.StateImplementing, CreatedBy: feature.CreatedByAssistant},
		}},
		Runs: runsStub{runs: map[string][]execution.Run{"fea_audio": {{
			ID: "run_1", Status: execution.RunStatusWaitingForUser, WaitKind: execution.RunWaitKindBlocker,
			Reason: "The coordinator could not complete the lead turn.",
		}}}},
	}
	state, err := describer.Describe(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Blocker: kind agent, check \"verify lead publication before review\", fingerprint fp1",
		"Detail: the agent's audit comment with its marker is not on the Forgejo pull request",
		"- lead: Pushed 056d768; the comment API returned 404.",
		"- continue by you (on this same failure): git fetch origin",
		"- recheck by the user\n",
	} {
		if !strings.Contains(state+"\n", want) {
			t.Errorf("state lacks %q:\n%s", want, state)
		}
	}
}
