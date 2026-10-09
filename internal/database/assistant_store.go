package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/assistant"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// AssistantStore persists work-order assistant sessions (ADR-015).
type AssistantStore struct {
	db *sql.DB
}

var _ assistant.Store = (*AssistantStore)(nil)

func NewAssistantStore(db *sql.DB) *AssistantStore {
	return &AssistantStore{db: db}
}

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *AssistantStore) CreateAssistantSession(
	ctx context.Context,
	record assistant.Record,
	opening assistant.Message,
) (assistant.Record, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return assistant.Record{}, false, fmt.Errorf("begin assistant session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO assistant_sessions
		 (id, project_id, feature_id, provider, model, status, message, workspace_id, base_commit_id,
		  provider_session_id, attempt_id, turn, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (feature_id) DO NOTHING`,
		record.ID, record.ProjectID, record.FeatureID, string(record.Provider), record.Model,
		string(record.Status), record.Message, record.WorkspaceID, record.BaseCommitID,
		record.ProviderSessionID, record.AttemptID, record.Turn,
		formatExecutionTime(record.CreatedAt), formatExecutionTime(record.UpdatedAt),
	)
	if err != nil {
		return assistant.Record{}, false, fmt.Errorf("create assistant session: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return assistant.Record{}, false, fmt.Errorf("create assistant session: %w", err)
	}
	if inserted == 1 {
		if err := appendAssistantMessage(ctx, tx, record.ID, opening); err != nil {
			return assistant.Record{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return assistant.Record{}, false, fmt.Errorf("commit assistant session: %w", err)
	}
	stored, err := s.GetAssistantSessionByFeature(ctx, record.FeatureID)
	return stored, inserted == 1, err
}

func (s *AssistantStore) GetAssistantSessionByFeature(ctx context.Context, featureID string) (assistant.Record, error) {
	var record assistant.Record
	var provider, status, createdAt, updatedAt string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, project_id, feature_id, provider, model, status, message, workspace_id, base_commit_id,
		        provider_session_id, attempt_id, turn, created_at, updated_at
		   FROM assistant_sessions WHERE feature_id = ?`,
		featureID,
	).Scan(
		&record.ID, &record.ProjectID, &record.FeatureID, &provider, &record.Model, &status, &record.Message,
		&record.WorkspaceID, &record.BaseCommitID, &record.ProviderSessionID, &record.AttemptID, &record.Turn,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.Record{}, assistant.ErrNotFound
	}
	if err != nil {
		return assistant.Record{}, fmt.Errorf("get assistant session: %w", err)
	}
	record.Provider = project.AgentProvider(provider)
	record.Status = assistant.Status(status)
	if record.CreatedAt, err = parseExecutionTime(createdAt); err != nil {
		return assistant.Record{}, err
	}
	if record.UpdatedAt, err = parseExecutionTime(updatedAt); err != nil {
		return assistant.Record{}, err
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT role, text, occurred_at FROM assistant_messages WHERE session_id = ? ORDER BY sequence`,
		record.ID,
	)
	if err != nil {
		return assistant.Record{}, fmt.Errorf("list assistant messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	record.Messages = make([]assistant.Message, 0)
	for rows.Next() {
		var message assistant.Message
		var occurredAt string
		if err := rows.Scan(&message.Role, &message.Text, &occurredAt); err != nil {
			return assistant.Record{}, fmt.Errorf("scan assistant message: %w", err)
		}
		if message.OccurredAt, err = parseExecutionTime(occurredAt); err != nil {
			return assistant.Record{}, err
		}
		record.Messages = append(record.Messages, message)
	}
	if err := rows.Err(); err != nil {
		return assistant.Record{}, fmt.Errorf("read assistant messages: %w", err)
	}
	return record, nil
}

func (s *AssistantStore) UpdateAssistantSession(ctx context.Context, record assistant.Record) error {
	return updateAssistantSession(ctx, s.db, record)
}

func (s *AssistantStore) FinishAssistantTurn(ctx context.Context, record assistant.Record, message *assistant.Message) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin assistant turn finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := updateAssistantSession(ctx, tx, record); err != nil {
		return err
	}
	if message != nil {
		if err := appendAssistantMessage(ctx, tx, record.ID, *message); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit assistant turn finish: %w", err)
	}
	return nil
}

func (s *AssistantStore) GetAssistantMutation(ctx context.Context, sessionID, key string) (assistant.Mutation, bool, error) {
	var mutation assistant.Mutation
	err := s.db.QueryRowContext(
		ctx,
		`SELECT digest, attempt_id FROM assistant_mutations WHERE session_id = ? AND idempotency_key = ?`,
		sessionID, key,
	).Scan(&mutation.Digest, &mutation.AttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return assistant.Mutation{}, false, nil
	}
	if err != nil {
		return assistant.Mutation{}, false, fmt.Errorf("get assistant mutation: %w", err)
	}
	return mutation, true, nil
}

func (s *AssistantStore) BeginAssistantReply(
	ctx context.Context,
	record assistant.Record,
	key string,
	mutation assistant.Mutation,
	message assistant.Message,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin assistant reply: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO assistant_mutations (session_id, idempotency_key, digest, attempt_id) VALUES (?, ?, ?, ?)`,
		record.ID, key, mutation.Digest, mutation.AttemptID,
	); err != nil {
		return fmt.Errorf("record assistant reply: %w", err)
	}
	if err := appendAssistantMessage(ctx, tx, record.ID, message); err != nil {
		return err
	}
	if err := updateAssistantSession(ctx, tx, record); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit assistant reply: %w", err)
	}
	return nil
}

func (s *AssistantStore) RecordAssistantUsage(
	ctx context.Context,
	sessionID string,
	attemptID string,
	usage workerhttp.TokenUsage,
	recordedAt time.Time,
) error {
	if _, err := s.db.ExecContext(
		ctx,
		`INSERT INTO assistant_turn_usage
		 (session_id, attempt_id, input_tokens, cached_input_tokens, cache_write_tokens, output_tokens, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (session_id, attempt_id) DO NOTHING`,
		sessionID, attemptID, usage.InputTokens, usage.CachedInputTokens,
		usage.CacheWriteTokens, usage.OutputTokens, formatExecutionTime(recordedAt),
	); err != nil {
		return fmt.Errorf("record assistant token usage: %w", err)
	}
	return nil
}

func updateAssistantSession(ctx context.Context, executor sqlExecutor, record assistant.Record) error {
	result, err := executor.ExecContext(
		ctx,
		`UPDATE assistant_sessions
		    SET status = ?, message = ?, provider_session_id = ?, attempt_id = ?, turn = ?, updated_at = ?
		  WHERE id = ?`,
		string(record.Status), record.Message, record.ProviderSessionID, record.AttemptID, record.Turn,
		formatExecutionTime(record.UpdatedAt), record.ID,
	)
	if err != nil {
		return fmt.Errorf("update assistant session: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update assistant session: %w", err)
	}
	if updated != 1 {
		return assistant.ErrNotFound
	}
	return nil
}

func appendAssistantMessage(ctx context.Context, executor sqlExecutor, sessionID string, message assistant.Message) error {
	if _, err := executor.ExecContext(
		ctx,
		`INSERT INTO assistant_messages (session_id, sequence, role, text, occurred_at)
		 VALUES (?, (SELECT COALESCE(MAX(sequence), 0) + 1 FROM assistant_messages WHERE session_id = ?), ?, ?, ?)`,
		sessionID, sessionID, message.Role, message.Text, formatExecutionTime(message.OccurredAt),
	); err != nil {
		return fmt.Errorf("append assistant message: %w", err)
	}
	return nil
}
