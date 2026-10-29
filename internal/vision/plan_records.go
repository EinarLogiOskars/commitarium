package vision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// Plans and discarding (PLAN_MODE_PLAN.md PM4/PM5, reshaped): each planning
// session the user builds is kept on the vision as a plan of its own, so the
// vision page lists "Initial plan" and each next round with what it was
// planned from. A session the user discards instead puts the vision back as
// it was before the session started, keeping anything built meanwhile.

// PlanRecord is a planning session that was built: what it was planned
// from, and the slices (vision item IDs) it planned.
type PlanRecord struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Round marks a next round of a built project.
	Round        bool           `json:"round,omitempty"`
	Goal         string         `json:"goal"`
	Document     string         `json:"document,omitempty"`
	DocumentPath string         `json:"document_path,omitempty"`
	Covered      []string       `json:"covered,omitempty"`
	Decisions    []string       `json:"decisions"`
	Questions    []string       `json:"open_questions"`
	Research     []ResearchNote `json:"research"`
	// Slices are the IDs of the vision items the plan added or changed, top
	// level and sub-items alike.
	Slices    []string   `json:"slices"`
	StartedAt time.Time  `json:"started_at"`
	BuiltAt   *time.Time `json:"built_at,omitempty"`
}

// PlanningSnapshot is the vision as it was when a planning session
// started, which Discard puts back.
type PlanningSnapshot struct {
	// Existed is false when the session created the vision.
	Existed  bool                       `json:"existed"`
	Plan     featureartifact.VisionPlan `json:"plan"`
	Source   string                     `json:"source,omitempty"`
	Planning *Planning                  `json:"planning,omitempty"`
}

// InitialPlanTitle names a project's first plan.
const InitialPlanTitle = "Initial plan"

// snapshotOf is the vision as a planning session finds it.
func snapshotOf(current Vision, existed bool) *PlanningSnapshot {
	snapshot := &PlanningSnapshot{Existed: existed, Plan: copyPlan(current.Plan), Source: current.Source}
	if current.Planning != nil {
		earlier := *current.Planning
		earlier.Before = nil
		snapshot.Planning = &earlier
	}
	return snapshot
}

// recordOf is the plan record of a built planning session on plan.
func recordOf(planning Planning, plan featureartifact.VisionPlan, id string) PlanRecord {
	record := PlanRecord{
		ID: id, Title: planTitle(planning), Round: planning.Round, Goal: plan.Statement,
		Document: planning.Document, DocumentPath: planning.DocumentPath,
		Covered:   append([]string(nil), planning.Covered...),
		Decisions: append([]string{}, planning.Decisions...), Questions: append([]string{}, planning.Questions...),
		Research: append([]ResearchNote{}, planning.Research...), Slices: planSlices(planning, plan),
		StartedAt: planning.StartedAt, BuiltAt: planning.BuiltAt,
	}
	return record
}

// planTitle is a plan's name: "Initial plan" for a project's first, and a
// round's written plan's first heading ("Next round" without one).
func planTitle(planning Planning) string {
	if !planning.Round {
		return InitialPlanTitle
	}
	for _, line := range strings.Split(planning.Document, "\n") {
		if heading, ok := strings.CutPrefix(strings.TrimSpace(line), "# "); ok && strings.TrimSpace(heading) != "" {
			return strings.TrimSpace(heading)
		}
	}
	return "Next round"
}

// planSlices are the items the planning planned: those naming its written
// plan, or, for a first plan from before the written plan, every item.
func planSlices(planning Planning, plan featureartifact.VisionPlan) []string {
	ids := []string{}
	walkItems(plan.Priorities, func(item featureartifact.VisionItem) {
		if planning.DocumentPath != "" && item.Document == planning.DocumentPath {
			ids = append(ids, item.ID)
		}
	})
	if len(ids) == 0 && !planning.Round {
		walkItems(plan.Priorities, func(item featureartifact.VisionItem) { ids = append(ids, item.ID) })
	}
	return ids
}

// archiveBuilt keeps a built planning from before plan records as the
// vision's first plan, before a new session replaces it.
func archiveBuilt(current *Vision) {
	if current.Planning == nil || current.Planning.Status != PlanningBuilt || len(current.Plans) > 0 {
		return
	}
	current.Plans = []PlanRecord{recordOf(*current.Planning, current.Plan, "plan-1")}
}

// DiscardPlanning drops the planning session the user switched off without
// building: its draft, coverage, decisions, open questions, and research,
// and the vision items it added or changed. The vision is put back as it was
// when the session started; items built meanwhile (done, or with a work
// order) are never touched. A session that created the vision, with nothing
// built, removes it again.
func (service *Service) DiscardPlanning(ctx context.Context, projectID string) (Vision, bool, error) {
	if _, err := service.projects.GetByID(ctx, projectID); err != nil {
		return Vision{}, false, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.read(projectID)
	if err != nil {
		return Vision{}, false, err
	}
	if !current.Planning.Open() && !current.Planning.Paused() {
		return Vision{}, false, ErrNotPlanning
	}
	before := current.Planning.Before
	if before == nil {
		// A session from before snapshots: what it planned is all it can
		// have changed, so only the built work stays.
		before = &PlanningSnapshot{Existed: true, Plan: current.Plan, Source: current.Source}
		before.Plan.Priorities = []featureartifact.VisionPriority{}
	}
	if !before.Existed && !built(current.Plan) {
		if err := os.Remove(filepath.Join(service.root, "visions", projectID+".json")); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return Vision{}, false, ErrUnavailable
		}
		return Vision{}, true, nil
	}
	restored := restorePlan(before.Plan, current.Plan)
	if !before.Existed {
		// Only the built work stays, under the goal it was built for.
		restored = featureartifact.VisionPlan{
			Statement: current.Plan.Statement, Confidence: "medium", Guesses: []string{},
			Priorities: builtItems(current.Plan), Stack: current.Plan.Stack,
		}
	}
	current.Plan, current.Planning = restored, before.Planning
	if before.Source != "" {
		current.Source = before.Source
	}
	current.Revision++
	stored, err := service.write(current)
	return stored, false, err
}

// restorePlan is the plan as it was before a planning session, with what
// was built meanwhile kept: a built item the snapshot has is the current
// one, and one it does not have stays at its priority.
func restorePlan(before, current featureartifact.VisionPlan) featureartifact.VisionPlan {
	restored := copyPlan(before)
	var replace func([]featureartifact.VisionItem)
	replace = func(items []featureartifact.VisionItem) {
		for index := range items {
			if now, _, found := Find(current, items[index].ID); found && itemBuilt(now) {
				items[index] = now
				continue
			}
			replace(items[index].Items)
		}
	}
	for index := range restored.Priorities {
		replace(restored.Priorities[index].Items)
	}
	present := map[string]bool{}
	walkItems(restored.Priorities, func(item featureartifact.VisionItem) { present[item.ID] = true })
	for _, priority := range current.Priorities {
		for _, item := range priority.Items {
			if !itemBuilt(item) || present[item.ID] {
				continue
			}
			at := slices.IndexFunc(restored.Priorities, func(p featureartifact.VisionPriority) bool {
				return p.Level == priority.Level
			})
			if at < 0 {
				restored.Priorities = append(restored.Priorities,
					featureartifact.VisionPriority{Level: priority.Level, Items: []featureartifact.VisionItem{}})
				at = len(restored.Priorities) - 1
			}
			restored.Priorities[at].Items = append(restored.Priorities[at].Items, item)
		}
	}
	slices.SortStableFunc(restored.Priorities, func(a, b featureartifact.VisionPriority) int { return a.Level - b.Level })
	return restored
}

// copyPlan is a deep copy of plan.
func copyPlan(plan featureartifact.VisionPlan) featureartifact.VisionPlan {
	contents, err := json.Marshal(plan)
	if err != nil {
		panic(fmt.Sprintf("copy a vision plan: %v", err))
	}
	var copied featureartifact.VisionPlan
	if err := json.Unmarshal(contents, &copied); err != nil {
		panic(fmt.Sprintf("copy a vision plan: %v", err))
	}
	return copied
}
