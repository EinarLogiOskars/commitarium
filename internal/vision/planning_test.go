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
	if _, _, err := service.StartPlanning(ctx, "prj_shop", " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("planning without a goal = %v", err)
	}
	started, fresh, err := service.StartPlanning(ctx, "prj_shop", "Keep two shops' stock in step.")
	if err != nil || !fresh || !started.Planning.Open() || started.Plan.Statement != "Keep two shops' stock in step." {
		t.Fatalf("started = %+v, %t, %v", started, fresh, err)
	}
	if again, fresh, err := service.StartPlanning(ctx, "prj_shop", "Something else"); err != nil || fresh ||
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
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step."); err != nil {
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
	resumed, started, err := service.StartPlanning(ctx, "prj_shop", "")
	if err != nil || !started || !resumed.Planning.Open() ||
		strings.Join(resumed.Planning.Decisions, "|") != "Sync nightly" || resumed.Plan.Statement != "Keep stock in step." {
		t.Fatalf("resumed = %+v, %t, %v", resumed, started, err)
	}
}

// The user's edit of a vision being planned keeps its planning.
func TestEditingAPlannedVisionKeepsItsPlanning(t *testing.T) {
	service, _ := newTestService(t)
	started, _, err := service.StartPlanning(t.Context(), "prj_shop", "Keep stock in step.")
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
