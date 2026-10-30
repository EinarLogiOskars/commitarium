package vision

import (
	"context"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/gitworkspace"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// ProjectLister lists the projects, to find the one a checkout belongs to.
type ProjectLister interface {
	List(context.Context) ([]project.Project, error)
}

// workOrderWorkspacePrefix starts the ID of a work order's workspace, whose
// rest is the work order's ID (internal/workspace).
const workOrderWorkspacePrefix = "wsp_"

// ForCheckouts answers what a managed checkout does once it is ready: a
// work order that is a slice of a written plan finds the plan at its path
// there, before its lead's first turn, so the plan never has to travel in
// the work order's goal (PLAN_MODE_PLAN.md PM6). A plan already on the
// branch, committed by an earlier slice, is left as it is.
func (service *Service) ForCheckouts(projects ProjectLister) func(ctx context.Context, spec workspace.CheckoutSpec, directory string) error {
	return func(ctx context.Context, spec workspace.CheckoutSpec, directory string) error {
		workOrderID, ok := strings.CutPrefix(spec.WorkspaceID, workOrderWorkspacePrefix)
		if !ok || workOrderID == "" {
			return nil
		}
		listed, err := projects.List(ctx)
		if err != nil {
			return err
		}
		for _, candidate := range listed {
			repository := candidate.ForgejoRepository
			if repository == nil || repository.Owner != spec.RepositoryOwner || repository.Name != spec.RepositoryName {
				continue
			}
			path, document, found, err := service.PlanFile(ctx, candidate.ID, workOrderID)
			if err != nil || !found {
				return err
			}
			if !strings.HasSuffix(document, "\n") {
				document += "\n"
			}
			_, err = gitworkspace.SeedFile(directory, path, []byte(document))
			return err
		}
		return nil
	}
}
