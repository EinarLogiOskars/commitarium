package vision

import (
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// The written plan (PLAN_MODE_PLAN.md PM6): planning ends with a plan
// document and its slices, each slice a vision item that one work order
// builds in a commit. The first slice's work order commits the document at
// its path, and every slice ticks its own line there.

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
// the written plan it is a slice of, for the end of its description: the
// first slice (in plan order) commits the document at its path, and every
// slice ticks its own line there in its commit. Empty for an item that is
// not a slice of a plan.
func SliceBrief(stored Vision, item featureartifact.VisionItem) string {
	path := item.Document
	if path == "" || len(item.Items) > 0 {
		return ""
	}
	first := ""
	walkItems(stored.Plan.Priorities, func(candidate featureartifact.VisionItem) {
		if first == "" && candidate.Document == path && len(candidate.Items) == 0 {
			first = candidate.ID
		}
	})
	tick := fmt.Sprintf("In this commit, tick this slice's line in %s (“- [ ] … %s” becomes “- [x] …”).", path, item.Title)
	document := ""
	if planning := stored.Planning; planning != nil && planning.DocumentPath == path {
		document = planning.Document
	}
	if first == item.ID && document != "" {
		return fmt.Sprintf("This is the first slice of the written plan below. Commit the plan, as it is, at %s "+
			"with this slice. %s\n\n<plan path=%q>\n%s\n</plan>", path, tick, path, document)
	}
	return fmt.Sprintf("This is a slice of the written plan at %s: read the plan there first for its goal, "+
		"decisions, and how the slices fit (the plan's first slice commits it; if it is not on the branch yet, "+
		"leave the tick). %s", path, tick)
}
