package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func Migrate(
	ctx context.Context,
	db *sql.DB,
) error {
	migrations, err := fs.Sub(
		migrationFiles,
		"migrations",
	)
	if err != nil {
		return fmt.Errorf(
			"open embedded migrations: %w",
			err,
		)
	}

	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations,
	)
	if err != nil {
		return fmt.Errorf(
			"create migration provider: %w",
			err,
		)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf(
			"apply database migrations: %w",
			err,
		)
	}

	return nil
}
