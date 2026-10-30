package vision

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// The written plan (PLAN_MODE_PLAN.md PM6): planning ends with a plan
// document and its slices, each slice a vision item that one work order
// builds in a commit. Commitarium writes the document into each slice's
// checkout at its path (plan_checkout.go); the first slice's work order
// commits it, and every slice ticks its own line there.

// documentPath is where a plan's document is committed: docs/plans/ and a
// slug of its first heading, or of the goal without one; one an earlier
// round's items already name gets a number.
func documentPath(document string, plan featureartifact.VisionPlan) string {
	title := plan.Statement
	for _, line := range strings.Split(document, "\n") {
		if heading, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok && strings.TrimSpace(heading) != "" {
			title = heading
			break
		}
	}
	slug := slugOf(title)
	taken := map[string]bool{}
	walkItems(plan.Priorities, func(item featureartifact.VisionItem) {
		if item.Document != "" {
			taken[item.Document] = true
		}
	})
	path := "docs/plans/" + slug + ".md"
	for suffix := 2; taken[path]; suffix++ {
		path = fmt.Sprintf("docs/plans/%s-%d.md", slug, suffix)
	}
	return path
}

// slugOf is a short file name from a title: lower case words joined by
// dashes, up to six of them.
func slugOf(title string) string {
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	if len(words) > 6 {
		words = words[:6]
	}
	slug := strings.Join(words, "-")
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	if slug == "" {
		return "plan"
	}
	return slug
}

// stampDocument marks the plan's items that are not built as slices of the
// document at path; built items keep what they had.
func stampDocument(priorities []featureartifact.VisionPriority, path string) {
	var walk func([]featureartifact.VisionItem)
	walk = func(items []featureartifact.VisionItem) {
		for index := range items {
			if itemBuilt(items[index]) {
				continue
			}
			items[index].Document = path
			walk(items[index].Items)
		}
	}
	for index := range priorities {
		walk(priorities[index].Items)
	}
}

func walkItems(priorities []featureartifact.VisionPriority, visit func(featureartifact.VisionItem)) {
	var walk func([]featureartifact.VisionItem)
	walk = func(items []featureartifact.VisionItem) {
		for _, item := range items {
			visit(item)
			walk(item.Items)
		}
	}
	for _, priority := range priorities {
		walk(priority.Items)
	}
}

// SliceBrief is what a work order for one of the vision's items hears of
// the written plan it is a slice of, for the end of its description. The plan
// itself never travels in the description, where a rewritten goal could drop
// it: Commitarium writes the file into the work order's checkout at its path
// (PlanFile), and the brief says where it is. The first slice (in plan
// order) commits it unchanged with its own work, and every slice ticks its
// own line there in its commit. Empty for an item that is not a slice of a
// plan.
func SliceBrief(stored Vision, item featureartifact.VisionItem) string {
	path := item.Document
	if path == "" || len(item.Items) > 0 {
		return ""
	}
	brief := fmt.Sprintf("The written plan is at %s in your checkout. The first slice commits it unchanged with "+
		"its own work; every slice ticks its own line there (“- [ ] … %s” becomes “- [x] …”). Read it first for "+
		"the goal, the decisions, and how the slices fit.", path, item.Title)
	if firstSlice(stored, path) == item.ID {
		return brief + " This is the first slice: commit the plan with it."
	}
	return brief + " This is a later slice: if the plan is not committed on your branch yet (your checkout " +
		"only has Commitarium's copy, untracked), leave it out of your commit and leave the tick; should bringing " +
		"main in stop on that copy, delete it first."
}

// firstSlice is the ID of the first slice, in plan order, of the written
// plan at path.
func firstSlice(stored Vision, path string) string {
	first := ""
	walkItems(stored.Plan.Priorities, func(candidate featureartifact.VisionItem) {
		if first == "" && candidate.Document == path && len(candidate.Items) == 0 {
			first = candidate.ID
		}
	})
	return first
}

// PlanFile is the written plan a work order is a slice of: its path in the
// repository and its current text, from the planning that wrote it or the
// plan record that kept it. found is false for a work order that is not a
// slice of a written plan, or whose plan text is gone.
func (service *Service) PlanFile(ctx context.Context, projectID, workOrderID string) (path, document string, found bool, err error) {
	if workOrderID == "" {
		return "", "", false, nil
	}
	service.mu.Lock()
	stored, err := service.read(projectID)
	service.mu.Unlock()
	if errors.Is(err, ErrNotFound) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	walkItems(stored.Plan.Priorities, func(item featureartifact.VisionItem) {
		if path == "" && item.FeatureID == workOrderID && item.Document != "" && len(item.Items) == 0 {
			path = item.Document
		}
	})
	if path == "" {
		return "", "", false, nil
	}
	document = planDocument(stored, path)
	return path, document, document != "", nil
}

// planDocument is the newest text of the written plan at path.
func planDocument(stored Vision, path string) string {
	if planning := stored.Planning; planning != nil && planning.DocumentPath == path && planning.Document != "" {
		return planning.Document
	}
	for index := len(stored.Plans) - 1; index >= 0; index-- {
		if record := stored.Plans[index]; record.DocumentPath == path && record.Document != "" {
			return record.Document
		}
	}
	return ""
}
