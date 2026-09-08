package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type ProjectStore struct {
	db *sql.DB
}

var _ project.Store = (*ProjectStore)(nil)

func NewProjectStore(db *sql.DB) *ProjectStore {
	return &ProjectStore{db: db}
}

func (s *ProjectStore) Create(
	ctx context.Context,
	createdProject project.Project,
) error {
	recoveryPolicy, err := project.NormalizeRecoveryPolicy(createdProject.RecoveryPolicy)
	if err != nil {
		return err
	}
	createdProject.RecoveryPolicy = recoveryPolicy
	result, err := s.db.ExecContext(
		ctx,
		`
			INSERT INTO projects (id, name, recovery_policy, created_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`,
		createdProject.ID,
		createdProject.Name,
		createdProject.RecoveryPolicy,
		createdProject.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf(
			"insert project %q: %w",
			createdProject.ID,
			err,
		)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"read inserted row count for project %q: %w",
			createdProject.ID,
			err,
		)
	}

	if rowsAffected == 0 {
		return project.ErrAlreadyExists
	}

	if rowsAffected != 1 {
		return fmt.Errorf(
			"insert project %q: expected one affected row, got %d",
			createdProject.ID,
			rowsAffected,
		)
	}

	return nil
}

func (s *ProjectStore) GetByID(
	ctx context.Context,
	id string,
) (project.Project, error) {
	storedProject := project.Project{}
	var createdAt string

	err := s.db.QueryRowContext(
		ctx,
		`
			SELECT id, name, recovery_policy, created_at
			FROM projects
			WHERE id = ?
		`,
		id,
	).Scan(
		&storedProject.ID,
		&storedProject.Name,
		&storedProject.RecoveryPolicy,
		&createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return project.Project{}, project.ErrNotFound
		}

		return project.Project{}, fmt.Errorf(
			"select project %q: %w",
			id,
			err,
		)
	}

	storedProject.CreatedAt, err = time.Parse(
		time.RFC3339Nano,
		createdAt,
	)
	if err != nil {
		return project.Project{}, fmt.Errorf(
			"parse creation time for project %q: %w",
			id,
			err,
		)
	}

	return storedProject, nil
}
