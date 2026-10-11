package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type AgentStore struct {
	db *sql.DB
}

var _ agent.Store = (*AgentStore)(nil)

func NewAgentStore(db *sql.DB) *AgentStore {
	return &AgentStore{db: db}
}

func (s *AgentStore) List(ctx context.Context) ([]agent.Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, provider, created_at, updated_at FROM agents ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	agents := make([]agent.Agent, 0)
	for rows.Next() {
		stored, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, stored)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	return agents, nil
}

func (s *AgentStore) Get(ctx context.Context, id string) (agent.Agent, error) {
	stored, err := scanAgent(s.db.QueryRowContext(ctx,
		`SELECT id, name, provider, created_at, updated_at FROM agents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Agent{}, agent.ErrNotFound
	}
	return stored, err
}

func (s *AgentStore) Create(ctx context.Context, created agent.Agent) error {
	if err := created.Validate(); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, name, provider, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO NOTHING`,
		created.ID, created.Name, string(created.Provider),
		formatExecutionTime(created.CreatedAt), formatExecutionTime(created.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}
	if inserted == 0 {
		return agent.ErrConflict
	}
	return nil
}

func (s *AgentStore) Rename(ctx context.Context, id, name string, at time.Time) (agent.Agent, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE agents SET name = ?, updated_at = ? WHERE id = ?`, name, formatExecutionTime(at), id)
	if err != nil {
		return agent.Agent{}, fmt.Errorf("rename agent: %w", err)
	}
	if updated, err := result.RowsAffected(); err != nil || updated == 0 {
		if err != nil {
			return agent.Agent{}, fmt.Errorf("rename agent: %w", err)
		}
		return agent.Agent{}, agent.ErrNotFound
	}
	return s.Get(ctx, id)
}

func (s *AgentStore) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin agent deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var used int
	if err := tx.QueryRowContext(ctx, agentInUseQuery, id).Scan(&used); err != nil {
		return fmt.Errorf("check agent use: %w", err)
	}
	if used != 0 {
		return agent.ErrInUse
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	if deleted, err := result.RowsAffected(); err != nil || deleted == 0 {
		if err != nil {
			return fmt.Errorf("delete agent: %w", err)
		}
		return agent.ErrNotFound
	}
	return tx.Commit()
}

// agentInUseQuery reports whether a project default, a work order that has
// not started, or a run that has not finished still points at the agent.
const agentInUseQuery = `SELECT EXISTS (
	SELECT 1 FROM projects WHERE lead_agent = ?1 OR reviewer_agent = ?1
	UNION ALL
	SELECT 1 FROM features
	 WHERE (lead_agent = ?1 OR reviewer_agent = ?1) AND state IN ('draft', 'ready')
	UNION ALL
	SELECT 1 FROM runs
	 WHERE (lead_agent = ?1 OR reviewer_agent = ?1)
	   AND status NOT IN ('succeeded', 'stopped', 'failed')
)`

type agentScanner interface {
	Scan(dest ...any) error
}

func scanAgent(row agentScanner) (agent.Agent, error) {
	var stored agent.Agent
	var provider, createdAt, updatedAt string
	if err := row.Scan(&stored.ID, &stored.Name, &provider, &createdAt, &updatedAt); err != nil {
		return agent.Agent{}, err
	}
	stored.Provider = project.AgentProvider(strings.TrimSpace(provider))
	var err error
	if stored.CreatedAt, err = parseExecutionTime(createdAt); err != nil {
		return agent.Agent{}, err
	}
	if stored.UpdatedAt, err = parseExecutionTime(updatedAt); err != nil {
		return agent.Agent{}, err
	}
	return stored, nil
}
