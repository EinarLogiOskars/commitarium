package conversations

import (
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func limitAction(kind featureartifact.AssistantActionType, change func(*featureartifact.AssistantAction)) Action {
	action := featureartifact.AssistantAction{Type: kind, Reason: "Because", Take: true}
	if change != nil {
		change(&action)
	}
	return Action{ID: "act_1", Status: ActionProposed, AssistantAction: action}
}

// The hard limits stop what is never Mayka's to take, whatever her say
// (ADR-041), and say why in plain words; everything else is hers.
func TestHardLimits(t *testing.T) {
	theirs := Limits{Theirs: "Dark mode"}
	for _, test := range []struct {
		name   string
		action Action
		limits Limits
		want   string
	}{
		{"her own start", limitAction(featureartifact.AssistantStartWorkOrder, nil), Limits{}, ""},
		{"a premium model", limitAction(featureartifact.AssistantCreateWorkOrder, nil),
			Limits{Premium: []string{"claude-fable-5-1"}}, "it uses claude-fable-5-1, which needs your approval."},
		{"the user's work order", limitAction(featureartifact.AssistantStartWorkOrder, nil), theirs,
			"“Dark mode” is your work order, so it waits for you."},
		{"the user's work order, asked for in the chat", limitAction(featureartifact.AssistantStartWorkOrder,
			func(action *featureartifact.AssistantAction) { action.Asked = true }), theirs, ""},
		{"closing the user's work order unasked", limitAction(featureartifact.AssistantCloseWorkOrder, nil), theirs,
			"“Dark mode” is your work order, so it waits for you."},
		{"closing the user's work order they asked her to close", limitAction(featureartifact.AssistantCloseWorkOrder,
			func(action *featureartifact.AssistantAction) { action.Asked = true }), theirs, ""},
		{"closing her own work", limitAction(featureartifact.AssistantCloseWorkOrder, nil), Limits{}, ""},
		{"a new app", limitAction(featureartifact.AssistantCreateApp, nil), Limits{},
			"a new app or project waits for your approval."},
		{"a project to plan", limitAction(featureartifact.AssistantPlanProject, nil), Limits{},
			"a new app or project waits for your approval."},
		{"building the plan", limitAction(featureartifact.AssistantBuildPlan, nil), Limits{},
			"building the plan waits for your approval."},
		{"plan mode", limitAction(featureartifact.AssistantStartPlanning, nil), Limits{},
			"starting plan mode waits for your approval."},
		{"a deploy", limitAction(featureartifact.AssistantDeployApp, nil), Limits{Builds: true},
			"changes on the server are applied by the app, not on their own."},
		{"Homelab", limitAction(featureartifact.AssistantSetHomelab, nil), Limits{Builds: true},
			"changes on the server are applied by the app, not on their own."},
		{"back to Ready, asking first", limitAction(featureartifact.AssistantRecoverWorkOrder,
			func(action *featureartifact.AssistantAction) { action.How = "back_to_ready" }), Limits{},
			"sending work back to Ready needs your approval."},
		{"back to Ready, building everything", limitAction(featureartifact.AssistantRecoverWorkOrder,
			func(action *featureartifact.AssistantAction) { action.How = "back_to_ready" }), Limits{Builds: true}, ""},
		{"her say", limitAction(featureartifact.AssistantSetSay, nil), Limits{}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := HardLimit(test.action, test.limits); got != test.want {
				t.Fatalf("HardLimit = %q, want %q", got, test.want)
			}
		})
	}
}

// Asking first, an action she takes applies and one she asks for stays a
// card; a taken one a hard limit stops is left for the user with the reason
// on its card (ADR-041).
func TestTakenActionsApplyAndAskedOnesStayCards(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Projects = gapProjects{mode: project.AssistantModeAssists}
	service.config.Features = &featuresStub{features: []feature.Feature{
		{ID: "fea_users", Title: "Dark mode", State: feature.StateDraft, CreatedBy: feature.CreatedByUser},
	}}
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	card := func(id string, action featureartifact.AssistantAction, premium ...string) Action {
		action.Reason = "Because"
		return Action{ID: id, Status: ActionProposed, AssistantAction: action, Premium: premium}
	}
	stored.Actions = append(stored.Actions,
		card("act_brief", featureartifact.AssistantAction{Type: featureartifact.AssistantCreateWorkOrder, Title: "Sort",
			Steps: "quick", Take: true}),
		card("act_asked", featureartifact.AssistantAction{Type: featureartifact.AssistantCreateWorkOrder, Title: "Filter",
			Steps: "quick"}),
		card("act_premium", featureartifact.AssistantAction{Type: featureartifact.AssistantCreateWorkOrder, Title: "Rewrite",
			Steps: "full", Take: true}, "claude-fable-5-1"),
		card("act_users", featureartifact.AssistantAction{Type: featureartifact.AssistantStartWorkOrder,
			WorkOrderID: "fea_users", Take: true}),
	)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].ID != "act_brief" {
		t.Fatalf("applied %+v err=%v, want only the action she took", apply, err)
	}
	conversation, _ := service.Get(t.Context(), "prj_test")
	left := map[string]string{}
	for _, action := range conversation.Actions {
		if action.ID == "act_asked" && (action.AutoTried || action.LeftForUser) {
			t.Fatalf("the action she asked for = %+v, want an untouched card", action)
		}
		if action.LeftForUser {
			left[action.ID] = action.LeftReason
		}
	}
	if left["act_premium"] != "It uses claude-fable-5-1, which needs your approval." ||
		left["act_users"] != "“Dark mode” is your work order, so it waits for you." || len(left) != 2 {
		t.Fatalf("left for the user = %v", left)
	}
	if last := conversation.Messages[len(conversation.Messages)-1]; !strings.HasPrefix(last.Text, "Mayka left") {
		t.Fatalf("last note = %q", last.Text)
	}
}

// Her turn's say is always taken, and a server card never: the user's app
// applies those it was asked for.
func TestATurnTakesItsSayAndNeverAServerCard(t *testing.T) {
	service, worker := newTestService(t)
	service.config.Features = &featuresStub{}
	if _, err := service.Send(t.Context(), "prj_test", "Just build it all, but never deploy", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Noted.",
		AssistantTurn: &featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{
			{Type: featureartifact.AssistantSetSay, Reason: "The user said so", Level: featureartifact.SayBuildEverything,
				Words: "Never deploy."},
			{Type: featureartifact.AssistantSetHomelab, Reason: "Asked", How: "on", Take: true},
		}},
	})
	conversation, err := service.Get(t.Context(), "prj_test")
	if err != nil || len(conversation.Actions) != 2 {
		t.Fatalf("actions = %+v err=%v", conversation.Actions, err)
	}
	if !conversation.Actions[0].Take || conversation.Actions[1].Take {
		t.Fatalf("take = %t, %t; want the say taken and the server card asked for",
			conversation.Actions[0].Take, conversation.Actions[1].Take)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].Type != featureartifact.AssistantSetSay {
		t.Fatalf("applied %+v err=%v, want the say", apply, err)
	}
}

// Her words are kept with the project and shown in her state beside the
// level, which is the project's assistant mode.
func TestHerSayIsKeptAndDescribed(t *testing.T) {
	service, _ := newTestService(t)
	if say := service.SayOf("prj_test"); say.Words != "" {
		t.Fatalf("a new project's say = %+v", say)
	}
	if err := service.SetSay(t.Context(), "prj_test", "  Advise only, change nothing.  "); err != nil {
		t.Fatal(err)
	}
	if err := service.SetSay(t.Context(), TopLevel, "Anything"); err == nil {
		t.Fatal("the top level kept a say")
	}
	if err := service.SetSay(t.Context(), "prj_test", strings.Repeat("x", featureartifact.MaxSayWordsBytes+1)); err == nil {
		t.Fatal("kept words past the bound")
	}
	say := service.SayOf("prj_test")
	if say.Words != "Advise only, change nothing." || say.UpdatedAt.IsZero() {
		t.Fatalf("say = %+v", say)
	}
	// A chat's write keeps it.
	if _, err := service.Send(t.Context(), "prj_test", "Hello", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if conversation, _ := service.Get(t.Context(), "prj_test"); conversation.Say == nil ||
		conversation.Say.Words != "Advise only, change nothing." {
		t.Fatalf("the conversation's say = %+v", conversation.Say)
	}
	for _, mode := range []project.AssistantMode{project.AssistantModeAssists, project.AssistantModeBuilds} {
		describer := StateDescriber{Projects: modeProjectsStub{mode: mode}, Agents: agentsStub{}, Say: service.SayOf}
		state, err := describer.Describe(t.Context(), "prj_test")
		if err != nil {
			t.Fatal(err)
		}
		want := "How much you do here (your say): Ask first. Your words: Advise only, change nothing."
		if mode == project.AssistantModeBuilds {
			want = "How much you do here (your say): Build everything. Your words: Advise only, change nothing."
		}
		if !strings.Contains(state, want) {
			t.Fatalf("%s: state = %s", mode, state)
		}
	}
}
