package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type FeatureStore struct {
	db *sql.DB
}

var _ feature.Store = (*FeatureStore)(nil)

func NewFeatureStore(db *sql.DB) *FeatureStore {
	return &FeatureStore{db: db}
}

func (s *FeatureStore) Create(
	ctx context.Context,
	createdFeature feature.Feature,
) error {
	if err := createdFeature.DialogueLimits.Validate(); err != nil {
		return err
	}
	agentProviders, err := createdFeature.AgentProviders.NormalizeChosen()
	if err != nil {
		return err
	}
	createdFeature.AgentProviders = agentProviders
	agentModels, err := createdFeature.AgentModels.Normalize()
	if err != nil {
		return err
	}
	createdFeature.AgentModels = agentModels
	mergePolicy, err := project.NormalizeMergePolicy(createdFeature.MergePolicy)
	if err != nil {
		return err
	}
	createdFeature.MergePolicy = mergePolicy
	autonomyPolicy, err := project.NormalizeAutonomyPolicy(createdFeature.AutonomyPolicy)
	if err != nil {
		return err
	}
	createdFeature.AutonomyPolicy = autonomyPolicy
	result, err := s.db.ExecContext(
		ctx,
		`
			INSERT INTO features (
				id,
				project_id,
				title,
				description,
				state,
				accepted_goal,
				goal_accepted_at,
				planning_round_limit,
				implementation_review_round_limit,
				lead_provider,
				reviewer_provider,
				lead_agent,
				reviewer_agent,
				lead_model,
				reviewer_model,
				merge_policy,
				autonomy_policy,
				independent_acceptance_tests,
				skip_plan,
				skip_reviewer,
				skip_code_review,
				kind,
				background,
				created_by,
				preview_check,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`,
		createdFeature.ID,
		createdFeature.ProjectID,
		createdFeature.Title,
		createdFeature.Description,
		createdFeature.State,
		createdFeature.AcceptedGoal,
		formatOptionalExecutionTime(createdFeature.GoalAcceptedAt),
		createdFeature.DialogueLimits.PlanningRounds,
		createdFeature.DialogueLimits.ImplementationReviewRounds,
		storedProvider(createdFeature.AgentProviders.Lead),
		storedProvider(createdFeature.AgentProviders.Reviewer),
		createdFeature.AgentProviders.LeadAgent,
		createdFeature.AgentProviders.ReviewerAgent,
		createdFeature.AgentModels.Lead,
		createdFeature.AgentModels.Reviewer,
		createdFeature.MergePolicy,
		createdFeature.AutonomyPolicy,
		createdFeature.IndependentAcceptanceTests,
		createdFeature.Steps.SkipPlan,
		createdFeature.Steps.SkipReviewer,
		createdFeature.Steps.SkipCodeReview,
		storedFeatureKind(createdFeature.Kind),
		createdFeature.Background,
		storedCreator(createdFeature.CreatedBy),
		createdFeature.PreviewCheck,
		createdFeature.CreatedAt.UTC().Format(time.RFC3339Nano),
		createdFeature.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf(
			"insert feature %q: %w",
			createdFeature.ID,
			err,
		)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"read inserted row count for feature %q: %w",
			createdFeature.ID,
			err,
		)
	}

	if rowsAffected == 0 {
		return feature.ErrAlreadyExists
	}

	if rowsAffected != 1 {
		return fmt.Errorf(
			"insert feature %q: expected one affected row, got %d",
			createdFeature.ID,
			rowsAffected,
		)
	}

	return nil
}

func (s *FeatureStore) GetByID(
	ctx context.Context,
	id string,
) (feature.Feature, error) {
	storedFeature, err := scanFeature(s.db.QueryRowContext(
		ctx,
		`
			SELECT
				id,
				project_id,
				title,
				description,
				state,
				accepted_goal,
				goal_accepted_at,
				planning_round_limit,
				implementation_review_round_limit,
				lead_provider,
				reviewer_provider,
				lead_agent,
				reviewer_agent,
				lead_model,
				reviewer_model,
				merge_policy,
				autonomy_policy,
				independent_acceptance_tests,
				skip_plan,
				skip_reviewer,
				skip_code_review,
				kind,
				background,
				created_by,
				preview_check,
				created_at,
				updated_at
			FROM features
			WHERE id = ?
		`,
		id,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return feature.Feature{}, feature.ErrNotFound
		}

		return feature.Feature{}, fmt.Errorf(
			"select feature %q: %w",
			id,
			err,
		)
	}

	return storedFeature, nil
}

func (s *FeatureStore) ListByProjectID(
	ctx context.Context,
	projectID string,
) ([]feature.Feature, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`
			SELECT
				id,
				project_id,
				title,
				description,
				state,
				accepted_goal,
				goal_accepted_at,
				planning_round_limit,
				implementation_review_round_limit,
				lead_provider,
				reviewer_provider,
				lead_agent,
				reviewer_agent,
				lead_model,
				reviewer_model,
				merge_policy,
				autonomy_policy,
				independent_acceptance_tests,
				skip_plan,
				skip_reviewer,
				skip_code_review,
				kind,
				background,
				created_by,
				preview_check,
				created_at,
				updated_at
			FROM features
			WHERE project_id = ?
			ORDER BY updated_at DESC, created_at DESC, id
		`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list features for project %q: %w", projectID, err)
	}
	defer rows.Close()

	features := make([]feature.Feature, 0)
	for rows.Next() {
		storedFeature, err := scanFeature(rows)
		if err != nil {
			return nil, fmt.Errorf("scan feature for project %q: %w", projectID, err)
		}
		features = append(features, storedFeature)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate features for project %q: %w", projectID, err)
	}
	return features, nil
}

type featureScanner interface {
	Scan(dest ...any) error
}

func scanFeature(row featureScanner) (feature.Feature, error) {
	storedFeature := feature.Feature{}
	var storedState string
	var storedKind string
	var goalAcceptedAt sql.NullString
	var createdAt string
	var updatedAt string

	if err := row.Scan(
		&storedFeature.ID,
		&storedFeature.ProjectID,
		&storedFeature.Title,
		&storedFeature.Description,
		&storedState,
		&storedFeature.AcceptedGoal,
		&goalAcceptedAt,
		&storedFeature.DialogueLimits.PlanningRounds,
		&storedFeature.DialogueLimits.ImplementationReviewRounds,
		&storedFeature.AgentProviders.Lead,
		&storedFeature.AgentProviders.Reviewer,
		&storedFeature.AgentProviders.LeadAgent,
		&storedFeature.AgentProviders.ReviewerAgent,
		&storedFeature.AgentModels.Lead,
		&storedFeature.AgentModels.Reviewer,
		&storedFeature.MergePolicy,
		&storedFeature.AutonomyPolicy,
		&storedFeature.IndependentAcceptanceTests,
		&storedFeature.Steps.SkipPlan,
		&storedFeature.Steps.SkipReviewer,
		&storedFeature.Steps.SkipCodeReview,
		&storedKind,
		&storedFeature.Background,
		&storedFeature.CreatedBy,
		&storedFeature.PreviewCheck,
		&createdAt,
		&updatedAt,
	); err != nil {
		return feature.Feature{}, err
	}

	storedFeature.State = feature.State(storedState)
	storedFeature.Kind = feature.Kind(storedKind)
	if storedFeature.AgentProviders.LeadAgent == "" && storedFeature.AgentProviders.ReviewerAgent == "" {
		// Agents not chosen yet (ADR-027); the provider columns hold a placeholder.
		storedFeature.AgentProviders = project.AgentProviders{}
	}
	if goalAcceptedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, goalAcceptedAt.String)
		if err != nil {
			return feature.Feature{}, fmt.Errorf(
				"parse goal acceptance time for feature %q: %w",
				storedFeature.ID,
				err,
			)
		}
		storedFeature.GoalAcceptedAt = &parsed
	}

	var err error
	storedFeature.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return feature.Feature{}, fmt.Errorf(
			"parse creation time for feature %q: %w",
			storedFeature.ID,
			err,
		)
	}
	storedFeature.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return feature.Feature{}, fmt.Errorf(
			"parse update time for feature %q: %w",
			storedFeature.ID,
			err,
		)
	}
	return storedFeature, nil
}

// storedCreator stores an unset creator as the user's.
func storedCreator(creator feature.Creator) feature.Creator {
	if creator == feature.CreatedByAssistant {
		return creator
	}
	return feature.CreatedByUser
}

// MarkCreatedByAssistant records that the assistant created a project's
// work orders.
func (s *FeatureStore) MarkCreatedByAssistant(ctx context.Context, projectID string, featureIDs []string) error {
	for _, id := range featureIDs {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE features SET created_by = 'assistant' WHERE id = ? AND project_id = ? AND created_by <> 'assistant'`,
			id, projectID,
		); err != nil {
			return fmt.Errorf("mark work order %q as the assistant's: %w", id, err)
		}
	}
	return nil
}

// Rename changes a work order's title.
func (s *FeatureStore) Rename(ctx context.Context, id string, title string) error {
	result, err := s.db.ExecContext(
		ctx, `UPDATE features SET title = ?, updated_at = ? WHERE id = ?`,
		title, time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return fmt.Errorf("rename feature %q: %w", id, err)
	}
	if updated, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("rename feature %q: %w", id, err)
	} else if updated != 1 {
		return feature.ErrNotFound
	}
	return nil
}

// UpdateReadySettings saves the settings a Ready work order starts with.
func (s *FeatureStore) UpdateReadySettings(ctx context.Context, chosen feature.Feature) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE features
		    SET lead_provider = ?, reviewer_provider = ?, lead_agent = ?, reviewer_agent = ?,
		        lead_model = ?, reviewer_model = ?, merge_policy = ?, autonomy_policy = ?,
		        independent_acceptance_tests = ?, skip_plan = ?, skip_reviewer = ?, skip_code_review = ?
		  WHERE id = ? AND state = ?`,
		storedProvider(chosen.AgentProviders.Lead), storedProvider(chosen.AgentProviders.Reviewer),
		chosen.AgentProviders.LeadAgent, chosen.AgentProviders.ReviewerAgent,
		chosen.AgentModels.Lead, chosen.AgentModels.Reviewer,
		chosen.MergePolicy, chosen.AutonomyPolicy, chosen.IndependentAcceptanceTests,
		chosen.Steps.SkipPlan, chosen.Steps.SkipReviewer, chosen.Steps.SkipCodeReview,
		chosen.ID, feature.StateReady,
	)
	if err != nil {
		return fmt.Errorf("update start settings of feature %q: %w", chosen.ID, err)
	}
	if updated, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("update start settings of feature %q: %w", chosen.ID, err)
	} else if updated != 1 {
		return feature.ErrNotReady
	}
	return nil
}

// storedFeatureKind stores a work order created without a kind as a work
// order.
func storedFeatureKind(kind feature.Kind) feature.Kind {
	if kind == "" {
		return feature.KindWorkOrder
	}
	return kind
}
