package importrules

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/arenadata/ad-status-sender/internal/rules"
	"github.com/arenadata/ad-status-sender/internal/rulesimport"
	"github.com/arenadata/ad-status-sender/internal/storage/sqlite"
)

func Rules(ctx context.Context, tx *sql.Tx, rr rules.Rules, hostIDs []int) error {
	if err := ensureHostsTx(ctx, tx, hostIDs); err != nil {
		return err
	}

	if err := importSystemdRules(ctx, tx, rr.Systemd, hostIDs); err != nil {
		return err
	}
	if err := importDockerRules(ctx, tx, rr.Docker, hostIDs); err != nil {
		return err
	}
	return nil
}

func ensureHostsTx(ctx context.Context, tx *sql.Tx, hostIDs []int) error {
	for _, h := range hostIDs {
		if _, ehErr := sqlite.EnsureHostTx(ctx, tx, h, ""); ehErr != nil {
			return fmt.Errorf("ensure host %d: %w", h, ehErr)
		}
	}
	return nil
}

func importSystemdRules(
	ctx context.Context,
	tx *sql.Tx,
	rulesIn []rules.RuleSystemd,
	hostIDs []int,
) error {
	for _, s := range rulesIn {
		comps := rulesimport.DedupTrim(s.Components)
		unit := strings.TrimSpace(s.Unit)
		unitGlob := strings.TrimSpace(s.UnitGlob)
		if len(comps) == 0 || (unit == "" && unitGlob == "") {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			if unit != "" {
				name = unit
			} else {
				name = unitGlob
			}
		}
		ruleID, upErr := sqlite.UpsertSystemdRuleTx(ctx, tx, name, true, unit, unitGlob)
		if upErr != nil {
			return upErr
		}
		if setErr := sqlite.SetRuleComponentsTx(ctx, tx, ruleID, comps); setErr != nil {
			return setErr
		}
		if err := rulesimport.ApplyHostScope(ctx, tx, ruleID, hostIDs); err != nil {
			return err
		}
	}
	return nil
}

func importDockerRules(
	ctx context.Context,
	tx *sql.Tx,
	rulesIn []rules.RuleDocker,
	hostIDs []int,
) error {
	for _, d := range rulesIn {
		comps := rulesimport.DedupTrim(d.Components)
		names := rulesimport.DedupTrim(d.Containers.Names)
		labels := rulesimport.DedupTrim(d.Containers.Labels)
		if len(comps) == 0 || (len(names) == 0 && len(labels) == 0) {
			continue
		}
		name := strings.TrimSpace(d.Name)
		if name == "" {
			if len(names) > 0 {
				name = strings.Join(names, ",")
			} else {
				name = strings.Join(labels, ",")
			}
		}
		ruleID, upErr := sqlite.UpsertDockerRuleTx(ctx, tx, name, true, names, labels)
		if upErr != nil {
			return upErr
		}
		if setErr := sqlite.SetRuleComponentsTx(ctx, tx, ruleID, comps); setErr != nil {
			return setErr
		}
		if err := rulesimport.ApplyHostScope(ctx, tx, ruleID, hostIDs); err != nil {
			return err
		}
	}
	return nil
}
