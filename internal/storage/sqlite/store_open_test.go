package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenAndPragmas(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "rules.db")

	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var fk int
	row := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`)
	if scanErr := row.Scan(&fk); scanErr != nil {
		t.Fatalf("PRAGMA scan: %v", scanErr)
	}
	if fk != 1 {
		t.Fatalf("PRAGMA foreign_keys=%d, want 1", fk)
	}

	checks := []string{
		"rules_revision",
		"host",
		"rule",
		"rule_component",
		"rule_host_scope",
		"rule_systemd",
		"rule_docker",
	}
	for _, tbl := range checks {
		var cnt int
		q := `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`
		if qErr := s.db.QueryRowContext(ctx, q, tbl).Scan(&cnt); qErr != nil {
			t.Fatalf("sqlite_master scan %s: %v", tbl, qErr)
		}
		if cnt != 1 {
			t.Fatalf("table %s not found", tbl)
		}
	}
}
