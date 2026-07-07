package importlegacy

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arenadata/ad-status-sender/internal/rulesimport"
	"github.com/arenadata/ad-status-sender/internal/storage/sqlite"
)

// legacyHostStatusMarker names the legacy services/ file that signals a host
// heartbeat rather than a component.
const legacyHostStatusMarker = "hoststatus"

func ServicesDir(ctx context.Context, tx *sql.Tx, dir string, namePrefix string, hostIDs []int) error {
	p := strings.TrimSpace(dir)
	if p == "" {
		return errors.New("services dir is empty")
	}
	if err := ensureHostsTx(ctx, tx, hostIDs); err != nil {
		return err
	}

	entries, rdErr := os.ReadDir(p)
	if rdErr != nil {
		return fmt.Errorf("read services dir: %w", rdErr)
	}
	for _, e := range entries {
		if !isRegular(e) {
			continue
		}
		// hoststatus is a host-heartbeat marker, not a component (see legacy
		// adcm-status-checker.sh): importing it yields a phantom DOWN component.
		if e.Name() == legacyHostStatusMarker {
			continue
		}
		if err := importServiceFileTx(ctx, tx, filepath.Join(p, e.Name()), namePrefix, hostIDs); err != nil {
			return err
		}
	}
	return nil
}

func DockerDir(ctx context.Context, tx *sql.Tx, dir string, namePrefix string, hostIDs []int) error {
	p := strings.TrimSpace(dir)
	if p == "" {
		return errors.New("docker dir is empty")
	}
	if err := ensureHostsTx(ctx, tx, hostIDs); err != nil {
		return err
	}

	entries, rdErr := os.ReadDir(p)
	if rdErr != nil {
		return fmt.Errorf("read docker dir: %w", rdErr)
	}
	for _, e := range entries {
		if !isRegular(e) {
			continue
		}
		full := filepath.Join(p, e.Name())
		ruleName := namePrefix + e.Name()
		if err := importDockerFileTx(ctx, tx, full, ruleName, hostIDs); err != nil {
			return err
		}
	}
	return nil
}

func ensureHostsTx(ctx context.Context, tx *sql.Tx, hostIDs []int) error {
	if len(hostIDs) == 0 {
		return nil
	}
	for _, h := range hostIDs {
		if _, ehErr := sqlite.EnsureHostTx(ctx, tx, h, ""); ehErr != nil {
			return fmt.Errorf("ensure host %d: %w", h, ehErr)
		}
	}
	return nil
}

func importServiceFileTx(ctx context.Context, tx *sql.Tx, path string, namePrefix string, hostIDs []int) error {
	unit := normalizeServiceUnit(filepath.Base(path))
	comps, linesErr := readLines(path)
	if linesErr != nil {
		return linesErr
	}
	comps = rulesimport.DedupTrim(comps)
	if len(comps) == 0 {
		return nil
	}
	ruleName := namePrefix + unit
	ruleID, upErr := sqlite.UpsertSystemdRuleTx(ctx, tx, ruleName, true, unit, "")
	if upErr != nil {
		return upErr
	}
	if setErr := sqlite.SetRuleComponentsTx(ctx, tx, ruleID, comps); setErr != nil {
		return setErr
	}
	if scErr := rulesimport.ApplyHostScope(ctx, tx, ruleID, hostIDs); scErr != nil {
		return scErr
	}
	return nil
}

func normalizeServiceUnit(name string) string {
	if strings.Contains(name, ".") {
		return name
	}
	return name + ".service"
}

func importDockerFileTx(ctx context.Context, tx *sql.Tx, path string, ruleName string, hostIDs []int) error {
	comps, names, labels, rdErr := parseDockerFile(path)
	if rdErr != nil {
		return rdErr
	}
	comps = rulesimport.DedupTrim(comps)
	names = rulesimport.DedupTrim(names)
	labels = rulesimport.DedupTrim(labels)
	if len(comps) == 0 || (len(names) == 0 && len(labels) == 0) {
		return nil
	}
	ruleID, upErr := sqlite.UpsertDockerRuleTx(ctx, tx, ruleName, true, names, labels)
	if upErr != nil {
		return upErr
	}
	if setErr := sqlite.SetRuleComponentsTx(ctx, tx, ruleID, comps); setErr != nil {
		return setErr
	}
	if scErr := rulesimport.ApplyHostScope(ctx, tx, ruleID, hostIDs); scErr != nil {
		return scErr
	}
	return nil
}

func isRegular(e fs.DirEntry) bool {
	if e.Type().IsRegular() {
		return true
	}
	info, err := e.Info()
	return err == nil && info.Mode().IsRegular()
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		out = append(out, s)
	}
	if scErr := sc.Err(); scErr != nil {
		return nil, fmt.Errorf("scan %s: %w", path, scErr)
	}
	return out, nil
}

// parseDockerFile reads the whole file (formats A/B), a deliberate superset of
// the legacy selector's tail -1. Legacy docker files hold a single record, so the
// results match; a multi-line file yields the union rather than the last line.
func parseDockerFile(path string) ([]string, []string, []string, error) {
	lines, rdErr := readAllLinesKeepEmpty(path)
	if rdErr != nil {
		return nil, nil, nil, rdErr
	}
	lines = stripComments(lines)

	sep := indexFirstEmpty(lines)
	if sep >= 0 {
		return parseDockerFormatA(lines, sep)
	}
	return parseDockerFormatB(lines)
}

func readAllLinesKeepEmpty(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	if scErr := sc.Err(); scErr != nil {
		return nil, fmt.Errorf("scan %s: %w", path, scErr)
	}
	return out, nil
}

func stripComments(in []string) []string {
	out := make([]string, 0, len(in))
	for _, ln := range in {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if t == "" {
			out = append(out, "")
			continue
		}
		out = append(out, t)
	}
	return out
}

func indexFirstEmpty(in []string) int {
	for i, s := range in {
		if s == "" {
			return i
		}
	}
	return -1
}

func parseDockerFormatA(lines []string, sep int) ([]string, []string, []string, error) {
	var comps, names, labels []string
	block1 := lines[:sep]
	block2 := lines[sep+1:]

	for _, s := range block1 {
		for _, tok := range strings.Fields(s) {
			if tok != "" {
				comps = append(comps, tok)
			}
		}
	}
	for _, s := range block2 {
		for _, tok := range strings.Fields(s) {
			if tok == "" {
				continue
			}
			if looksLikeLabel(tok) {
				labels = append(labels, tok)
			} else {
				names = append(names, tok)
			}
		}
	}
	comps = rulesimport.DedupTrim(comps)
	names = rulesimport.DedupTrim(names)
	labels = rulesimport.DedupTrim(labels)
	return comps, names, labels, nil
}

func parseDockerFormatB(lines []string) ([]string, []string, []string, error) {
	if len(lines) == 0 {
		return nil, nil, nil, nil
	}
	var comps, names, labels []string
	for _, tok := range strings.Fields(lines[0]) {
		if tok != "" {
			comps = append(comps, tok)
		}
	}
	for _, s := range lines[1:] {
		for _, tok := range strings.Fields(s) {
			if tok == "" {
				continue
			}
			if looksLikeLabel(tok) {
				labels = append(labels, tok)
			} else {
				names = append(names, tok)
			}
		}
	}
	comps = rulesimport.DedupTrim(comps)
	names = rulesimport.DedupTrim(names)
	labels = rulesimport.DedupTrim(labels)
	return comps, names, labels, nil
}

func looksLikeLabel(s string) bool {
	return strings.IndexByte(s, '=') > 0
}

func sameStringsIgnoreOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
