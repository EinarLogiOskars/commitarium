package vision

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// planAndBuild plans items in an open planning, drafts it, and builds it.
func planAndBuild(t *testing.T, service *Service, decisions []string, document string, items ...featureartifact.PlanItem) Vision {
	t.Helper()
	ctx := t.Context()
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Decisions: decisions, Items: items, Document: document}); err != nil {
		t.Fatal(err)
	}
	built, err := service.FinishPlanning(ctx, "prj_shop")
	if err != nil {
		t.Fatal(err)
	}
	return built
}

// Build it keeps each planning session as a plan of its own: the first as
// "Initial plan", a next round by its written plan's title, each with what
// it was planned from and the slices it planned, in the vision's JSON.
func TestBuildingAPlanKeepsItAsAPlanRecord(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", "main"); err != nil {
		t.Fatal(err)
	}
	if err := service.AddResearch(ctx, "prj_shop", "Stock APIs", "Per location. Sources: https://shopify.dev/docs"); err != nil {
		t.Fatal(err)
	}
	first := planAndBuild(t, service, []string{"data: sync nightly"}, "# Stock sync\n\n- [ ] 1. Read stock",
		featureartifact.PlanItem{Priority: 0, Title: "Read stock"})
	if len(first.Plans) != 1 {
		t.Fatalf("plans = %+v", first.Plans)
	}
	record := first.Plans[0]
	if record.ID != "plan-1" || record.Title != InitialPlanTitle || record.Round || record.Goal != "Keep stock in step." ||
		record.DocumentPath != "docs/plans/stock-sync.md" || strings.Join(record.Decisions, "|") != "data: sync nightly" ||
		len(record.Research) != 1 || strings.Join(record.Slices, ",") != "read-stock" || record.BuiltAt == nil ||
		first.Planning.Before != nil {
		t.Fatalf("record = %+v", record)
	}
	if _, err := service.LinkWorkOrder(ctx, "prj_shop", "read-stock", "ftr_read"); err != nil {
		t.Fatal(err)
	}

	round, _, err := service.StartPlanning(ctx, "prj_shop", "", "main")
	if err != nil || !round.Planning.Round || round.Planning.Before == nil || len(round.Plans) != 1 {
		t.Fatalf("round = %+v, %v", round, err)
	}
	second := planAndBuild(t, service, []string{"features: weekly"}, "# Weekly digest\n\n- [ ] 1. Send the digest",
		featureartifact.PlanItem{Priority: 0, Title: "Read stock"}, featureartifact.PlanItem{Title: "Send the digest"})
	if len(second.Plans) != 2 || second.Plans[0].Title != InitialPlanTitle {
		t.Fatalf("plans = %+v", second.Plans)
	}
	next := second.Plans[1]
	if next.ID != "plan-2" || next.Title != "Weekly digest" || !next.Round ||
		strings.Join(next.Slices, ",") != "send-the-digest" || strings.Join(next.Decisions, "|") != "features: weekly" {
		t.Fatalf("round record = %+v", next)
	}

	contents, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"plans":[{"id":"plan-1","title":"Initial plan"`, `"slices":["send-the-digest"]`,
		`"document_path":"docs/plans/weekly-digest.md"`, `"round":true`} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("JSON lacks %s: %s", want, contents)
		}
	}
}

// A planning built before plan records is kept as the first plan when the
// next session starts, rather than lost.
func TestABuiltPlanningFromBeforeRecordsBecomesTheInitialPlan(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", ""); err != nil {
		t.Fatal(err)
	}
	built := planAndBuild(t, service, []string{"Sync nightly"}, "# Stock\n\n- [ ] 1. Read stock",
		featureartifact.PlanItem{Title: "Read stock"})
	built.Plans = nil // as written before plan records
	if _, err := service.write(built); err != nil {
		t.Fatal(err)
	}
	started, _, err := service.StartPlanning(ctx, "prj_shop", "", "")
	if err != nil || len(started.Plans) != 1 || started.Plans[0].Title != InitialPlanTitle ||
		strings.Join(started.Plans[0].Decisions, "|") != "Sync nightly" || len(started.Planning.Decisions) != 0 {
		t.Fatalf("started = %+v, %v", started, err)
	}
}

// Discard puts the vision back as the session found it: its items, goal,
// and earlier planning, without the session's draft or notes; work built
// meanwhile stays, and a session that made the vision removes it.
func TestDiscardingPlanningRestoresTheVisionButKeepsBuiltWork(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, _, err := service.DiscardPlanning(ctx, "prj_shop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("discarding without a vision = %v", err)
	}
	// A session that made the vision, with nothing built, removes it.
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{Items: []featureartifact.PlanItem{{Title: "Read stock"}}}); err != nil {
		t.Fatal(err)
	}
	if _, removed, err := service.DiscardPlanning(ctx, "prj_shop"); err != nil || !removed {
		t.Fatalf("removed = %t, %v", removed, err)
	}
	if _, err := service.Get(ctx, "prj_shop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the vision after discarding its first planning = %v", err)
	}

	// A built first plan, and a round on it.
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", ""); err != nil {
		t.Fatal(err)
	}
	first := planAndBuild(t, service, []string{"Sync nightly"}, "# Stock\n\n- [ ] 1. Read stock",
		featureartifact.PlanItem{Title: "Read stock"}, featureartifact.PlanItem{Priority: 1, Title: "Export a CSV"})
	if _, err := service.LinkWorkOrder(ctx, "prj_shop", "read-stock", "ftr_read"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := service.AddResearch(ctx, "prj_shop", "Digests", "Weekly works. Sources: https://example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		Goal: "Keep stock in step, and say so weekly.", Decisions: []string{"features: weekly digest"},
		Document: "# Digest\n\n- [ ] 1. Send the digest",
		Items:    []featureartifact.PlanItem{{Title: "Send the digest"}, {Title: "Digest settings"}},
	}); err != nil {
		t.Fatal(err)
	}
	// One of the round's items gets a work order before the user discards.
	if _, err := service.LinkWorkOrder(ctx, "prj_shop", "send-the-digest", "ftr_digest"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PausePlanning(ctx, "prj_shop"); err != nil {
		t.Fatal(err) // paused planning can be discarded too
	}
	restored, removed, err := service.DiscardPlanning(ctx, "prj_shop")
	if err != nil || removed {
		t.Fatalf("discard = %t, %v", removed, err)
	}
	titles := map[string]featureartifact.VisionItem{}
	walkItems(restored.Plan.Priorities, func(item featureartifact.VisionItem) { titles[item.Title] = item })
	if restored.Plan.Statement != first.Plan.Statement || titles["Read stock"].FeatureID != "ftr_read" ||
		titles["Export a CSV"].ID == "" || titles["Send the digest"].FeatureID != "ftr_digest" ||
		titles["Digest settings"].ID != "" || len(titles) != 3 {
		t.Fatalf("restored plan = %+v", restored.Plan)
	}
	if restored.Planning == nil || restored.Planning.Status != PlanningBuilt || restored.Planning.Round ||
		strings.Join(restored.Planning.Decisions, "|") != "Sync nightly" || len(restored.Plans) != 1 {
		t.Fatalf("restored planning = %+v, plans %+v", restored.Planning, restored.Plans)
	}
	if _, _, err := service.DiscardPlanning(ctx, "prj_shop"); !errors.Is(err, ErrNotPlanning) {
		t.Fatalf("discarding a built plan = %v", err)
	}
}
