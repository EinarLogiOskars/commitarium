package workerjournal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (store *Store) CreateAttempt(
	ctx context.Context,
	creation AttemptCreation,
) (workerhttp.Attempt, bool, error) {
	if err := creation.Validate(); err != nil {
		return workerhttp.Attempt{}, false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return workerhttp.Attempt{}, false, fmt.Errorf("begin worker attempt creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, launchKey, requestDigest, found, err := findAttempt(ctx, tx, creation.Attempt.AttemptReference)
	if err != nil {
		return workerhttp.Attempt{}, false, err
	}
	if found {
		if launchKey != creation.IdempotencyKey ||
			requestDigest != creation.RequestDigest ||
			existing.Mode != creation.Attempt.Mode ||
			existing.Assignment != creation.Attempt.Assignment {
			return workerhttp.Attempt{}, false, ErrAttemptConflict
		}
		return existing, false, nil
	}

	var activeAttemptID string
	err = tx.QueryRowContext(
		ctx,
		`SELECT attempt_id FROM worker_attempts
		 WHERE session_id = ? AND state <> 'terminal' LIMIT 1`,
		creation.Attempt.SessionID,
	).Scan(&activeAttemptID)
	if err == nil {
		return workerhttp.Attempt{}, false, ErrAttemptActive
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return workerhttp.Attempt{}, false, fmt.Errorf("find active worker attempt: %w", err)
	}

	assignmentJSON, err := json.Marshal(creation.Attempt.Assignment)
	if err != nil {
		return workerhttp.Attempt{}, false, fmt.Errorf("encode worker assignment: %w", err)
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO worker_attempts (
			session_id, attempt_id, mode, assignment_json,
			launch_idempotency_key, launch_request_digest,
			provider_session_id, state, latest_event_sequence,
			started_at, updated_at, ended_at, result_json
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, NULL, NULL)`,
		creation.Attempt.SessionID,
		creation.Attempt.AttemptID,
		creation.Attempt.Mode,
		string(assignmentJSON),
		creation.IdempotencyKey,
		creation.RequestDigest,
		creation.Attempt.ProviderSessionID,
		creation.Attempt.State,
		formatTime(creation.Attempt.StartedAt),
		formatTime(creation.Attempt.UpdatedAt),
	)
	if err != nil {
		return workerhttp.Attempt{}, false, fmt.Errorf("insert worker attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return workerhttp.Attempt{}, false, fmt.Errorf("commit worker attempt: %w", err)
	}
	return creation.Attempt, true, nil
}

func (store *Store) GetAttempt(
	ctx context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	if err := reference.Validate(); err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("%w: invalid attempt reference", ErrInvalidRecord)
	}
	attempt, _, _, found, err := findAttempt(ctx, store.db, reference)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	if !found {
		return workerhttp.Attempt{}, ErrNotFound
	}
	return attempt, nil
}

func (store *Store) TransitionAttempt(
	ctx context.Context,
	transition AttemptTransition,
) (workerhttp.Attempt, error) {
	if err := transition.Validate(); err != nil {
		return workerhttp.Attempt{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("begin worker attempt transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	attempt, _, _, found, err := findAttempt(ctx, tx, transition.Reference)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	if !found {
		return workerhttp.Attempt{}, ErrNotFound
	}
	if attempt.State != transition.Expected {
		return workerhttp.Attempt{}, ErrStateConflict
	}
	if transition.OccurredAt.Before(attempt.UpdatedAt) {
		return workerhttp.Attempt{}, fmt.Errorf("%w: transition precedes the current attempt state", ErrInvalidRecord)
	}

	// Provider session IDs are opaque values owned by the provider. Validation
	// may inspect surrounding whitespace, but the journal must preserve the
	// exact value it was given so a later resume targets the same session.
	providerSessionID := transition.ProviderSessionID
	if attempt.ProviderSessionID != "" &&
		providerSessionID != "" &&
		attempt.ProviderSessionID != providerSessionID {
		return workerhttp.Attempt{}, ErrAttemptConflict
	}
	if providerSessionID != "" {
		attempt.ProviderSessionID = providerSessionID
	}
	attempt.State = transition.State
	attempt.UpdatedAt = transition.OccurredAt.UTC()
	attempt.EndedAt = nil
	attempt.Result = nil
	if attempt.State == workerhttp.AttemptStateTerminal {
		endedAt := attempt.UpdatedAt
		attempt.EndedAt = &endedAt
		attempt.Result = transition.Result
	}
	if err := attempt.Validate(); err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("%w: transitioned attempt is invalid: %v", ErrInvalidRecord, err)
	}
	resultJSON, err := marshalOptional(attempt.Result)
	if err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("encode terminal result: %w", err)
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE worker_attempts
		 SET provider_session_id = ?, state = ?, updated_at = ?, ended_at = ?, result_json = ?
		 WHERE session_id = ? AND attempt_id = ? AND state = ?`,
		attempt.ProviderSessionID,
		attempt.State,
		formatTime(attempt.UpdatedAt),
		formatOptionalTime(attempt.EndedAt),
		resultJSON,
		attempt.SessionID,
		attempt.AttemptID,
		transition.Expected,
	)
	if err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("update worker attempt: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return workerhttp.Attempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return workerhttp.Attempt{}, fmt.Errorf("commit worker attempt transition: %w", err)
	}
	return attempt, nil
}

func (store *Store) RecoverInterrupted(
	ctx context.Context,
	occurredAt time.Time,
) (RecoveryResult, error) {
	if occurredAt.IsZero() {
		return RecoveryResult{}, fmt.Errorf("%w: recovery time is required", ErrInvalidRecord)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("begin worker journal recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	formattedTime := formatTime(occurredAt)
	rows, err := tx.QueryContext(
		ctx,
		`SELECT updated_at FROM worker_attempts
		 WHERE state NOT IN ('terminal', 'indeterminate')
		 UNION ALL
		 SELECT updated_at FROM worker_mutations WHERE status = 'pending'`,
	)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("read worker recovery times: %w", err)
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			_ = rows.Close()
			return RecoveryResult{}, fmt.Errorf("scan worker recovery time: %w", err)
		}
		updatedAt, err := parseTime(value)
		if err != nil {
			_ = rows.Close()
			return RecoveryResult{}, err
		}
		if occurredAt.Before(updatedAt) {
			_ = rows.Close()
			return RecoveryResult{}, fmt.Errorf("%w: recovery time precedes durable worker state", ErrInvalidRecord)
		}
	}
	if err := rows.Close(); err != nil {
		return RecoveryResult{}, fmt.Errorf("close worker recovery times: %w", err)
	}
	if err := rows.Err(); err != nil {
		return RecoveryResult{}, fmt.Errorf("iterate worker recovery times: %w", err)
	}
	attemptResult, err := tx.ExecContext(
		ctx,
		`UPDATE worker_attempts
		 SET state = 'indeterminate', updated_at = ?
		 WHERE state NOT IN ('terminal', 'indeterminate')`,
		formattedTime,
	)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("mark worker attempts indeterminate: %w", err)
	}
	attemptCount, err := attemptResult.RowsAffected()
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("count indeterminate worker attempts: %w", err)
	}
	mutationResult, err := tx.ExecContext(
		ctx,
		`UPDATE worker_mutations
		 SET status = 'indeterminate', updated_at = ?
		 WHERE status = 'pending'`,
		formattedTime,
	)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("mark worker mutations indeterminate: %w", err)
	}
	mutationCount, err := mutationResult.RowsAffected()
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("count indeterminate worker mutations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RecoveryResult{}, fmt.Errorf("commit worker journal recovery: %w", err)
	}
	return RecoveryResult{AttemptsMarked: attemptCount, MutationsMarked: mutationCount}, nil
}

func (store *Store) ClaimMutation(
	ctx context.Context,
	mutation Mutation,
) (Mutation, bool, error) {
	if err := mutation.Validate(); err != nil {
		return Mutation{}, false, err
	}
	if mutation.Status != MutationPending || !mutation.UpdatedAt.Equal(mutation.RequestedAt) {
		return Mutation{}, false, fmt.Errorf("%w: new mutation must be pending at its request time", ErrInvalidRecord)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Mutation{}, false, fmt.Errorf("begin worker mutation claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, found, err := findMutation(ctx, tx, mutation.MutationIdentity)
	if err != nil {
		return Mutation{}, false, err
	}
	if found {
		if existing.Kind != mutation.Kind || existing.RequestDigest != mutation.RequestDigest {
			return Mutation{}, false, ErrMutationConflict
		}
		return existing, false, nil
	}
	attempt, _, _, found, err := findAttempt(ctx, tx, mutation.AttemptReference)
	if err != nil {
		return Mutation{}, false, err
	}
	if !found {
		return Mutation{}, false, ErrNotFound
	}
	if attempt.State.IsTerminal() {
		return Mutation{}, false, ErrStateConflict
	}
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO worker_mutations (
			session_id, attempt_id, idempotency_key, kind, request_digest,
			status, requested_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)
		 ON CONFLICT(session_id, attempt_id, idempotency_key) DO NOTHING`,
		mutation.SessionID,
		mutation.AttemptID,
		mutation.IdempotencyKey,
		mutation.Kind,
		mutation.RequestDigest,
		formatTime(mutation.RequestedAt),
		formatTime(mutation.UpdatedAt),
	)
	if err != nil {
		return Mutation{}, false, fmt.Errorf("insert worker mutation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Mutation{}, false, fmt.Errorf("count inserted worker mutation: %w", err)
	}
	if rows == 1 {
		if err := tx.Commit(); err != nil {
			return Mutation{}, false, fmt.Errorf("commit worker mutation claim: %w", err)
		}
		return mutation, true, nil
	}
	return Mutation{}, false, ErrStateConflict
}

func (store *Store) GetMutation(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
) (Mutation, error) {
	if err := identity.Validate(); err != nil {
		return Mutation{}, fmt.Errorf("%w: invalid mutation identity", ErrInvalidRecord)
	}
	mutation, found, err := findMutation(ctx, store.db, identity)
	if err != nil {
		return Mutation{}, err
	}
	if !found {
		return Mutation{}, ErrNotFound
	}
	return mutation, nil
}

func (store *Store) ResolveMutation(
	ctx context.Context,
	resolution MutationResolution,
) (Mutation, error) {
	if err := resolution.Validate(); err != nil {
		return Mutation{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Mutation{}, fmt.Errorf("begin worker mutation resolution: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	mutation, err := scanMutation(tx.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, idempotency_key, kind, request_digest,
		        status, requested_at, updated_at
		 FROM worker_mutations
		 WHERE session_id = ? AND attempt_id = ? AND idempotency_key = ?`,
		resolution.SessionID,
		resolution.AttemptID,
		resolution.IdempotencyKey,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Mutation{}, ErrNotFound
	}
	if err != nil {
		return Mutation{}, fmt.Errorf("select worker mutation for resolution: %w", err)
	}
	if mutation.Status.IsTerminal() {
		if mutation.Status == resolution.Status {
			return mutation, nil
		}
		return Mutation{}, ErrMutationConflict
	}
	if resolution.OccurredAt.Before(mutation.RequestedAt) {
		return Mutation{}, fmt.Errorf("%w: resolution precedes mutation request", ErrInvalidRecord)
	}
	mutation.Status = resolution.Status
	mutation.UpdatedAt = resolution.OccurredAt.UTC()
	result, err := tx.ExecContext(
		ctx,
		`UPDATE worker_mutations SET status = ?, updated_at = ?
		 WHERE session_id = ? AND attempt_id = ? AND idempotency_key = ? AND status = 'pending'`,
		mutation.Status,
		formatTime(mutation.UpdatedAt),
		mutation.SessionID,
		mutation.AttemptID,
		mutation.IdempotencyKey,
	)
	if err != nil {
		return Mutation{}, fmt.Errorf("update worker mutation: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return Mutation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Mutation{}, fmt.Errorf("commit worker mutation resolution: %w", err)
	}
	return mutation, nil
}

func (store *Store) AppendEvent(
	ctx context.Context,
	appendRequest EventAppend,
) (workerhttp.Event, bool, error) {
	if err := appendRequest.Validate(); err != nil {
		return workerhttp.Event{}, false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return workerhttp.Event{}, false, fmt.Errorf("begin worker event append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	attempt, _, _, found, err := findAttempt(ctx, tx, appendRequest.Event.AttemptReference)
	if err != nil {
		return workerhttp.Event{}, false, err
	}
	if !found {
		return workerhttp.Event{}, false, ErrNotFound
	}
	eventJSON, err := json.Marshal(appendRequest.Event)
	if err != nil {
		return workerhttp.Event{}, false, fmt.Errorf("encode worker event: %w", err)
	}
	existing, existingJSON, found, err := findEvent(
		ctx,
		tx,
		appendRequest.Event.AttemptReference,
		appendRequest.Event.Sequence,
	)
	if err != nil {
		return workerhttp.Event{}, false, err
	}
	if found {
		if existingJSON != string(eventJSON) ||
			attempt.LatestEventSequence < appendRequest.Event.Sequence {
			return workerhttp.Event{}, false, ErrEventConflict
		}
		return existing, false, nil
	}
	if attempt.State.IsTerminal() {
		return workerhttp.Event{}, false, ErrStateConflict
	}
	if appendRequest.Event.Sequence != attempt.LatestEventSequence+1 {
		return workerhttp.Event{}, false, ErrEventSequence
	}
	if appendRequest.AcceptedAt.Before(attempt.UpdatedAt) {
		return workerhttp.Event{}, false, fmt.Errorf("%w: event acceptance precedes current attempt state", ErrInvalidRecord)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO worker_events (session_id, attempt_id, sequence, event_json)
		 VALUES (?, ?, ?, ?)`,
		appendRequest.Event.SessionID,
		appendRequest.Event.AttemptID,
		appendRequest.Event.Sequence,
		string(eventJSON),
	); err != nil {
		return workerhttp.Event{}, false, fmt.Errorf("insert worker event: %w", err)
	}
	result, err := tx.ExecContext(
		ctx,
		`UPDATE worker_attempts SET latest_event_sequence = ?, updated_at = ?
		 WHERE session_id = ? AND attempt_id = ? AND latest_event_sequence = ?`,
		appendRequest.Event.Sequence,
		formatTime(appendRequest.AcceptedAt),
		appendRequest.Event.SessionID,
		appendRequest.Event.AttemptID,
		attempt.LatestEventSequence,
	)
	if err != nil {
		return workerhttp.Event{}, false, fmt.Errorf("advance worker event cursor: %w", err)
	}
	if err := requireOneRow(result); err != nil {
		return workerhttp.Event{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return workerhttp.Event{}, false, fmt.Errorf("commit worker event: %w", err)
	}
	return appendRequest.Event, true, nil
}

func (store *Store) ListEventsAfter(
	ctx context.Context,
	reference workerhttp.AttemptReference,
	afterSequence int64,
) ([]workerhttp.Event, error) {
	if err := reference.Validate(); err != nil || afterSequence < 0 {
		return nil, fmt.Errorf("%w: invalid event replay request", ErrInvalidRecord)
	}
	if _, err := store.GetAttempt(ctx, reference); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(
		ctx,
		`SELECT event_json FROM worker_events
		 WHERE session_id = ? AND attempt_id = ? AND sequence > ? ORDER BY sequence`,
		reference.SessionID,
		reference.AttemptID,
		afterSequence,
	)
	if err != nil {
		return nil, fmt.Errorf("list worker events: %w", err)
	}
	defer rows.Close()
	events := make([]workerhttp.Event, 0)
	for rows.Next() {
		var eventJSON string
		if err := rows.Scan(&eventJSON); err != nil {
			return nil, fmt.Errorf("scan worker event: %w", err)
		}
		event, err := decodeEvent(eventJSON)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate worker events: %w", err)
	}
	return events, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func findAttempt(
	ctx context.Context,
	querier queryRower,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, string, string, bool, error) {
	attempt, launchKey, requestDigest, err := scanAttempt(querier.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, mode, assignment_json,
		        launch_idempotency_key, launch_request_digest,
		        provider_session_id, state, latest_event_sequence,
		        started_at, updated_at, ended_at, result_json
		 FROM worker_attempts WHERE session_id = ? AND attempt_id = ?`,
		reference.SessionID,
		reference.AttemptID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return workerhttp.Attempt{}, "", "", false, nil
	}
	if err != nil {
		return workerhttp.Attempt{}, "", "", false, fmt.Errorf("select worker attempt: %w", err)
	}
	return attempt, launchKey, requestDigest, true, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanAttempt(scanner rowScanner) (workerhttp.Attempt, string, string, error) {
	var attempt workerhttp.Attempt
	var mode string
	var assignmentJSON string
	var launchKey string
	var requestDigest string
	var state string
	var startedAt string
	var updatedAt string
	var endedAt sql.NullString
	var resultJSON sql.NullString
	if err := scanner.Scan(
		&attempt.SessionID,
		&attempt.AttemptID,
		&mode,
		&assignmentJSON,
		&launchKey,
		&requestDigest,
		&attempt.ProviderSessionID,
		&state,
		&attempt.LatestEventSequence,
		&startedAt,
		&updatedAt,
		&endedAt,
		&resultJSON,
	); err != nil {
		return workerhttp.Attempt{}, "", "", err
	}
	attempt.Mode = workerhttp.AttemptMode(mode)
	attempt.State = workerhttp.AttemptState(state)
	if err := json.Unmarshal([]byte(assignmentJSON), &attempt.Assignment); err != nil {
		return workerhttp.Attempt{}, "", "", fmt.Errorf("decode worker assignment: %w", err)
	}
	var err error
	attempt.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return workerhttp.Attempt{}, "", "", err
	}
	attempt.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return workerhttp.Attempt{}, "", "", err
	}
	if endedAt.Valid {
		parsed, err := parseTime(endedAt.String)
		if err != nil {
			return workerhttp.Attempt{}, "", "", err
		}
		attempt.EndedAt = &parsed
	}
	if resultJSON.Valid {
		attempt.Result = &workerhttp.TerminalResult{}
		if err := json.Unmarshal([]byte(resultJSON.String), attempt.Result); err != nil {
			return workerhttp.Attempt{}, "", "", fmt.Errorf("decode terminal result: %w", err)
		}
	}
	if err := attempt.Validate(); err != nil {
		return workerhttp.Attempt{}, "", "", fmt.Errorf("validate stored worker attempt: %w", err)
	}
	return attempt, launchKey, requestDigest, nil
}

func scanMutation(scanner rowScanner) (Mutation, error) {
	var mutation Mutation
	var kind string
	var status string
	var requestedAt string
	var updatedAt string
	if err := scanner.Scan(
		&mutation.SessionID,
		&mutation.AttemptID,
		&mutation.IdempotencyKey,
		&kind,
		&mutation.RequestDigest,
		&status,
		&requestedAt,
		&updatedAt,
	); err != nil {
		return Mutation{}, err
	}
	mutation.Kind = MutationKind(kind)
	mutation.Status = MutationStatus(status)
	var err error
	mutation.RequestedAt, err = parseTime(requestedAt)
	if err != nil {
		return Mutation{}, err
	}
	mutation.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Mutation{}, err
	}
	if err := mutation.Validate(); err != nil {
		return Mutation{}, err
	}
	return mutation, nil
}

func findMutation(
	ctx context.Context,
	querier queryRower,
	identity workerhttp.MutationIdentity,
) (Mutation, bool, error) {
	mutation, err := scanMutation(querier.QueryRowContext(
		ctx,
		`SELECT session_id, attempt_id, idempotency_key, kind, request_digest,
		        status, requested_at, updated_at
		 FROM worker_mutations
		 WHERE session_id = ? AND attempt_id = ? AND idempotency_key = ?`,
		identity.SessionID,
		identity.AttemptID,
		identity.IdempotencyKey,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Mutation{}, false, nil
	}
	if err != nil {
		return Mutation{}, false, fmt.Errorf("select worker mutation: %w", err)
	}
	return mutation, true, nil
}

func findEvent(
	ctx context.Context,
	querier queryRower,
	reference workerhttp.AttemptReference,
	sequence int64,
) (workerhttp.Event, string, bool, error) {
	var eventJSON string
	err := querier.QueryRowContext(
		ctx,
		`SELECT event_json FROM worker_events
		 WHERE session_id = ? AND attempt_id = ? AND sequence = ?`,
		reference.SessionID,
		reference.AttemptID,
		sequence,
	).Scan(&eventJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return workerhttp.Event{}, "", false, nil
	}
	if err != nil {
		return workerhttp.Event{}, "", false, fmt.Errorf("select worker event: %w", err)
	}
	event, err := decodeEvent(eventJSON)
	return event, eventJSON, true, err
}

func decodeEvent(value string) (workerhttp.Event, error) {
	var event workerhttp.Event
	if err := json.Unmarshal([]byte(value), &event); err != nil {
		return workerhttp.Event{}, fmt.Errorf("decode worker event: %w", err)
	}
	if err := event.Validate(); err != nil {
		return workerhttp.Event{}, fmt.Errorf("validate stored worker event: %w", err)
	}
	return event, nil
}

func marshalOptional(value *workerhttp.TerminalResult) (any, error) {
	if value == nil {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(payload), nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse worker journal time: %w", err)
	}
	return parsed, nil
}

func requireOneRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected worker journal rows: %w", err)
	}
	if rows != 1 {
		return ErrStateConflict
	}
	return nil
}
