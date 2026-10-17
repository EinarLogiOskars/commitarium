package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// PhaseChoices returns a work order's choices by phase (ADR-031); a work
// order without any has the zero value.
func (store *FeatureStore) PhaseChoices(ctx context.Context, featureID string) (project.PhaseChoices, error) {
	var choices project.PhaseChoices
	err := store.db.QueryRowContext(ctx,
		`SELECT lead_plan_model, lead_plan_effort, lead_effort,
		        reviewer_plan_model, reviewer_plan_effort, reviewer_effort
		   FROM work_order_phase_choices WHERE feature_id = ?`, featureID,
	).Scan(&choices.LeadPlanModel, &choices.LeadPlanEffort, &choices.LeadEffort,
		&choices.ReviewerPlanModel, &choices.ReviewerPlanEffort, &choices.ReviewerEffort)
	if errors.Is(err, sql.ErrNoRows) {
		return project.PhaseChoices{}, nil
	}
	if err != nil {
		return project.PhaseChoices{}, fmt.Errorf("read phase choices of work order %q: %w", featureID, err)
	}
	return choices, nil
}

// SavePhaseChoices replaces a work order's choices by phase.
func (store *FeatureStore) SavePhaseChoices(ctx context.Context, featureID string, choices project.PhaseChoices) error {
	_, err := store.db.ExecContext(ctx,
		`INSERT INTO work_order_phase_choices
		   (feature_id, lead_plan_model, lead_plan_effort, lead_effort,
		    reviewer_plan_model, reviewer_plan_effort, reviewer_effort, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(feature_id) DO UPDATE SET
		   lead_plan_model = excluded.lead_plan_model, lead_plan_effort = excluded.lead_plan_effort,
		   lead_effort = excluded.lead_effort, reviewer_plan_model = excluded.reviewer_plan_model,
		   reviewer_plan_effort = excluded.reviewer_plan_effort, reviewer_effort = excluded.reviewer_effort,
		   updated_at = excluded.updated_at`,
		featureID, choices.LeadPlanModel, choices.LeadPlanEffort, choices.LeadEffort,
		choices.ReviewerPlanModel, choices.ReviewerPlanEffort, choices.ReviewerEffort,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save phase choices of work order %q: %w", featureID, err)
	}
	return nil
}
