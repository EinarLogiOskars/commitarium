package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	mergePolicy, err := project.NormalizeMergePolicy(createdProject.MergePolicy)
	if err != nil {
		return err
	}
	createdProject.MergePolicy = mergePolicy
	if err := createdProject.DialogueLimits.Validate(); err != nil {
		return err
	}
	agentProviders, err := createdProject.AgentProviders.Normalize()
	if err != nil {
		return err
	}
	createdProject.AgentProviders = agentProviders
	var forgejoOwner string
	var forgejoRepository string
	var forgejoDefaultBranch string
	var forgejoBoundAt any
	if createdProject.ForgejoRepository != nil {
		if err := createdProject.ForgejoRepository.Validate(); err != nil {
			return err
		}
		forgejoOwner = createdProject.ForgejoRepository.Owner
		forgejoRepository = createdProject.ForgejoRepository.Name
		forgejoDefaultBranch = createdProject.ForgejoRepository.DefaultBranch
		forgejoBoundAt = createdProject.ForgejoRepository.BoundAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.ExecContext(
		ctx,
		`
			INSERT INTO projects (
				id, name, recovery_policy, merge_policy,
				planning_round_limit, implementation_review_round_limit,
				lead_provider, reviewer_provider,
				forgejo_owner, forgejo_repository, forgejo_default_branch, forgejo_bound_at,
				created_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING
		`,
		createdProject.ID,
		createdProject.Name,
		createdProject.RecoveryPolicy,
		createdProject.MergePolicy,
		createdProject.DialogueLimits.PlanningRounds,
		createdProject.DialogueLimits.ImplementationReviewRounds,
		createdProject.AgentProviders.Lead,
		createdProject.AgentProviders.Reviewer,
		forgejoOwner,
		forgejoRepository,
		forgejoDefaultBranch,
		forgejoBoundAt,
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
	storedProject, err := scanProject(s.db.QueryRowContext(
		ctx,
		`
			SELECT id, name, recovery_policy, merge_policy,
			       planning_round_limit, implementation_review_round_limit,
			       lead_provider, reviewer_provider,
			       forgejo_owner, forgejo_repository, forgejo_default_branch, forgejo_bound_at,
			       created_at
			FROM projects
			WHERE id = ?
		`,
		id,
	))
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

	return storedProject, nil
}

func (s *ProjectStore) List(ctx context.Context) ([]project.Project, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, name, recovery_policy, merge_policy,
		        planning_round_limit, implementation_review_round_limit,
		        lead_provider, reviewer_provider,
		        forgejo_owner, forgejo_repository, forgejo_default_branch, forgejo_bound_at,
		        created_at
		 FROM projects
		 ORDER BY created_at, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	projects := make([]project.Project, 0)
	for rows.Next() {
		storedProject, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, storedProject)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

func (s *ProjectStore) UpdateMergePolicy(
	ctx context.Context,
	projectID string,
	policy project.MergePolicy,
) (project.Project, error) {
	normalized, err := project.NormalizeMergePolicy(policy)
	if err != nil {
		return project.Project{}, err
	}
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE projects SET merge_policy = ? WHERE id = ?`,
		normalized,
		projectID,
	)
	if err != nil {
		return project.Project{}, fmt.Errorf("update merge policy for project %q: %w", projectID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return project.Project{}, fmt.Errorf("read merge policy update count for project %q: %w", projectID, err)
	}
	if rowsAffected == 0 {
		return project.Project{}, project.ErrNotFound
	}
	if rowsAffected != 1 {
		return project.Project{}, fmt.Errorf(
			"update merge policy for project %q: expected one affected row, got %d",
			projectID,
			rowsAffected,
		)
	}
	return s.GetByID(ctx, projectID)
}

func (s *ProjectStore) UpdateAgentProviders(
	ctx context.Context,
	projectID string,
	providers project.AgentProviders,
) (project.Project, error) {
	normalized, err := providers.Normalize()
	if err != nil {
		return project.Project{}, err
	}
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE projects SET lead_provider = ?, reviewer_provider = ? WHERE id = ?`,
		normalized.Lead,
		normalized.Reviewer,
		projectID,
	)
	if err != nil {
		return project.Project{}, fmt.Errorf("update agent providers for project %q: %w", projectID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return project.Project{}, fmt.Errorf("read agent provider update count for project %q: %w", projectID, err)
	}
	if rowsAffected == 0 {
		return project.Project{}, project.ErrNotFound
	}
	if rowsAffected != 1 {
		return project.Project{}, fmt.Errorf(
			"update agent providers for project %q: expected one affected row, got %d",
			projectID,
			rowsAffected,
		)
	}
	return s.GetByID(ctx, projectID)
}

func (s *ProjectStore) UpdateDialogueLimits(
	ctx context.Context,
	projectID string,
	limits project.DialogueLimits,
) (project.Project, error) {
	if err := limits.Validate(); err != nil {
		return project.Project{}, err
	}
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE projects
		 SET planning_round_limit = ?, implementation_review_round_limit = ?
		 WHERE id = ?`,
		limits.PlanningRounds,
		limits.ImplementationReviewRounds,
		projectID,
	)
	if err != nil {
		return project.Project{}, fmt.Errorf("update dialogue limits for project %q: %w", projectID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return project.Project{}, fmt.Errorf("read dialogue limit update count for project %q: %w", projectID, err)
	}
	if rowsAffected == 0 {
		return project.Project{}, project.ErrNotFound
	}
	if rowsAffected != 1 {
		return project.Project{}, fmt.Errorf(
			"update dialogue limits for project %q: expected one affected row, got %d",
			projectID,
			rowsAffected,
		)
	}
	return s.GetByID(ctx, projectID)
}

func (s *ProjectStore) BindForgejoRepository(
	ctx context.Context,
	projectID string,
	repository project.ForgejoRepository,
) (project.Project, error) {
	if err := repository.Validate(); err != nil {
		return project.Project{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return project.Project{}, fmt.Errorf("begin Forgejo repository binding: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	storedProject, err := scanProject(tx.QueryRowContext(
		ctx,
		`SELECT id, name, recovery_policy, merge_policy,
		        planning_round_limit, implementation_review_round_limit,
		        lead_provider, reviewer_provider,
		        forgejo_owner, forgejo_repository, forgejo_default_branch, forgejo_bound_at,
		        created_at
		 FROM projects WHERE id = ?`,
		projectID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return project.Project{}, project.ErrNotFound
	}
	if err != nil {
		return project.Project{}, fmt.Errorf("select project %q for Forgejo binding: %w", projectID, err)
	}
	if storedProject.ForgejoRepository != nil {
		if sameRepository(*storedProject.ForgejoRepository, repository) {
			return storedProject, nil
		}
		return project.Project{}, project.ErrForgejoRepositoryAlreadyBound
	}

	result, err := tx.ExecContext(
		ctx,
		`UPDATE projects
		 SET forgejo_owner = ?, forgejo_repository = ?,
		     forgejo_default_branch = ?, forgejo_bound_at = ?
		 WHERE id = ? AND forgejo_bound_at IS NULL`,
		repository.Owner,
		repository.Name,
		repository.DefaultBranch,
		repository.BoundAt.UTC().Format(time.RFC3339Nano),
		projectID,
	)
	if err != nil {
		return project.Project{}, fmt.Errorf("bind Forgejo repository to project %q: %w", projectID, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return project.Project{}, fmt.Errorf("read Forgejo repository binding row count for project %q: %w", projectID, err)
	}
	if rowsAffected != 1 {
		return project.Project{}, fmt.Errorf(
			"bind Forgejo repository to project %q: expected one affected row, got %d",
			projectID,
			rowsAffected,
		)
	}
	if err := tx.Commit(); err != nil {
		return project.Project{}, fmt.Errorf("commit Forgejo repository binding: %w", err)
	}
	storedProject.ForgejoRepository = &repository
	return storedProject, nil
}

type projectScanner interface {
	Scan(dest ...any) error
}

func scanProject(scanner projectScanner) (project.Project, error) {
	storedProject := project.Project{}
	var forgejoOwner string
	var forgejoRepository string
	var forgejoDefaultBranch string
	var forgejoBoundAt sql.NullString
	var createdAt string
	if err := scanner.Scan(
		&storedProject.ID,
		&storedProject.Name,
		&storedProject.RecoveryPolicy,
		&storedProject.MergePolicy,
		&storedProject.DialogueLimits.PlanningRounds,
		&storedProject.DialogueLimits.ImplementationReviewRounds,
		&storedProject.AgentProviders.Lead,
		&storedProject.AgentProviders.Reviewer,
		&forgejoOwner,
		&forgejoRepository,
		&forgejoDefaultBranch,
		&forgejoBoundAt,
		&createdAt,
	); err != nil {
		return project.Project{}, err
	}
	if err := storedProject.DialogueLimits.Validate(); err != nil {
		return project.Project{}, err
	}
	mergePolicy, err := project.NormalizeMergePolicy(storedProject.MergePolicy)
	if err != nil {
		return project.Project{}, err
	}
	storedProject.MergePolicy = mergePolicy
	providers, err := storedProject.AgentProviders.Normalize()
	if err != nil {
		return project.Project{}, err
	}
	storedProject.AgentProviders = providers
	parsedCreatedAt, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return project.Project{}, fmt.Errorf(
			"parse creation time for project %q: %w", storedProject.ID, err,
		)
	}
	storedProject.CreatedAt = parsedCreatedAt
	if forgejoBoundAt.Valid {
		parsedBoundAt, err := time.Parse(time.RFC3339Nano, forgejoBoundAt.String)
		if err != nil {
			return project.Project{}, fmt.Errorf(
				"parse Forgejo binding time for project %q: %w", storedProject.ID, err,
			)
		}
		storedProject.ForgejoRepository = &project.ForgejoRepository{
			Owner: forgejoOwner, Name: forgejoRepository,
			DefaultBranch: forgejoDefaultBranch, BoundAt: parsedBoundAt,
		}
	}
	return storedProject, nil
}

func sameRepository(left, right project.ForgejoRepository) bool {
	return strings.EqualFold(left.Owner, right.Owner) && strings.EqualFold(left.Name, right.Name)
}
