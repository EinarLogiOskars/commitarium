-- +goose Up

ALTER TABLE worker_mutations RENAME TO worker_mutations_before_supersede;

CREATE TABLE worker_mutations (
    session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (
        kind IN ('message', 'pause', 'continue', 'stop', 'force_stop', 'supersede')
    ),
    request_digest TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'applied', 'rejected', 'indeterminate')
    ),
    requested_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (session_id, attempt_id, idempotency_key),
    FOREIGN KEY (session_id, attempt_id)
        REFERENCES worker_attempts(session_id, attempt_id) ON DELETE RESTRICT
) STRICT;

INSERT INTO worker_mutations (
    session_id, attempt_id, idempotency_key, kind, request_digest,
    status, requested_at, updated_at
)
SELECT session_id, attempt_id, idempotency_key, kind, request_digest,
       status, requested_at, updated_at
FROM worker_mutations_before_supersede;

DROP TABLE worker_mutations_before_supersede;

-- +goose Down

ALTER TABLE worker_mutations RENAME TO worker_mutations_with_supersede;

CREATE TABLE worker_mutations (
    session_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (
        kind IN ('message', 'pause', 'continue', 'stop', 'force_stop')
    ),
    request_digest TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'applied', 'rejected', 'indeterminate')
    ),
    requested_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (session_id, attempt_id, idempotency_key),
    FOREIGN KEY (session_id, attempt_id)
        REFERENCES worker_attempts(session_id, attempt_id) ON DELETE RESTRICT
) STRICT;

INSERT INTO worker_mutations (
    session_id, attempt_id, idempotency_key, kind, request_digest,
    status, requested_at, updated_at
)
SELECT session_id, attempt_id, idempotency_key, kind, request_digest,
       status, requested_at, updated_at
FROM worker_mutations_with_supersede
WHERE kind <> 'supersede';

DROP TABLE worker_mutations_with_supersede;
