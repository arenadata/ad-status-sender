package sqlite

import (
	"context"
	"database/sql"
)

func EnsureHostTx(ctx context.Context, tx *sql.Tx, id int, name string) (int, error) {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO host(id, name) VALUES(?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name       = COALESCE(excluded.name, host.name),
			updated_at = strftime('%s','now')`,
		id, nilIfEmpty(name),
	)
	return id, err
}
