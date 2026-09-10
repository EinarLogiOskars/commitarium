package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
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
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`,
		createdFeature.ID,
		createdFeature.ProjectID,
		createdFeature.Title,
		createdFeature.Description,
		createdFeature.State,
		createdFeature.AcceptedGoal,
		formatOptionalExecutionTime(createdFeature.GoalAcceptedAt),
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
		&createdAt,
		&updatedAt,
	); err != nil {
		return feature.Feature{}, err
	}

	storedFeature.State = feature.State(storedState)
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
