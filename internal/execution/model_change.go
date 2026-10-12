package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// ModelChange is one change of a role's model while a run works: the user
// noticed a wrong model and switched it, at once (Stopped) or from the next
// turn.
type ModelChange struct {
	Role      worker.Role `json:"role"`
	FromModel string      `json:"from_model"`
	ToModel   string      `json:"to_model"`
	Stopped   bool        `json:"stopped"`
	CreatedAt time.Time   `json:"created_at"`
}

// ModelChangeStore is implemented by stores that let a run's models change.
type ModelChangeStore interface {
	// ChangeRunModels sets the run's models, records each changed role, and
	// marks the attempts stopped for the switch, in one transaction.
	ChangeRunModels(ctx context.Context, runID string, models project.AgentModels, changes []ModelChange, stopAttempts []string, occurredAt time.Time) (Run, error)
	// TakeModelSwitch removes an attempt's switch mark and reports whether it
	// had one.
	TakeModelSwitch(ctx context.Context, runID, attemptID string) (bool, error)
	HasModelSwitch(ctx context.Context, runID, attemptID string) (bool, error)
	ModelChangesForRun(ctx context.Context, runID string) ([]ModelChange, error)
}

var ErrModelChangeUnsupported = errors.New("this store cannot change a run's models")

// ChangeRunModels switches a run's models. Only the models change, never the
// agents: a new model of the same agent keeps its conversation.
func (s *Service) ChangeRunModels(
	ctx context.Context,
	runID string,
	models project.AgentModels,
	stopAttempts []string,
) (Run, []ModelChange, error) {
	store, ok := s.store.(ModelChangeStore)
	if !ok {
		return Run{}, nil, ErrModelChangeUnsupported
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return Run{}, nil, err
	}
	if run.Status.IsTerminal() {
		return Run{}, nil, fmt.Errorf("%w: the run has ended", ErrInvalidRun)
	}
	models.Lead, models.Reviewer = strings.TrimSpace(models.Lead), strings.TrimSpace(models.Reviewer)
	if models.Lead == "" {
		models.Lead = run.AgentModels.Lead
	}
	if models.Reviewer == "" {
		models.Reviewer = run.AgentModels.Reviewer
	}
	now := s.now().UTC()
	changes := make([]ModelChange, 0, 2)
	stopped := len(stopAttempts) > 0
	if models.Lead != run.AgentModels.Lead {
		changes = append(changes, ModelChange{Role: worker.RoleLead, FromModel: run.AgentModels.Lead, ToModel: models.Lead, Stopped: stopped, CreatedAt: now})
	}
	if models.Reviewer != run.AgentModels.Reviewer {
		changes = append(changes, ModelChange{Role: worker.RoleReviewer, FromModel: run.AgentModels.Reviewer, ToModel: models.Reviewer, Stopped: stopped, CreatedAt: now})
	}
	if len(changes) == 0 {
		return run, nil, nil
	}
	changed, err := store.ChangeRunModels(ctx, runID, models, changes, stopAttempts, now)
	return changed, changes, err
}

// TakeModelSwitch reports whether an attempt was stopped to switch models,
// and forgets the mark.
func (s *Service) TakeModelSwitch(ctx context.Context, runID, attemptID string) (bool, error) {
	if store, ok := s.store.(ModelChangeStore); ok {
		return store.TakeModelSwitch(ctx, runID, attemptID)
	}
	return false, nil
}

// HasModelSwitch reports whether an attempt was stopped to switch models,
// keeping the mark.
func (s *Service) HasModelSwitch(ctx context.Context, runID, attemptID string) (bool, error) {
	if store, ok := s.store.(ModelChangeStore); ok {
		return store.HasModelSwitch(ctx, runID, attemptID)
	}
	return false, nil
}

// ModelChangesForRun lists a run's model changes, oldest first.
func (s *Service) ModelChangesForRun(ctx context.Context, runID string) ([]ModelChange, error) {
	if store, ok := s.store.(ModelChangeStore); ok {
		return store.ModelChangesForRun(ctx, runID)
	}
	return nil, nil
}
