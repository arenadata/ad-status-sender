package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // register SQLite driver via side effects for database/sql
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const pragmaTimeout = 5 * time.Second

type Store struct{ db *sql.DB }

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Per-connection PRAGMAs (foreign_keys, busy_timeout) below are set on one
	// connection; pin the pool to a single connection so every query sees them
	// and sqlite's single-writer model is respected.
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), pragmaTimeout)
	defer cancel()

	// Enable FK, set busy timeout, and WAL mode. Safe to run each start.
	if _, e := db.ExecContext(ctx, `
		PRAGMA foreign_keys = ON;
		PRAGMA busy_timeout = 5000;
		PRAGMA journal_mode = WAL;
	`); e != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set pragmas: %w", e)
	}

	if e := migrateUp(context.Background(), db); e != nil {
		_ = db.Close()
		return nil, e
	}

	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// gooseMu serializes goose's process-global dialect/baseFS state, which is
// mutated on every migrate; concurrent Open() calls would otherwise race.
//
//nolint:gochecknoglobals // guards goose's own process-global state; must be package-level
var gooseMu sync.Mutex

func migrateUp(ctx context.Context, db *sql.DB) error {
	gooseMu.Lock()
	defer gooseMu.Unlock()
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	goose.SetBaseFS(migrationsFS)
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
