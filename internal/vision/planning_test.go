package vision

import (
	"errors"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// A vision being planned starts from its goal and fills in as the assistant
// updates it; Build it ends planning with what is still open as guesses.
func TestAVisionIsPlannedThenBuilt(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, _, err := service.StartPlanning(ctx, "prj_shop", " ", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("planning without a goal = %v", err)
	}
	started, fresh, err := service.StartPlanning(ctx, "prj_shop", "Keep two shops' stock in step.", "")
	if err != nil || !fresh || !started.Planning.Open() || started.Plan.Statement != "Keep two shops' stock in step." {
		t.Fatalf("started = %+v, %t, %v", started, fresh, err)
	}
	if again, fresh, err := service.StartPlanning(ctx, "prj_shop", "Something else", ""); err != nil || fresh ||
		again.Plan.Statement != started.Plan.Statement {
		t.Fatalf("started again = %+v, %t, %v", again, fresh, err)
	}
	if _, err := service.FinishPlanning(ctx, "prj_shop"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("building a plan without items = %v", err)
	}

	updated, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		Decisions: []string{" Sync nightly ", ""}, Questions: []string{"Which shop is the source of truth?"},
		Stack: &featureartifact.VisionStack{Presets: []string{"go"}, Frameworks: "Go with SQLite", Reason: "One binary."},
		Items: []featureartifact.PlanItem{
			{Priority: 0, Title: "Read stock from both shops", Description: "Both shops' stock in one place."},
			{Priority: 1, Title: "Report differences"},
			{Priority: 0, Title: "Read stock from both shops"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(updated.Planning.Decisions, "|") != "Sync nightly" || updated.Plan.Confidence != "medium" ||
		updated.Plan.Stack == nil || len(updated.Plan.Priorities) != 2 || len(updated.Plan.Priorities[0].Items) != 2 ||
		updated.Plan.Priorities[0].Items[1].ID == updated.Plan.Priorities[0].Items[0].ID {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		Stack: &featureartifact.VisionStack{Presets: []string{"cobol"}},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown stack = %v", err)
	}
	// An update without items or a goal keeps them.
	kept, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Questions: []string{"How often?"}})
	if err != nil || len(kept.Plan.Priorities) != 2 || kept.Plan.Statement != started.Plan.Statement ||
		len(kept.Planning.Decisions) != 0 {
		t.Fatalf("kept = %+v, %v", kept, err)
	}

	// Build it needs the written plan.
	if _, err := service.FinishPlanning(ctx, "prj_shop"); !errors.Is(err, ErrNoDraft) {
		t.Fatalf("building before a draft = %v", err)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		Questions: []string{"How often?"}, Document: "# Stock sync\n\n- [ ] 1. Read stock from both shops",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.AddResearch(ctx, "prj_shop", "Shopify's stock API",
		"Inventory levels come per location (https://shopify.dev/docs/api).\n\nSources:\n- https://shopify.dev/docs/api/admin\n- https://shopify.dev/changelog."); err != nil {
		t.Fatal(err)
	}
	built, err := service.FinishPlanning(ctx, "prj_shop")
	if err != nil {
		t.Fatal(err)
	}
	if built.Planning.Status != PlanningBuilt || built.Planning.BuiltAt == nil ||
		strings.Join(built.Plan.Guesses, "|") != "How often?" || len(built.Planning.Research) != 1 ||
		strings.Join(built.Planning.Research[0].Sources, " ") != "https://shopify.dev/docs/api/admin https://shopify.dev/changelog" {
		t.Fatalf("built = %+v", built)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{}); !errors.Is(err, ErrNotPlanning) {
		t.Fatalf("updating a built plan = %v", err)
	}
	// Research on a vision no longer being planned is not recorded.
	if err := service.AddResearch(ctx, "prj_shop", "Later", "Found."); err != nil {
		t.Fatal(err)
	}
	if stored, _ := service.Get(ctx, "prj_shop"); len(stored.Planning.Research) != 1 {
		t.Fatalf("research = %+v", stored.Planning.Research)
	}
}

// Stopping planning keeps the plan as it is, and starting again resumes it.
func TestPlanningPausesAndResumes(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, err := service.PausePlanning(ctx, "prj_shop"); err == nil {
		t.Fatal("paused a vision that was never planned")
	}
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Decisions: []string{"Sync nightly"}}); err != nil {
		t.Fatal(err)
	}
	paused, err := service.PausePlanning(ctx, "prj_shop")
	if err != nil || !paused.Planning.Paused() || paused.Planning.Open() {
		t.Fatalf("paused = %+v, %v", paused.Planning, err)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{}); !errors.Is(err, ErrNotPlanning) {
		t.Fatalf("updating paused planning = %v", err)
	}
	if _, err := service.FinishPlanning(ctx, "prj_shop"); !errors.Is(err, ErrNotPlanning) {
		t.Fatalf("building paused planning = %v", err)
	}
	resumed, started, err := service.StartPlanning(ctx, "prj_shop", "", "")
	if err != nil || !started || !resumed.Planning.Open() ||
		strings.Join(resumed.Planning.Decisions, "|") != "Sync nightly" || resumed.Plan.Statement != "Keep stock in step." {
		t.Fatalf("resumed = %+v, %t, %v", resumed, started, err)
	}
}

// The conversation's step and coverage are kept until they change, in any
// order; keys off the checklist are ignored rather than refused.
func TestPlanningMarksWhatTheChecklistCovers(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", ""); err != nil {
		t.Fatal(err)
	}
	moved, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Step: "look", Decisions: []string{"look: dark theme"}})
	if err != nil || moved.Planning.Step != "look" {
		t.Fatalf("moved = %+v, %v", moved.Planning, err)
	}
	kept, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Decisions: []string{"look: dark theme"}})
	if err != nil || kept.Planning.Step != "look" {
		t.Fatalf("kept = %+v, %v", kept.Planning, err)
	}
	covered, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Step: "colours",
		Covered: []string{"stack", "Look", "colours", "purpose", "review"}})
	if err != nil || covered.Planning.Step != "look" || strings.Join(covered.Planning.Covered, ",") != "purpose,look,stack" {
		t.Fatalf("covered = %+v, %v", covered.Planning, err)
	}
	kept, err = service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Step: "review"})
	if err != nil || kept.Planning.Step != "review" || strings.Join(kept.Planning.Covered, ",") != "purpose,look,stack" {
		t.Fatalf("kept coverage = %+v, %v", kept.Planning, err)
	}
}

// The user's edit of a vision being planned keeps its planning.
func TestEditingAPlannedVisionKeepsItsPlanning(t *testing.T) {
	service, _ := newTestService(t)
	started, _, err := service.StartPlanning(t.Context(), "prj_shop", "Keep stock in step.", "")
	if err != nil {
		t.Fatal(err)
	}
	plan := started.Plan
	plan.Statement = "Keep both shops' stock in step."
	saved, err := service.Save(t.Context(), "prj_shop", plan, started.Revision)
	if err != nil || !saved.Planning.Open() {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
}

// Planning belongs to a chat (ADR-039): starting it from another chat moves
// it there, and resuming it from a chat resumes it there.
func TestPlanningMovesWithTheChat(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	started, fresh, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", "main")
	if err != nil || !fresh || started.Planning.Chat != "main" {
		t.Fatalf("started = %+v, %t, %v", started.Planning, fresh, err)
	}
	if again, fresh, err := service.StartPlanning(ctx, "prj_shop", "", "main"); err != nil || fresh || again.Planning.Chat != "main" {
		t.Fatalf("again = %+v, %t, %v", again.Planning, fresh, err)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Decisions: []string{"Sync nightly"}}); err != nil {
		t.Fatal(err)
	}
	moved, fresh, err := service.StartPlanning(ctx, "prj_shop", "", "chat_0123456789abcdef")
	if err != nil || !fresh || moved.Planning.Chat != "chat_0123456789abcdef" ||
		strings.Join(moved.Planning.Decisions, "|") != "Sync nightly" {
		t.Fatalf("moved = %+v, %t, %v", moved.Planning, fresh, err)
	}
	// No chat keeps the one it is in.
	if kept, fresh, err := service.StartPlanning(ctx, "prj_shop", "", ""); err != nil || fresh || kept.Planning.Chat != moved.Planning.Chat {
		t.Fatalf("kept = %+v, %t, %v", kept.Planning, fresh, err)
	}
	if _, err := service.PausePlanning(ctx, "prj_shop"); err != nil {
		t.Fatal(err)
	}
	resumed, fresh, err := service.StartPlanning(ctx, "prj_shop", "", "main")
	if err != nil || !fresh || !resumed.Planning.Open() || resumed.Planning.Chat != "main" {
		t.Fatalf("resumed = %+v, %t, %v", resumed.Planning, fresh, err)
	}
}

// Planning a vision with built work is a next round: its agenda starts from
// the round, its items go in as the next priority, and the built items stay
// as they are. A new project's planning keeps its own agenda.
func TestARoundAddsToWhatIsBuilt(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	first, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", "main")
	if err != nil || first.Planning.Round {
		t.Fatalf("first = %+v, %v", first.Planning, err)
	}
	if ignored, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Step: "round"}); err != nil || ignored.Planning.Step != "" {
		t.Fatalf("a round step in a new project's planning = %+v, %v", ignored.Planning, err)
	}
	planned, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Step: "stack", Items: []featureartifact.PlanItem{
		{Priority: 0, Title: "Read stock"}, {Priority: 1, Title: "Report differences"}, {Priority: 1, Title: "Export a CSV"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Document: "# Stock\n\n- [ ] 1. Read stock"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinishPlanning(ctx, "prj_shop"); err != nil {
		t.Fatal(err)
	}
	read, report := planned.Plan.Priorities[0].Items[0], planned.Plan.Priorities[1].Items[0]
	if _, err := service.LinkWorkOrder(ctx, "prj_shop", read.ID, "ftr_read"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LinkWorkOrder(ctx, "prj_shop", report.ID, "ftr_report"); err != nil {
		t.Fatal(err)
	}

	round, fresh, err := service.StartPlanning(ctx, "prj_shop", "", "main")
	if err != nil || !fresh || !round.Planning.Round || round.Planning.RoundPriority != 2 ||
		strings.Join(round.Planning.Checklist(), ",") != strings.Join(RoundChecklist, ",") || len(round.Planning.Decisions) != 0 {
		t.Fatalf("round = %+v, %t, %v", round.Planning, fresh, err)
	}
	if ignored, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Step: "stack", Covered: []string{"stack", "features"}}); err != nil ||
		ignored.Planning.Step != "" || strings.Join(ignored.Planning.Covered, ",") != "features" {
		t.Fatalf("the stack in a round = %+v, %v", ignored.Planning, err)
	}
	updated, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Step: "round", Items: []featureartifact.PlanItem{
		{Priority: 0, Title: "Read stock", Description: "Rewritten."},
		{Priority: 0, Title: "Weekly digest"},
		{Priority: 3, Title: "Digest by email"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	levels := map[string]int{}
	byTitle := map[string]featureartifact.VisionItem{}
	for _, priority := range updated.Plan.Priorities {
		for _, item := range priority.Items {
			levels[item.Title], byTitle[item.Title] = priority.Level, item
		}
	}
	if levels["Read stock"] != 0 || byTitle["Read stock"].FeatureID != "ftr_read" || byTitle["Read stock"].Description != "" ||
		levels["Report differences"] != 1 || byTitle["Report differences"].FeatureID != "ftr_report" ||
		levels["Weekly digest"] != 2 || levels["Digest by email"] != 3 || len(levels) != 4 || updated.Planning.Step != "round" {
		t.Fatalf("the round's plan = %+v", updated.Plan.Priorities)
	}
}
