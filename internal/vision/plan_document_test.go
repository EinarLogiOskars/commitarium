package vision

import (
	"errors"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// Planning talks until the written plan is drafted; the draft sets its path
// once, a revision replaces it, its slices are items naming it, and Build
// it waits for it (PLAN_MODE_PLAN.md PM6).
func TestTheWrittenPlanIsDraftedThenRevised(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", "main"); err != nil {
		t.Fatal(err)
	}
	talking, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Items: []featureartifact.PlanItem{{Title: "Read stock"}}})
	if err != nil || talking.Planning.Phase != PhaseTalking || talking.Planning.Drafted() {
		t.Fatalf("talking = %+v, %v", talking.Planning, err)
	}
	if _, err := service.FinishPlanning(ctx, "prj_shop"); !errors.Is(err, ErrNoDraft) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("Build it before a draft = %v", err)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		Document: strings.Repeat("x", featureartifact.MaxPlanDocumentBytes+1),
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too long a plan = %v", err)
	}
	drafted, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		Document: "# Stock sync, first version\n\n- [ ] 1. Read stock\n- [ ] 2. Report differences",
		Items:    []featureartifact.PlanItem{{Title: "Read stock"}, {Priority: 1, Title: "Report differences"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	const path = "docs/plans/stock-sync-first-version.md"
	if !drafted.Planning.Drafted() || drafted.Planning.DocumentPath != path {
		t.Fatalf("drafted = %+v", drafted.Planning)
	}
	for _, priority := range drafted.Plan.Priorities {
		for _, item := range priority.Items {
			if item.Document != path {
				t.Fatalf("slice %q names %q", item.Title, item.Document)
			}
		}
	}
	revised, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Document: "# Renamed\n\n- [ ] 1. Read and report"})
	if err != nil || revised.Planning.DocumentPath != path || !strings.Contains(revised.Planning.Document, "Read and report") {
		t.Fatalf("revised = %+v, %v", revised.Planning, err)
	}
	if _, err := service.FinishPlanning(ctx, "prj_shop"); err != nil {
		t.Fatal(err)
	}
}

// In a round, a large change's slices are items at the round's priority and
// a smaller one's are sub-items of one item; the built items keep what they
// had. The first slice's work order commits the plan, and the others read
// it at its path; each ticks its own line.
func TestSlicesBecomeItemsAndBriefs(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", "main"); err != nil {
		t.Fatal(err)
	}
	first, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		Document: "# Stock\n\n- [ ] 1. Read stock", Items: []featureartifact.PlanItem{{Title: "Read stock"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FinishPlanning(ctx, "prj_shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.LinkWorkOrder(ctx, "prj_shop", first.Plan.Priorities[0].Items[0].ID, "ftr_read"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "", "main"); err != nil {
		t.Fatal(err)
	}
	round, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		// The same heading as the first round's plan gets a path of its own.
		Document: "# Stock\n\n- [ ] 1. Digest model\n- [ ] 2. Digest page\n- [ ] 3. Export a CSV",
		Items: []featureartifact.PlanItem{
			{Title: "Read stock", Description: "Rewritten."},
			{Priority: 1, Title: "Digest model"},
			{Priority: 1, Title: "Digest page"},
			{Priority: 2, Title: "Export", Items: []featureartifact.PlanSubItem{
				{Title: "Export a CSV", Description: "One file per shop."},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const path = "docs/plans/stock-2.md"
	if round.Planning.DocumentPath != path {
		t.Fatalf("the round's path = %q", round.Planning.DocumentPath)
	}
	byTitle, levels := map[string]featureartifact.VisionItem{}, map[string]int{}
	walkItems(round.Plan.Priorities, func(item featureartifact.VisionItem) { byTitle[item.Title] = item })
	for _, priority := range round.Plan.Priorities {
		for _, item := range priority.Items {
			levels[item.Title] = priority.Level
		}
	}
	read, model, page, export := byTitle["Read stock"], byTitle["Digest model"], byTitle["Digest page"], byTitle["Export"]
	if read.FeatureID != "ftr_read" || read.Document != "docs/plans/stock.md" || read.Description != "" ||
		levels["Digest model"] != 1 || levels["Digest page"] != 1 || levels["Export"] != 2 ||
		model.Document != path || len(export.Items) != 1 || export.Items[0].Document != path {
		t.Fatalf("the round's plan = %+v", round.Plan.Priorities)
	}
	firstBrief := SliceBrief(round, model)
	if !strings.Contains(firstBrief, "Commit the plan, as it is, at "+path) || !strings.Contains(firstBrief, "- [ ] 3. Export a CSV") ||
		!strings.Contains(firstBrief, "Digest model") {
		t.Fatalf("the first slice's brief = %q", firstBrief)
	}
	for _, later := range []featureartifact.VisionItem{page, export.Items[0]} {
		brief := SliceBrief(round, later)
		if !strings.Contains(brief, "read the plan there") || !strings.Contains(brief, path) ||
			!strings.Contains(brief, later.Title) || strings.Contains(brief, "<plan") {
			t.Fatalf("a later slice's brief = %q", brief)
		}
	}
	if SliceBrief(round, export) != "" {
		t.Fatal("an item that groups slices has no brief of its own")
	}
}
