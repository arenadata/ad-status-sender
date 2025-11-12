package importlegacy

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arenadata/ad-status-sender/internal/storage/sqlite"
)

/* helpers */

func openTestStore(t *testing.T) (*sqlite.Store, context.Context) {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "rules.db")
	s, err := sqlite.Open(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return s, ctx
}

func mkServicesDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "services")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	return dir
}

func writeServicesFiles(t *testing.T, dir string, data map[string]string) {
	t.Helper()
	for name, content := range data {
		full := filepath.Join(dir, name)
		if mkErr := os.MkdirAll(filepath.Dir(full), 0o755); mkErr != nil {
			t.Fatalf("mkdir for %s: %v", name, mkErr)
		}
		if wErr := os.WriteFile(full, []byte(content+"\n"), 0o644); wErr != nil {
			t.Fatalf("write %s: %v", name, wErr)
		}
	}
}

/* tests */

func TestServicesDir_GlobalBasic(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	sdir := mkServicesDir(t)

	if err := os.WriteFile(filepath.Join(sdir, "nginx.service"), []byte("1761\n"), 0o644); err != nil {
		t.Fatalf("write nginx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sdir, "hbase-regionserver.service"), []byte("1732\n"), 0o644); err != nil {
		t.Fatalf("write hbase: %v", err)
	}

	err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return ServicesDir(ctx, tx, sdir, "legacy/", nil)
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	rr, err := store.LoadRulesForHost(ctx, 7)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Systemd) != 2 {
		t.Fatalf("got %d systemd, want 2", len(rr.Systemd))
	}

	want := map[string]string{
		"nginx.service":              "1761",
		"hbase-regionserver.service": "1732",
	}
	got := map[string]string{}
	for _, r := range rr.Systemd {
		if len(r.Components) == 1 {
			got[r.Unit] = r.Components[0]
		}
	}
	if got["nginx.service"] != want["nginx.service"] ||
		got["hbase-regionserver.service"] != want["hbase-regionserver.service"] {
		t.Fatalf("content mismatch: got=%v want=%v", got, want)
	}
}

func TestServicesDir_ScopedHosts(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	sdir := mkServicesDir(t)

	if err := os.WriteFile(filepath.Join(sdir, "ozone-scm.service"), []byte("1763\n"), 0o644); err != nil {
		t.Fatalf("write ozone-scm: %v", err)
	}

	err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return ServicesDir(ctx, tx, sdir, "legacy/", []int{7, 8})
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	rr7, err := store.LoadRulesForHost(ctx, 7)
	if err != nil {
		t.Fatalf("load host7: %v", err)
	}
	rr9, err := store.LoadRulesForHost(ctx, 9)
	if err != nil {
		t.Fatalf("load host9: %v", err)
	}

	if len(rr7.Systemd) != 1 {
		t.Fatalf("host7: got %d systemd, want 1", len(rr7.Systemd))
	}
	if len(rr9.Systemd) != 0 {
		t.Fatalf("host9: got %d systemd, want 0 (no scope)", len(rr9.Systemd))
	}
}

func TestServicesDir_MultiComponents_Dedup_Comments(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	sdir := mkServicesDir(t)

	content := "# comment\n\n  1001 \n1002\n1001\n   \n# another\n"
	if err := os.WriteFile(filepath.Join(sdir, "trino-worker.service"), []byte(content), 0o644); err != nil {
		t.Fatalf("write trino: %v", err)
	}

	err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return ServicesDir(ctx, tx, sdir, "", nil)
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	rr, err := store.LoadRulesForHost(ctx, 42)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Systemd) != 1 {
		t.Fatalf("got %d systemd, want 1", len(rr.Systemd))
	}
	r := rr.Systemd[0]
	if r.Unit != "trino-worker.service" {
		t.Fatalf("unit=%q, want trino-worker.service", r.Unit)
	}
	if !sameStringsIgnoreOrder(r.Components, []string{"1001", "1002"}) {
		t.Fatalf("components=%v, want [1001 1002] (order free)", r.Components)
	}
}

func TestServicesDir_IgnoresNonRegularFiles(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	sdir := mkServicesDir(t)

	if err := os.Mkdir(filepath.Join(sdir, "zookeeper"), 0o755); err != nil {
		t.Fatalf("mkdir zookeeper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sdir, "nginx.service"), []byte("1761\n"), 0o644); err != nil {
		t.Fatalf("write nginx: %v", err)
	}

	err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return ServicesDir(ctx, tx, sdir, "", nil)
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	rr, err := store.LoadRulesForHost(ctx, 1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Systemd) != 1 || rr.Systemd[0].Unit != "nginx.service" {
		t.Fatalf("expected only nginx.service, got: %+v", rr.Systemd)
	}
}

func TestServicesDir_EmptyDir(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	sdir := mkServicesDir(t)

	err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return ServicesDir(ctx, tx, sdir, "legacy/", nil)
	})
	if err != nil {
		t.Fatalf("import (empty dir) returned error: %v", err)
	}

	rr, loadErr := store.LoadRulesForHost(ctx, 123)
	if loadErr != nil {
		t.Fatalf("load: %v", loadErr)
	}
	if len(rr.Systemd) != 0 {
		t.Fatalf("got %d systemd rules, want 0 for empty dir", len(rr.Systemd))
	}
}

func TestServicesDir_BrokenFile(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	sdir := mkServicesDir(t)

	p := filepath.Join(sdir, "nginx.service")
	if writeErr := os.WriteFile(p, []byte("1761\n"), 0o000); writeErr != nil {
		t.Fatalf("prepare broken file: %v", writeErr)
	}

	err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return ServicesDir(ctx, tx, sdir, "", nil)
	})
	if err == nil {
		t.Fatalf("expected error on unreadable file, got nil")
	}

	rr, loadErr := store.LoadRulesForHost(ctx, 7)
	if loadErr != nil {
		t.Fatalf("load: %v", loadErr)
	}
	if len(rr.Systemd) != 0 {
		t.Fatalf("got %d systemd rules, want 0 after failure", len(rr.Systemd))
	}
}

func TestServicesDir_FullListing(t *testing.T) {
	t.Parallel()

	dbdir := t.TempDir()
	dsn := "file:" + filepath.Join(dbdir, "rules.db")
	store, openErr := sqlite.Open(dsn)
	if openErr != nil {
		t.Fatalf("open store: %v", openErr)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	sdir := filepath.Join(dbdir, "services")
	if mkErr := os.MkdirAll(sdir, 0o755); mkErr != nil {
		t.Fatalf("mkdir services: %v", mkErr)
	}

	files := map[string]string{
		"hadoop-hdfs-datanode.service":        "1736",
		"hadoop-hdfs-journalnode.service":     "1738",
		"hadoop-yarn-nodemanager.service":     "1752",
		"hadoop-yarn-resourcemanager.service": "1753",
		"hbase-regionserver.service":          "1732",
		"impala-impalad.service":              "1757",
		"kyuubi-server.service":               "1760",
		"nginx.service":                       "1761",
		"ozone-datanode.service":              "1764",
		"ozone-om.service":                    "1762",
		"ozone-scm.service":                   "1763",
		"phoenix-queryserver.service":         "1733",
		"trino-worker.service":                "1770",
		"zookeeper":                           "1773",
	}
	writeServicesFiles(t, sdir, files)

	importErr := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return ServicesDir(ctx, tx, sdir, "legacy/", nil)
	})
	if importErr != nil {
		t.Fatalf("import: %v", importErr)
	}

	rr, loadErr := store.LoadRulesForHost(ctx, 7)
	if loadErr != nil {
		t.Fatalf("load rules: %v", loadErr)
	}
	if len(rr.Systemd) != len(files) {
		t.Fatalf("got %d systemd rules, want %d", len(rr.Systemd), len(files))
	}

	got := map[string]string{}
	for _, r := range rr.Systemd {
		if len(r.Components) == 1 {
			got[r.Unit] = r.Components[0]
		}
	}
	for unit, comp := range files {
		if got[unit] != comp {
			t.Fatalf("unit %q: got component %q, want %q", unit, got[unit], comp)
		}
	}
}
