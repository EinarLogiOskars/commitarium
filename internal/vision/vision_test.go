package vision

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type workerStub struct {
	attempt workerhttp.Attempt
	puts    []workerhttp.PutAttemptRequest
}

func (worker *workerStub) PutAttempt(_ context.Context, identity workerhttp.MutationIdentity, request workerhttp.PutAttemptRequest) (workerhttp.Attempt, bool, error) {
	worker.puts = append(worker.puts, request)
	now := time.Now().UTC()
	worker.attempt = workerhttp.Attempt{
		AttemptReference: identity.AttemptReference, Mode: request.Mode, Assignment: request.Assignment,
		ProviderSessionID: "thread", State: workerhttp.AttemptStateRunning, StartedAt: now, UpdatedAt: now,
	}
	return worker.attempt, true, nil
}

func (worker *workerStub) GetAttempt(context.Context, workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	return worker.attempt, nil
}

func (*workerStub) SendCommand(context.Context, workerhttp.MutationIdentity, workerhttp.CommandRequest) (workerhttp.Attempt, error) {
	panic("unexpected command")
}

func (*workerStub) ForceStop(context.Context, workerhttp.MutationIdentity, workerhttp.ForceStopRequest) (workerhttp.Attempt, error) {
	panic("unexpected force stop")
}

func (*workerStub) Supersede(context.Context, workerhttp.MutationIdentity, workerhttp.SupersedeRequest) (workerhttp.Attempt, error) {
	panic("unexpected supersede")
}

func (worker *workerStub) finish(result workerhttp.TerminalResult) {
	worker.attempt.State = workerhttp.AttemptStateTerminal
	worker.attempt.Result = &result
}

type projectsStub struct{}

func (projectsStub) GetByID(_ context.Context, id string) (project.Project, error) {
	return project.Project{ID: id, Name: "Calories"}, nil
}

func (projectsStub) GetRepositoryOverview(context.Context, string) (project.RepositoryOverview, error) {
	readme := "# Calories\\nTrack meals."
	return project.RepositoryOverview{ReadmeMarkdown: &readme, Tree: []project.RepositoryTreeEntry{{Path: "README.md"}}}, nil
}

func newTestService(t *testing.T) (*Service, *workerStub) {
	t.Helper()
	worker := &workerStub{}
	service, err := NewService(t.TempDir(), t.TempDir(), projectsStub{}, func(string) (workerhttp.Service, error) {
		return worker, nil
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return service, worker
}

func samplePlan() *featureartifact.VisionPlan {
	return &featureartifact.VisionPlan{
		Statement: "A calorie tracker for one person.", Confidence: "low", Guesses: []string{"Metric units"},
		Priorities: []featureartifact.VisionPriority{{Level: 0, Items: []featureartifact.VisionItem{
			{ID: "log-meals", Title: "Log meals", Items: []featureartifact.VisionItem{
				{ID: "add-a-meal", Title: "Add a meal"},
				{ID: "daily-total", Title: "Show the daily total"},
			}},
		}}},
	}
}

func TestTheAssistantWritesTheVisionAfterAQuestion(t *testing.T) {
	service, worker := newTestService(t)
	session, created, err := service.Start(t.Context(), "prj_test", "codex", "gpt-5", ModeConverse, "A calorie tracker", "start-1")
	if err != nil || !created || session.Status != StatusRunning {
		t.Fatalf("start = %+v created=%t err=%v", session, created, err)
	}
	first := worker.puts[0]
	if first.OutputContract != workerhttp.OutputContractVisionPlan || first.Assignment.Role != workerhttp.RoleConsultant ||
		!strings.Contains(first.Instructions, "First ask the user") || !strings.Contains(first.Instructions, "Track meals.") {
		t.Fatalf("unexpected first turn: %+v", first)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired, Summary: "Who is it for?",
	})
	if session, err = service.GetSession(t.Context(), "prj_test", session.ID); err != nil || session.Status != StatusWaitingForUser {
		t.Fatalf("after the question = %+v err=%v", session, err)
	}
	if _, err := service.Get(t.Context(), "prj_test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a vision exists before it was written: %v", err)
	}
	if session, _, err = service.Reply(t.Context(), "prj_test", session.ID, "Just me.", "reply-1"); err != nil || session.Status != StatusRunning {
		t.Fatalf("reply = %+v err=%v", session, err)
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Here is the plan.", VisionPlan: samplePlan(),
	})
	if session, err = service.GetSession(t.Context(), "prj_test", session.ID); err != nil || session.Status != StatusWritten {
		t.Fatalf("after the proposal = %+v err=%v", session, err)
	}
	stored, err := service.Get(t.Context(), "prj_test")
	if err != nil || stored.Revision != 1 || stored.Source != "assistant" || stored.Plan.Confidence != "low" {
		t.Fatalf("stored vision = %+v err=%v", stored, err)
	}
}

func TestWorkOrdersLinkToLeafItemsAndSurviveARewrite(t *testing.T) {
	service, worker := newTestService(t)
	session, _, _ := service.Start(t.Context(), "prj_test", "codex", "gpt-5", ModeDescribe, "A calorie tracker", "start-1")
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Done.", VisionPlan: samplePlan(),
	})
	if _, err := service.GetSession(t.Context(), "prj_test", session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LinkWorkOrder(t.Context(), "prj_test", "log-meals", "fea_group"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a grouping item started a work order: %v", err)
	}
	linked, err := service.LinkWorkOrder(t.Context(), "prj_test", "add-a-meal", "fea_one")
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if item, label, _ := Find(linked.Plan, "add-a-meal"); item.FeatureID != "fea_one" || label != "P0.1.1" {
		t.Fatalf("linked item = %+v %q", item, label)
	}
	if _, label, _ := Find(linked.Plan, "daily-total"); label != "P0.1.2" {
		t.Fatalf("second label = %q", label)
	}

	rewrite, _, _ := service.Start(t.Context(), "prj_test", "codex", "gpt-5", ModeDescribe, "Add exercise too", "start-2")
	if !strings.Contains(worker.puts[len(worker.puts)-1].Instructions, "current vision") {
		t.Fatal("a rewrite did not see the current vision")
	}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Updated.", VisionPlan: samplePlan(),
	})
	if _, err := service.GetSession(t.Context(), "prj_test", rewrite.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ := service.Get(t.Context(), "prj_test")
	if item, _, _ := Find(stored.Plan, "add-a-meal"); item.FeatureID != "fea_one" || stored.Revision != 3 {
		t.Fatalf("the rewrite lost the work order: %+v revision=%d", item, stored.Revision)
	}
	if _, err := service.Save(t.Context(), "prj_test", stored.Plan, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale edit was saved: %v", err)
	}
	stored.Plan.Statement = "A calorie and exercise tracker."
	if saved, err := service.Save(t.Context(), "prj_test", stored.Plan, 3); err != nil || saved.Source != "user" || saved.Revision != 4 {
		t.Fatalf("edit = %+v err=%v", saved, err)
	}
}

type checkoutsStub struct {
	ensured []workspace.CheckoutSpec
	removed []string
}

func (stub *checkoutsStub) Ensure(_ context.Context, spec workspace.CheckoutSpec) error {
	stub.ensured = append(stub.ensured, spec)
	return nil
}

func (stub *checkoutsStub) Remove(_ context.Context, id string) error {
	stub.removed = append(stub.removed, id)
	return nil
}

type branchesStub struct{}

func (branchesStub) GetBranch(_ context.Context, _, _, branch string) (workspace.Branch, error) {
	return workspace.Branch{Name: branch, CommitID: strings.Repeat("a", 40)}, nil
}

type repositoryProjectsStub struct{ projectsStub }

func (repositoryProjectsStub) GetByID(_ context.Context, id string) (project.Project, error) {
	return project.Project{ID: id, Name: "Calories", ForgejoRepository: &project.ForgejoRepository{
		Owner: "commitarium", Name: "calories", DefaultBranch: "main",
	}}, nil
}

func TestTheAssistantInspectsACheckoutAndMarksDeliveredItemsDone(t *testing.T) {
	worker := &workerStub{}
	checkouts := &checkoutsStub{}
	service, err := NewService(t.TempDir(), t.TempDir(), repositoryProjectsStub{}, func(string) (workerhttp.Service, error) {
		return worker, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	service.WithRepository(checkouts, branchesStub{})
	session, _, err := service.Start(t.Context(), "prj_test", "codex", "gpt-5", ModeDescribe, "A calorie tracker", "start-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(checkouts.ensured) != 1 || checkouts.ensured[0].WorkspaceID != session.ID ||
		checkouts.ensured[0].Branch != "main" || checkouts.ensured[0].BaseCommitID != strings.Repeat("a", 40) {
		t.Fatalf("checkout = %+v", checkouts.ensured)
	}
	first := worker.puts[0]
	if first.WorkspaceAccess != workerhttp.WorkspaceAccessReadOnly || !strings.Contains(first.Instructions, "checked out read-only") {
		t.Fatalf("the assistant cannot inspect the code: %+v", first)
	}
	plan := samplePlan()
	plan.Priorities[0].Items[0].Items[0].Done = true
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Done.", VisionPlan: plan,
	})
	if _, err := service.GetSession(t.Context(), "prj_test", session.ID); err != nil {
		t.Fatal(err)
	}
	if len(checkouts.removed) != 1 || checkouts.removed[0] != session.ID {
		t.Fatalf("the finished session kept its checkout: %v", checkouts.removed)
	}
	stored, _ := service.Get(t.Context(), "prj_test")
	if item, _, _ := Find(stored.Plan, "add-a-meal"); !item.Done {
		t.Fatalf("the delivered item is not done: %+v", item)
	}
}

func TestANewProjectGetsAStackRecommendation(t *testing.T) {
	service, worker := newTestService(t)
	session, _, err := service.Start(t.Context(), "prj_test", "codex", "gpt-5", ModeDescribe, "A calorie tracker", "start-1")
	if err != nil {
		t.Fatal(err)
	}
	if instructions := worker.puts[0].Instructions; !strings.Contains(instructions, "no code yet") ||
		!strings.Contains(instructions, "node-lts") {
		t.Fatalf("an empty repository got no stack question: %s", instructions)
	}
	plan := samplePlan()
	plan.Stack = &featureartifact.VisionStack{Presets: []string{"node-lts", "cobol"}, Frameworks: "React with Vite", Reason: "A small web app."}
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Done.", VisionPlan: plan,
	})
	if _, err := service.GetSession(t.Context(), "prj_test", session.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ := service.Get(t.Context(), "prj_test")
	if stored.Plan.Stack == nil || len(stored.Plan.Stack.Presets) != 1 || stored.Plan.Stack.Presets[0] != "node-lts" {
		t.Fatalf("stack = %+v", stored.Plan.Stack)
	}
}

func TestTheOpenConversationIsFoundWithoutItsID(t *testing.T) {
	service, worker := newTestService(t)
	if _, err := service.OpenSession(t.Context(), "prj_test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an open conversation before any: %v", err)
	}
	session, _, _ := service.Start(t.Context(), "prj_test", "codex", "gpt-5", ModeConverse, "A calorie tracker", "start-1")
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionInputRequired, Summary: "Who is it for?",
	})
	open, err := service.OpenSession(t.Context(), "prj_test")
	if err != nil || open.ID != session.ID || open.Status != StatusWaitingForUser {
		t.Fatalf("open conversation = %+v err=%v", open, err)
	}
	if _, err := service.OpenSession(t.Context(), "prj_other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project's conversation was found: %v", err)
	}
}

func TestWorkOutsideThePlanCanJoinTheVision(t *testing.T) {
	service, worker := newTestService(t)
	session, _, _ := service.Start(t.Context(), "prj_test", "codex", "gpt-5", ModeDescribe, "A calorie tracker", "start-1")
	worker.finish(workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Done.", VisionPlan: samplePlan(),
	})
	if _, err := service.GetSession(t.Context(), "prj_test", session.ID); err != nil {
		t.Fatal(err)
	}
	stored, itemID, err := service.AddItem(t.Context(), "prj_test", 2, "Export to CSV", "Download the log.", "fea_export", "user")
	if err != nil {
		t.Fatalf("add item: %v", err)
	}
	item, label, found := Find(stored.Plan, itemID)
	if !found || label != "P2.1" || item.FeatureID != "fea_export" || stored.Source != "user" {
		t.Fatalf("item = %+v %q found=%t source=%s", item, label, found, stored.Source)
	}
	again, secondID, err := service.AddItem(t.Context(), "prj_test", 0, "Export to CSV", "", "", "assistant")
	if err != nil || secondID == itemID {
		t.Fatalf("second item = %q err=%v", secondID, err)
	}
	if _, label, _ := Find(again.Plan, secondID); label != "P0.2" {
		t.Fatalf("second label = %q", label)
	}
	if _, _, err := service.AddItem(t.Context(), "prj_test", 12, "Too low", "", "", "user"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an out-of-range priority was accepted: %v", err)
	}
}
