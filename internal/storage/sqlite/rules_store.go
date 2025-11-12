package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/arenadata/ad-status-sender/internal/rules"
)

func (s *Store) GetRulesRevision(ctx context.Context) (int64, error) {
	row := s.db.QueryRowContext(ctx, `SELECT revision FROM rules_revision WHERE id=1`)
	var rev int64
	scanErr := row.Scan(&rev)
	if scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, scanErr
	}
	return rev, nil
}

func (s *Store) UpdateRules(ctx context.Context, apply func(tx *sql.Tx) error) error {
	tx, beginErr := s.db.BeginTx(ctx, nil)
	if beginErr != nil {
		return beginErr
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	appErr := apply(tx)
	if appErr != nil {
		_ = tx.Rollback()
		return appErr
	}

	if _, updErr := tx.ExecContext(ctx, `
		UPDATE rules_revision
		   SET revision = revision + 1,
		       updated_at = strftime('%s','now')
		 WHERE id = 1`,
	); updErr != nil {
		_ = tx.Rollback()
		return updErr
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	return nil
}

func UpsertSystemdRuleTx(
	ctx context.Context,
	tx *sql.Tx,
	name string,
	enabled bool,
	unit, unitGlob string,
) (int64, error) {
	res, insErr := tx.ExecContext(ctx,
		`INSERT INTO rule(kind, name, enabled, updated_at) VALUES('systemd', ?, ?, ?)`,
		name, b2i(enabled), time.Now().Unix(),
	)
	if insErr != nil {
		return 0, insErr
	}
	ruleID, _ := res.LastInsertId()

	_, sysErr := tx.ExecContext(ctx,
		`INSERT INTO rule_systemd(rule_id, unit, unit_glob) VALUES(?,?,?)`,
		ruleID, nilIfEmpty(unit), nilIfEmpty(unitGlob),
	)
	if sysErr != nil {
		return 0, sysErr
	}
	return ruleID, nil
}

func UpsertDockerRuleTx(
	ctx context.Context,
	tx *sql.Tx,
	name string,
	enabled bool,
	names, labels []string,
) (int64, error) {
	res, insErr := tx.ExecContext(ctx,
		`INSERT INTO rule(kind, name, enabled, updated_at) VALUES('docker', ?, ?, ?)`,
		name, b2i(enabled), time.Now().Unix(),
	)
	if insErr != nil {
		return 0, insErr
	}
	ruleID, _ := res.LastInsertId()

	setErr := setDockerSelectorsTx(ctx, tx, ruleID, names, labels)
	if setErr != nil {
		return 0, setErr
	}
	return ruleID, nil
}

func SetRuleComponentsTx(ctx context.Context, tx *sql.Tx, ruleID int64, components []string) error {
	if _, delErr := tx.ExecContext(ctx, `DELETE FROM rule_component WHERE rule_id=?`, ruleID); delErr != nil {
		return delErr
	}
	for _, c := range components {
		if c == "" {
			continue
		}
		if _, insErr := tx.ExecContext(ctx,
			`INSERT INTO rule_component(rule_id, component_id) VALUES(?,?)`, ruleID, c); insErr != nil {
			return insErr
		}
	}
	return nil
}

func SetRuleHostScopeTx(ctx context.Context, tx *sql.Tx, ruleID int64, hostIDs []int) error {
	if _, delErr := tx.ExecContext(ctx, `DELETE FROM rule_host_scope WHERE rule_id=?`, ruleID); delErr != nil {
		return delErr
	}
	for _, h := range hostIDs {
		if _, insErr := tx.ExecContext(ctx,
			`INSERT INTO rule_host_scope(rule_id, host_id) VALUES(?,?)`, ruleID, h); insErr != nil {
			return insErr
		}
	}
	return nil
}

func setDockerSelectorsTx(ctx context.Context, tx *sql.Tx, ruleID int64, names, labels []string) error {
	if _, delErr := tx.ExecContext(ctx, `DELETE FROM rule_docker WHERE rule_id=?`, ruleID); delErr != nil {
		return delErr
	}
	for _, n := range names {
		if n == "" {
			continue
		}
		if _, insErr := tx.ExecContext(ctx,
			`INSERT INTO rule_docker(rule_id, name, label) VALUES(?, ?, NULL)`,
			ruleID, n,
		); insErr != nil {
			return insErr
		}
	}
	for _, l := range labels {
		if l == "" {
			continue
		}
		if _, insErr := tx.ExecContext(ctx,
			`INSERT INTO rule_docker(rule_id, name, label) VALUES(?, NULL, ?)`,
			ruleID, l,
		); insErr != nil {
			return insErr
		}
	}
	return nil
}

func DeleteRuleTx(ctx context.Context, tx *sql.Tx, ruleID int64) error {
	_, delErr := tx.ExecContext(ctx, `DELETE FROM rule WHERE id=?`, ruleID)
	return delErr
}

func (s *Store) LoadRulesForHost(ctx context.Context, hostID int) (rules.Rules, error) {
	out := rules.Rules{}

	// systemd
	sysRows, qErr := s.db.QueryContext(ctx, `
SELECT r.id, r.name, rs.unit, rs.unit_glob
FROM rule r
JOIN rule_systemd rs ON rs.rule_id = r.id
LEFT JOIN rule_host_scope hs ON hs.rule_id = r.id
WHERE r.enabled=1 AND r.kind='systemd'
  AND (hs.host_id IS NULL OR hs.host_id = ?)
`, hostID)
	if qErr != nil {
		return out, qErr
	}
	defer sysRows.Close()

	type srow struct {
		id       int64
		name     sql.NullString
		unit     sql.NullString
		unitGlob sql.NullString
	}
	var sys []srow
	for sysRows.Next() {
		var t srow
		scErr := sysRows.Scan(&t.id, &t.name, &t.unit, &t.unitGlob)
		if scErr != nil {
			return out, scErr
		}
		sys = append(sys, t)
	}
	if rowsErr := sysRows.Err(); rowsErr != nil {
		return out, rowsErr
	}
	for _, t := range sys {
		comp, compErr := s.listComponents(ctx, t.id)
		if compErr != nil {
			return out, compErr
		}
		out.Systemd = append(out.Systemd, rules.RuleSystemd{
			Name:       t.name.String,
			Unit:       t.unit.String,
			UnitGlob:   t.unitGlob.String,
			Components: comp,
		})
	}

	// docker
	dRows, q2Err := s.db.QueryContext(ctx, `
SELECT r.id, r.name
FROM rule r
LEFT JOIN rule_host_scope hs ON hs.rule_id = r.id
WHERE r.enabled=1 AND r.kind='docker'
  AND (hs.host_id IS NULL OR hs.host_id = ?)
`, hostID)
	if q2Err != nil {
		return out, q2Err
	}
	defer dRows.Close()

	type drow struct {
		id   int64
		name sql.NullString
	}
	var dset []drow
	for dRows.Next() {
		var t drow
		scErr := dRows.Scan(&t.id, &t.name)
		if scErr != nil {
			return out, scErr
		}
		dset = append(dset, t)
	}
	if rowsErr := dRows.Err(); rowsErr != nil {
		return out, rowsErr
	}
	for _, t := range dset {
		names, nErr := s.listDockerNames(ctx, t.id)
		if nErr != nil {
			return out, nErr
		}
		labels, lErr := s.listDockerLabels(ctx, t.id)
		if lErr != nil {
			return out, lErr
		}
		comp, compErr := s.listComponents(ctx, t.id)
		if compErr != nil {
			return out, compErr
		}
		out.Docker = append(out.Docker, rules.RuleDocker{
			Name:       t.name.String,
			Components: comp,
			Containers: rules.DockerSelector{Names: names, Labels: labels},
		})
	}
	return out, nil
}

func (s *Store) listComponents(ctx context.Context, ruleID int64) ([]string, error) {
	rows, qErr := s.db.QueryContext(ctx, `SELECT component_id FROM rule_component WHERE rule_id=?`, ruleID)
	if qErr != nil {
		return nil, qErr
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		scErr := rows.Scan(&v)
		if scErr != nil {
			return nil, scErr
		}
		out = append(out, v)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return out, nil
}

func (s *Store) listDockerNames(ctx context.Context, ruleID int64) ([]string, error) {
	rows, qErr := s.db.QueryContext(ctx,
		`SELECT name FROM rule_docker WHERE rule_id=? AND name IS NOT NULL`, ruleID)
	if qErr != nil {
		return nil, qErr
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		scErr := rows.Scan(&v)
		if scErr != nil {
			return nil, scErr
		}
		out = append(out, v)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return out, nil
}

func (s *Store) listDockerLabels(ctx context.Context, ruleID int64) ([]string, error) {
	rows, qErr := s.db.QueryContext(ctx,
		`SELECT label FROM rule_docker WHERE rule_id=? AND label IS NOT NULL`, ruleID)
	if qErr != nil {
		return nil, qErr
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		scErr := rows.Scan(&v)
		if scErr != nil {
			return nil, scErr
		}
		out = append(out, v)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return out, nil
}
