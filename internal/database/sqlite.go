package database

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

func OpenSQLite(
	ctx context.Context,
	path string,
) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_busy_timeout=5000&_foreign_keys=on&_journal_mode=wal"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf(
			"open SQLite database: %w",
			err,
		)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf(
			"ping SQLite database: %w",
			err,
		)
	}

	return db, nil
}
