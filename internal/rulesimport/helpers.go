package rulesimport

import (
	"context"
	"database/sql"
	"strings"

	"github.com/arenadata/ad-status-sender/internal/storage/sqlite"
)

func DedupTrim(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func ApplyHostScope(ctx context.Context, tx *sql.Tx, ruleID int64, hostIDs []int) error {
	if len(hostIDs) == 0 {
		return nil
	}
	return sqlite.SetRuleHostScopeTx(ctx, tx, ruleID, hostIDs)
}
