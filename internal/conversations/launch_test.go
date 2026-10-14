package conversations

import (
	"context"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

type appProjectsStub struct{ projectsStub }

func (appProjectsStub) GetByID(ctx context.Context, id string) (project.Project, error) {
	stored, err := projectsStub{}.GetByID(ctx, id)
	// An app the top-level assistant created builds everything.
	stored.App, stored.AssistantMode = true, project.AssistantModeBuilds
	return stored, err
}

func TestALaunchGoesFromVisionToSetupToBuildingTheWholeVision(t *testing.T) {
	service, _ := newTestService(t)
	visions := &visionStub{}
	features := &featuresStub{}
	service.config.Visions, service.config.Features = visions, features
	var setUp []string
	watcher := Watcher{Service: service, Features: features, Visions: visions,
		SetUp: func(_ context.Context, _ string, presets []string, _ string) error {
			setUp = append(setUp, presets...)
			return nil
		}}

	if _, err := service.StartLaunch(t.Context(), "prj_test"); err != nil {
		t.Fatal(err)
	}
	// No vision yet: nothing happens.
	watcher.Visions = missingVision{}
	watcher.launch(t.Context(), "prj_test")
	if len(setUp) != 0 {
		t.Fatal("set up before the vision was written")
	}

	watcher.Visions = visions
	visions.plan = featureartifact.VisionPlan{
		Stack: &featureartifact.VisionStack{Presets: []string{"node-lts"}, Frameworks: "React"},
		Priorities: []featureartifact.VisionPriority{
			{Level: 0, Items: []featureartifact.VisionItem{{ID: "log", Title: "Log a meal"}}},
			{Level: 1, Items: []featureartifact.VisionItem{{ID: "goal", Title: "Calorie goal"}}},
		},
	}
	watcher.launch(t.Context(), "prj_test")
	if len(setUp) != 1 || setUp[0] != "node-lts" {
		t.Fatalf("stack set up = %v", setUp)
	}
	watcher.launch(t.Context(), "prj_test")
	if len(setUp) != 1 {
		t.Fatal("the stack was set up twice")
	}

	features.features = []feature.Feature{{ID: "fea_setup", Title: projectSetupTitle, State: feature.StateCompleted}}
	watcher.launch(t.Context(), "prj_test")
	conversation, _ := service.Get(t.Context(), "prj_test")
	if conversation.Launch.Stage != LaunchBuilding || conversation.Goal == nil ||
		conversation.Goal.Title != "Finish the vision" || len(conversation.Goal.Items) != 2 {
		t.Fatalf("launch = %+v goal = %+v", conversation.Launch, conversation.Goal)
	}
	if launches, _ := service.ActiveLaunches(t.Context()); len(launches) != 0 {
		t.Fatalf("a built app is still launching: %v", launches)
	}
}

type missingVision struct{}

func (missingVision) Get(context.Context, string) (vision.Vision, error) {
	return vision.Vision{}, vision.ErrNotFound
}

func TestAnAppAppliesTheAssistantsWorkWithoutAGoal(t *testing.T) {
	service, _ := newTestService(t)
	service.config.Projects = appProjectsStub{}
	service.config.Features = &featuresStub{}
	stored, err := service.read("prj_test")
	if err != nil {
		t.Fatal(err)
	}
	stored.Actions = append(stored.Actions,
		Action{ID: "act_change", Status: ActionProposed, AssistantAction: featureartifact.AssistantAction{
			Type: featureartifact.AssistantCreateWorkOrder, Reason: "Asked", Title: "Blue buttons", Steps: "quick"}},
		Action{ID: "act_premium", Status: ActionProposed, Premium: []string{"claude-fable-5-1"},
			AssistantAction: featureartifact.AssistantAction{
				Type: featureartifact.AssistantCreateWorkOrder, Reason: "Asked", Title: "Rewrite", Steps: "full"}},
	)
	if err := service.write(stored); err != nil {
		t.Fatal(err)
	}
	apply, err := service.autopilotActions(t.Context(), "prj_test")
	if err != nil || len(apply) != 1 || apply[0].ID != "act_change" {
		t.Fatalf("apps mode applied %+v err=%v", apply, err)
	}
}
