package featureartifact

import (
	"fmt"
	"strings"
)

// VisionPlan is a project's vision (ADR-020): why it exists and what it should
// become, and the prioritized work that gets it there. Priorities run from P0
// (needed first); items nest up to three levels, and only items without
// children become work orders.
type VisionPlan struct {
	Statement  string           `json:"statement"`
	Priorities []VisionPriority `json:"priorities"`
	// Confidence is how sure the assistant is that it had what it needed:
	// high, medium, or low. Guesses name what it had to assume.
	Confidence string   `json:"confidence"`
	Guesses    []string `json:"guesses"`
}

type VisionPriority struct {
	Level int          `json:"level"`
	Items []VisionItem `json:"items"`
}

type VisionItem struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Items       []VisionItem `json:"items,omitempty"`
	// FeatureID is the work order delivering a leaf item.
	FeatureID string `json:"feature_id,omitempty"`
	// Done marks a leaf item the project already delivers without a work
	// order, such as one the code did before the vision was written.
	Done bool `json:"done,omitempty"`
}

const (
	MaxVisionDepth    = 3
	MaxVisionItems    = 200
	maxVisionPriority = 9
)

// Validate checks a vision plan's shape. Item IDs must be unique.
func (plan VisionPlan) Validate() error {
	if err := validRequiredText("vision statement", plan.Statement, MaxGoalBytes); err != nil {
		return err
	}
	switch plan.Confidence {
	case "high", "medium", "low":
	default:
		return fmt.Errorf("%w: vision confidence must be high, medium, or low", ErrInvalidArtifact)
	}
	if len(plan.Guesses) > MaxBriefItems {
		return fmt.Errorf("%w: too many vision guesses", ErrInvalidArtifact)
	}
	for index, guess := range plan.Guesses {
		if err := validRequiredText(fmt.Sprintf("vision guess %d", index+1), guess, MaxStepTextBytes); err != nil {
			return err
		}
	}
	seenLevels := map[int]bool{}
	seenItems := map[string]bool{}
	count := 0
	for _, priority := range plan.Priorities {
		if priority.Level < 0 || priority.Level > maxVisionPriority || seenLevels[priority.Level] {
			return fmt.Errorf("%w: vision priorities must be distinct levels from P0 to P%d", ErrInvalidArtifact, maxVisionPriority)
		}
		seenLevels[priority.Level] = true
		if err := validateVisionItems(priority.Items, 1, seenItems, &count); err != nil {
			return err
		}
	}
	return nil
}

func validateVisionItems(items []VisionItem, depth int, seen map[string]bool, count *int) error {
	if len(items) > 0 && depth > MaxVisionDepth {
		return fmt.Errorf("%w: vision items nest at most %d levels", ErrInvalidArtifact, MaxVisionDepth)
	}
	for _, item := range items {
		*count++
		if *count > MaxVisionItems {
			return fmt.Errorf("%w: a vision holds at most %d items", ErrInvalidArtifact, MaxVisionItems)
		}
		if !stepIDPattern.MatchString(item.ID) || seen[item.ID] {
			return fmt.Errorf("%w: vision item %q has an invalid or repeated ID", ErrInvalidArtifact, item.ID)
		}
		seen[item.ID] = true
		if err := validRequiredText("vision item "+item.ID+" title", item.Title, MaxStepTextBytes); err != nil {
			return err
		}
		if item.Description != strings.TrimSpace(item.Description) || len(item.Description) > MaxStepTextBytes {
			return fmt.Errorf("%w: vision item %q description is invalid", ErrInvalidArtifact, item.ID)
		}
		if len(item.Items) > 0 && (item.FeatureID != "" || item.Done) {
			return fmt.Errorf("%w: vision item %q groups others, so it has no work order and is done when they are", ErrInvalidArtifact, item.ID)
		}
		if err := validateVisionItems(item.Items, depth+1, seen, count); err != nil {
			return err
		}
	}
	return nil
}

// NormalizeProposed trims a proposed plan and gives its items stable IDs
// derived from their titles; proposed items have no work orders yet.
func (plan VisionPlan) NormalizeProposed() (VisionPlan, error) {
	plan.Statement = strings.TrimSpace(plan.Statement)
	plan.Confidence = strings.ToLower(strings.TrimSpace(plan.Confidence))
	plan.Guesses = trimmedVisionTexts(plan.Guesses)
	seen := map[string]bool{}
	for index := range plan.Priorities {
		plan.Priorities[index].Items = normalizeVisionItems(plan.Priorities[index].Items, seen)
	}
	return plan, plan.Validate()
}

func normalizeVisionItems(items []VisionItem, seen map[string]bool) []VisionItem {
	normalized := make([]VisionItem, 0, len(items))
	for _, item := range items {
		item.Title = strings.TrimSpace(item.Title)
		item.Description = strings.TrimSpace(item.Description)
		item.FeatureID = ""
		if len(item.Items) > 0 {
			item.Done = false
		}
		base := normalizeIDs([]string{item.Title}, "item")[0]
		id, suffix := base, 2
		for seen[id] {
			id = fmt.Sprintf("%s-%d", base, suffix)
			suffix++
		}
		seen[id] = true
		item.ID = id
		item.Items = normalizeVisionItems(item.Items, seen)
		normalized = append(normalized, item)
	}
	return normalized
}

func trimmedVisionTexts(values []string) []string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return trimmed
}
