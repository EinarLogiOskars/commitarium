package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenSQLite(t *testing.T) {
	path := filepath.Join(
		t.TempDir(),
		"coordinator.db",
	)

	db, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()

	var result int
	err = db.QueryRowContext(
		t.Context(),
		"SELECT 1",
	).Scan(&result)
	if err != nil {
		t.Fatalf("query database: %v", err)
	}

	if result != 1 {
		t.Errorf("expected 1, got %d", result)
	}
}

func TestOpenSQLiteReturnsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	db, err := OpenSQLite(
		ctx,
		filepath.Join(t.TempDir(), "coordinator.db"),
	)

	if db != nil {
		_ = db.Close()
		t.Fatalf("expected no database after cancellation")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"expect context cancellation, got %v",
			err,
		)
	}
}

func TestOpenSQLiteEnablesForeignKeys(t *testing.T) {
	db, err := OpenSQLite(
		t.Context(),
		filepath.Join(t.TempDir(), "coordinator.db"),
	)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()

	var enabled int
	if err := db.QueryRowContext(
		t.Context(),
		"PRAGMA foreign_keys",
	).Scan(&enabled); err != nil {
		t.Fatalf("read foreign-key setting: %v", err)
	}

	if enabled != 1 {
		t.Errorf(
			"expected foreign keys to be enabled, got %d",
			enabled,
		)
	}
}

func TestOpenSQLiteSetsBusyTimeout(t *testing.T) {
	db, err := OpenSQLite(
		t.Context(),
		filepath.Join(t.TempDir(), "coordinator.db"),
	)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()

	var milliseconds int
	if err := db.QueryRowContext(
		t.Context(),
		"PRAGMA busy_timeout",
	).Scan(&milliseconds); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}

	if milliseconds != 5000 {
		t.Errorf(
			"expected busy timeout 5000, got %d",
			milliseconds,
		)
	}
}

func TestOpenSQLiteEnablesWAL(t *testing.T) {
	db, err := OpenSQLite(
		t.Context(),
		filepath.Join(t.TempDir(), "coordinator.db"),
	)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	defer db.Close()

	var journalMode string
	if err := db.QueryRowContext(
		t.Context(),
		"PRAGMA journal_mode",
	).Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}

	if journalMode != "wal" {
		t.Errorf(
			"expected journal mode %q, got %q",
			"wal",
			journalMode,
		)
	}
}
