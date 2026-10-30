package vision

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type listedProjects []project.Project

func (projects listedProjects) List(context.Context) ([]project.Project, error) { return projects, nil }

// Each slice's checkout gets the written plan at its path before its lead's
// first turn, the first slice's and the later ones' alike; a plan already
// on the branch is kept as it is, and other checkouts get nothing.
func TestTheWrittenPlanReachesEachSlicesCheckout(t *testing.T) {
	service, _ := newTestService(t)
	ctx := t.Context()
	if _, _, err := service.StartPlanning(ctx, "prj_shop", "Keep stock in step.", "main"); err != nil {
		t.Fatal(err)
	}
	const document = "# Stock\n\n- [ ] 1. Read stock\n- [ ] 2. Report differences"
	drafted, err := service.UpdatePlanning(ctx, "prj_shop", PlanningUpdate{
		Document: document,
		Items:    []featureartifact.PlanItem{{Title: "Read stock"}, {Title: "Report differences"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := service.FinishPlanning(ctx, "prj_shop")
	if err != nil {
		t.Fatal(err)
	}
	items := built.Plan.Priorities[0].Items
	for index, workOrder := range []string{"fea_first", "fea_later"} {
		if _, err := service.LinkWorkOrder(ctx, "prj_shop", items[index].ID, workOrder); err != nil {
			t.Fatal(err)
		}
	}
	path := drafted.Planning.DocumentPath
	ready := service.ForCheckouts(listedProjects{{
		ID: "prj_shop", ForgejoRepository: &project.ForgejoRepository{Owner: "owner", Name: "shop"},
	}})
	checkout := func(workspaceID string) string {
		directory := t.TempDir()
		if err := os.MkdirAll(filepath.Join(directory, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := ready(ctx, workspace.CheckoutSpec{
			WorkspaceID: workspaceID, RepositoryOwner: "owner", RepositoryName: "shop",
		}, directory); err != nil {
			t.Fatalf("ready %s: %v", workspaceID, err)
		}
		return directory
	}
	for _, workspaceID := range []string{"wsp_fea_first", "wsp_fea_later"} {
		contents, err := os.ReadFile(filepath.Join(checkout(workspaceID), filepath.FromSlash(path)))
		if err != nil || string(contents) != document+"\n" {
			t.Fatalf("%s's plan = %q, %v", workspaceID, contents, err)
		}
	}
	if _, err := os.Stat(filepath.Join(checkout("wsp_fea_other"), filepath.FromSlash(path))); !os.IsNotExist(err) {
		t.Fatalf("a work order outside the plan got it: %v", err)
	}
	// A plan an earlier slice merged, ticked since, stays as the branch has it.
	merged := t.TempDir()
	if err := os.MkdirAll(filepath.Join(merged, filepath.Dir(filepath.FromSlash(path))), 0o755); err != nil {
		t.Fatal(err)
	}
	ticked := "# Stock\n\n- [x] 1. Read stock\n- [ ] 2. Report differences\n"
	if err := os.WriteFile(filepath.Join(merged, filepath.FromSlash(path)), []byte(ticked), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ready(ctx, workspace.CheckoutSpec{
		WorkspaceID: "wsp_fea_later", RepositoryOwner: "owner", RepositoryName: "shop",
	}, merged); err != nil {
		t.Fatal(err)
	}
	if contents, _ := os.ReadFile(filepath.Join(merged, filepath.FromSlash(path))); string(contents) != ticked {
		t.Fatalf("the branch's plan was overwritten: %q", contents)
	}
}
