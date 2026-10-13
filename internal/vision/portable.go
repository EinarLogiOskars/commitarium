package vision

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

// PortableVisionPath is where a copy of the vision travels with the
// repository. The vision record stays the source of truth (ADR-020); the
// file only seeds the vision of a copy imported elsewhere.
const PortableVisionPath = ".commitarium/vision.json"

// SourceRepository marks a vision seeded from PortableVisionPath.
const SourceRepository = "repository"

const portableVisionMaxBytes = 256 * 1024

type repositoryFileReader interface {
	ReadRepositoryFile(ctx context.Context, projectID, filePath string, maxBytes int64) ([]byte, error)
}

// PortablePlan encodes a vision for PortableVisionPath. Work orders are this
// machine's, so links are dropped; an item whose work order delivered it is
// marked done instead.
func PortablePlan(plan featureartifact.VisionPlan, delivered func(featureID string) bool) ([]byte, error) {
	plan.Priorities = append([]featureartifact.VisionPriority(nil), plan.Priorities...)
	for index := range plan.Priorities {
		plan.Priorities[index].Items = portableItems(plan.Priorities[index].Items, delivered)
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ParsePortablePlan reads a vision written by PortablePlan.
func ParsePortablePlan(contents []byte) (featureartifact.VisionPlan, error) {
	var plan featureartifact.VisionPlan
	if err := json.Unmarshal(contents, &plan); err != nil {
		return featureartifact.VisionPlan{}, fmt.Errorf("%w: %s is not valid JSON", ErrInvalid, PortableVisionPath)
	}
	for index := range plan.Priorities {
		plan.Priorities[index].Items = portableItems(plan.Priorities[index].Items, nil)
	}
	if err := plan.Validate(); err != nil {
		return featureartifact.VisionPlan{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return plan, nil
}

func portableItems(items []featureartifact.VisionItem, delivered func(string) bool) []featureartifact.VisionItem {
	copied := make([]featureartifact.VisionItem, len(items))
	for index, item := range items {
		if item.FeatureID != "" && delivered != nil && delivered(item.FeatureID) {
			item.Done = true
		}
		item.FeatureID = ""
		item.Items = portableItems(item.Items, delivered)
		copied[index] = item
	}
	return copied
}

// seedFromRepository gives a project without a vision the one its repository
// carries, if any. Callers hold service.mu.
func (service *Service) seedFromRepository(ctx context.Context, projectID string) (Vision, error) {
	reader, ok := service.projects.(repositoryFileReader)
	if !ok {
		return Vision{}, ErrNotFound
	}
	if _, checked := service.seedChecked.LoadOrStore(projectID, true); checked {
		return Vision{}, ErrNotFound
	}
	contents, err := reader.ReadRepositoryFile(ctx, projectID, PortableVisionPath, portableVisionMaxBytes)
	if err != nil {
		return Vision{}, ErrNotFound
	}
	plan, err := ParsePortablePlan(contents)
	if err != nil {
		return Vision{}, ErrNotFound
	}
	return service.write(Vision{ProjectID: projectID, Plan: plan, Source: SourceRepository, Revision: 1})
}
