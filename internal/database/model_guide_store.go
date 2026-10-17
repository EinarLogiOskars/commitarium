package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
)

// ModelGuideStore keeps the user's overrides of the official model guide.
type ModelGuideStore struct{ db *sql.DB }

func NewModelGuideStore(db *sql.DB) *ModelGuideStore { return &ModelGuideStore{db: db} }

func (store *ModelGuideStore) ListModelGuideOverrides(ctx context.Context) ([]modelguide.Override, error) {
	rows, err := store.db.QueryContext(ctx,
		`SELECT model_id, tier, official_tier, updated_at FROM model_guide_overrides ORDER BY model_id`)
	if err != nil {
		return nil, fmt.Errorf("list model guide overrides: %w", err)
	}
	defer rows.Close()
	overrides := make([]modelguide.Override, 0)
	for rows.Next() {
		var override modelguide.Override
		var tier, officialTier, updatedAt string
		if err := rows.Scan(&override.ModelID, &tier, &officialTier, &updatedAt); err != nil {
			return nil, err
		}
		override.Tier, override.OfficialTier = modelguide.Tier(tier), modelguide.Tier(officialTier)
		if override.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
			return nil, fmt.Errorf("parse model guide override time for %q: %w", override.ModelID, err)
		}
		overrides = append(overrides, override)
	}
	return overrides, rows.Err()
}

func (store *ModelGuideStore) SaveModelGuideOverride(ctx context.Context, override modelguide.Override) error {
	_, err := store.db.ExecContext(ctx,
		`INSERT INTO model_guide_overrides(model_id, tier, official_tier, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(model_id) DO UPDATE SET tier = excluded.tier, official_tier = excluded.official_tier,
		 updated_at = excluded.updated_at`,
		override.ModelID, string(override.Tier), string(override.OfficialTier),
		override.UpdatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("save model guide override for %q: %w", override.ModelID, err)
	}
	return nil
}

func (store *ModelGuideStore) DeleteModelGuideOverride(ctx context.Context, modelID string) error {
	if _, err := store.db.ExecContext(ctx, `DELETE FROM model_guide_overrides WHERE model_id = ?`, modelID); err != nil {
		return fmt.Errorf("delete model guide override for %q: %w", modelID, err)
	}
	return nil
}

var _ modelguide.Store = (*ModelGuideStore)(nil)
