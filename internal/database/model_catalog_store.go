package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/modelcatalog"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type ModelCatalogStore struct{ db *sql.DB }

func NewModelCatalogStore(db *sql.DB) *ModelCatalogStore { return &ModelCatalogStore{db: db} }

func (store *ModelCatalogStore) Load(ctx context.Context) ([]modelcatalog.Catalog, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT provider, role, models_json, fetched_at, last_error FROM model_catalogs`)
	if err != nil {
		return nil, fmt.Errorf("load model catalogs: %w", err)
	}
	defer rows.Close()
	var catalogs []modelcatalog.Catalog
	for rows.Next() {
		var catalog modelcatalog.Catalog
		var encoded string
		var fetched sql.NullString
		if err := rows.Scan(&catalog.Provider, &catalog.Role, &encoded, &fetched, &catalog.LastError); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(encoded), &catalog.Models); err != nil {
			return nil, fmt.Errorf("decode %s %s model catalog: %w", catalog.Provider, catalog.Role, err)
		}
		if fetched.Valid {
			parsed, err := time.Parse(time.RFC3339Nano, fetched.String)
			if err != nil {
				return nil, err
			}
			catalog.FetchedAt = &parsed
		}
		catalogs = append(catalogs, catalog)
	}
	return catalogs, rows.Err()
}

func (store *ModelCatalogStore) SaveSuccess(ctx context.Context, catalog modelcatalog.Catalog) error {
	encoded, err := json.Marshal(catalog.Models)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO model_catalogs(provider, role, models_json, fetched_at, last_error)
		VALUES (?, ?, ?, ?, '') ON CONFLICT(provider, role) DO UPDATE SET models_json=excluded.models_json, fetched_at=excluded.fetched_at, last_error=''`,
		catalog.Provider, catalog.Role, string(encoded), catalog.FetchedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (store *ModelCatalogStore) SaveFailure(ctx context.Context, provider project.AgentProvider, role modelcatalog.Role, message string) error {
	_, err := store.db.ExecContext(ctx, `INSERT INTO model_catalogs(provider, role, last_error) VALUES (?, ?, ?)
		ON CONFLICT(provider, role) DO UPDATE SET last_error=excluded.last_error`, provider, role, message)
	return err
}

var _ modelcatalog.Store = (*ModelCatalogStore)(nil)
