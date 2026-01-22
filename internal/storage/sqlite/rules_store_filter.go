package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func (s *Store) FindRuleIDsSimple(
	ctx context.Context,
	hostID int,
	componentID string,
	unit string,
	container string,
) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	return findRuleIDsSimpleTx(ctx, tx, hostID, componentID, unit, container)
}

func (s *Store) DeleteRulesSimple(
	ctx context.Context,
	hostID int,
	componentID string,
	unit string,
	container string,
) (int, error) {
	var deleted int
	err := s.UpdateRules(ctx, func(tx *sql.Tx) error {
		ids, findErr := findRuleIDsSimpleTx(ctx, tx, hostID, componentID, unit, container)
		if findErr != nil {
			return findErr
		}
		for _, id := range ids {
			if delErr := DeleteRuleTx(ctx, tx, id); delErr != nil {
				return delErr
			}
			deleted++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

func findRuleIDsSimpleTx(
	ctx context.Context,
	tx *sql.Tx,
	hostID int,
	componentID string,
	unit string,
	container string,
) ([]int64, error) {
	if hostID == 0 {
		return nil, errors.New("hostID must be non-zero")
	}
	componentID = strings.TrimSpace(componentID)
	if componentID == "" {
		return nil, errors.New("componentID must be non-empty")
	}
	unit = strings.TrimSpace(unit)
	container = strings.TrimSpace(container)

	const q = `
SELECT DISTINCT r.id
FROM rule r
WHERE 1 = 1
  AND EXISTS (
        SELECT 1 FROM rule_component rc
        WHERE rc.rule_id = r.id AND rc.component_id = ?
      )
  AND (
        EXISTS (SELECT 1 FROM rule_host_scope hs
                WHERE hs.rule_id = r.id AND hs.host_id = ?)
        OR
        NOT EXISTS (SELECT 1 FROM rule_host_scope x
                    WHERE x.rule_id = r.id)
      )
  AND ( ? = '' OR EXISTS (
        SELECT 1 FROM rule_systemd rs
        WHERE rs.rule_id = r.id AND rs.unit = ?
      ))
  AND ( ? = '' OR EXISTS (
        SELECT 1 FROM rule_docker rd
        WHERE rd.rule_id = r.id AND rd.name = ?
      ))
`

	args := []any{
		componentID,
		hostID,
		unit, unit,
		container, container,
	}

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		ids = append(ids, id)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return ids, nil
}
