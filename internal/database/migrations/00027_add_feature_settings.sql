-- +goose Up

ALTER TABLE features
    ADD COLUMN planning_round_limit INTEGER NOT NULL DEFAULT 6
    CHECK (planning_round_limit >= 0);

ALTER TABLE features
    ADD COLUMN implementation_review_round_limit INTEGER NOT NULL DEFAULT 6
    CHECK (implementation_review_round_limit >= 0);

ALTER TABLE features
    ADD COLUMN lead_provider TEXT NOT NULL DEFAULT 'codex'
    CHECK (lead_provider IN ('codex', 'claude'));

ALTER TABLE features
    ADD COLUMN reviewer_provider TEXT NOT NULL DEFAULT 'codex'
    CHECK (reviewer_provider IN ('codex', 'claude'));

ALTER TABLE features
    ADD COLUMN merge_policy TEXT NOT NULL DEFAULT 'require_user_approval'
    CHECK (merge_policy IN ('require_user_approval', 'auto_after_gates'));

ALTER TABLE features
    ADD COLUMN autonomy_policy TEXT NOT NULL DEFAULT 'review_each_phase'
    CHECK (autonomy_policy IN ('review_each_phase', 'run_to_completion'));

-- Runs already contain the immutable values that their work orders started
-- with. Preserve those values when upgrading an admitted order; an order that
-- has not started yet captures its project's settings at migration time.
UPDATE features AS feature
SET planning_round_limit = COALESCE(
        (SELECT planning_round_limit FROM runs WHERE feature_id = feature.id ORDER BY started_at, id LIMIT 1),
        (SELECT planning_round_limit FROM projects WHERE id = feature.project_id)
    ),
    implementation_review_round_limit = COALESCE(
        (SELECT implementation_review_round_limit FROM runs WHERE feature_id = feature.id ORDER BY started_at, id LIMIT 1),
        (SELECT implementation_review_round_limit FROM projects WHERE id = feature.project_id)
    ),
    lead_provider = COALESCE(
        (SELECT lead_provider FROM runs WHERE feature_id = feature.id ORDER BY started_at, id LIMIT 1),
        (SELECT lead_provider FROM projects WHERE id = feature.project_id)
    ),
    reviewer_provider = COALESCE(
        (SELECT reviewer_provider FROM runs WHERE feature_id = feature.id ORDER BY started_at, id LIMIT 1),
        (SELECT reviewer_provider FROM projects WHERE id = feature.project_id)
    ),
    merge_policy = COALESCE(
        (SELECT merge_policy FROM runs WHERE feature_id = feature.id ORDER BY started_at, id LIMIT 1),
        (SELECT merge_policy FROM projects WHERE id = feature.project_id)
    ),
    autonomy_policy = COALESCE(
        (SELECT autonomy_policy FROM runs WHERE feature_id = feature.id ORDER BY started_at, id LIMIT 1),
        (SELECT autonomy_policy FROM projects WHERE id = feature.project_id)
    );
