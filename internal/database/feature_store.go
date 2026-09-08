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
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`,
		createdFeature.ID,
		createdFeature.ProjectID,
		createdFeature.Title,
		createdFeature.Description,
		createdFeature.State,
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
	storedFeature := feature.Feature{}
	var storedState string
	var createdAt string
	var updatedAt string

	err := s.db.QueryRowContext(
		ctx,
		`
			SELECT
				id,
				project_id,
				title,
				description,
				state,
				created_at,
				updated_at
			FROM features
			WHERE id = ?
		`,
		id,
	).Scan(
		&storedFeature.ID,
		&storedFeature.ProjectID,
		&storedFeature.Title,
		&storedFeature.Description,
		&storedState,
		&createdAt,
		&updatedAt,
	)
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

	storedFeature.State = feature.State(storedState)

	storedFeature.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return feature.Feature{}, fmt.Errorf(
			"parse creation time for feature %q: %w",
			id,
			err,
		)
	}

	storedFeature.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return feature.Feature{}, fmt.Errorf(
			"parse update time for feature %q: %w",
			id,
			err,
		)
	}

	return storedFeature, nil
}
