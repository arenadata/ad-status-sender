package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
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

func migrateUp(ctx context.Context, db *sql.DB) error {
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("goose dialect: %w", err)
	}
	goose.SetBaseFS(migrationsFS)
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("goose up: %w", err)
	}
	return nil
}
