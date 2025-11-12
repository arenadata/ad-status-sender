package importlegacy

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arenadata/ad-status-sender/internal/storage/sqlite"
)

func openDockerTestStore(t *testing.T) (*sqlite.Store, context.Context) {
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

func mkDockerDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "docker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir docker: %v", err)
	}
	return dir
}

func writeDockerFile(t *testing.T, dir, fname string, components, selectors []string) {
	t.Helper()
	full := filepath.Join(dir, fname)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", fname, err)
	}
	var data []byte
	for _, c := range components {
		data = append(data, []byte(c+"\n")...)
	}
	data = append(data, '\n')
	for _, s := range selectors {
		data = append(data, []byte(s+"\n")...)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", fname, err)
	}
}

func TestDockerDir_NamesOnly_Global(t *testing.T) {
	t.Parallel()
	store, ctx := openDockerTestStore(t)
	ddir := mkDockerDir(t)

	writeDockerFile(t, ddir, "core", []string{"601"}, []string{"db", "cache"})

	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return DockerDir(ctx, tx, ddir, "legacy/", nil)
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	rr, err := store.LoadRulesForHost(ctx, 7)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Docker) != 1 {
		t.Fatalf("docker rules = %d, want 1", len(rr.Docker))
	}
	d := rr.Docker[0]
	if !sameStringsIgnoreOrder(d.Components, []string{"601"}) {
		t.Fatalf("components mismatch: %+v", d.Components)
	}
	if !sameStringsIgnoreOrder(d.Containers.Names, []string{"db", "cache"}) {
		t.Fatalf("names mismatch: %+v", d.Containers.Names)
	}
	if len(d.Containers.Labels) != 0 {
		t.Fatalf("labels should be empty, got: %+v", d.Containers.Labels)
	}
}

func TestDockerDir_LabelsOnly_Global(t *testing.T) {
	t.Parallel()
	store, ctx := openDockerTestStore(t)
	ddir := mkDockerDir(t)

	writeDockerFile(t, ddir, "etl", []string{"701"}, []string{"app=etl", "stage=prod"})

	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return DockerDir(ctx, tx, ddir, "", nil)
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	rr, err := store.LoadRulesForHost(ctx, 1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Docker) != 1 {
		t.Fatalf("docker rules = %d, want 1", len(rr.Docker))
	}
	d := rr.Docker[0]
	if !sameStringsIgnoreOrder(d.Components, []string{"701"}) {
		t.Fatalf("components mismatch: %+v", d.Components)
	}
	if len(d.Containers.Names) != 0 {
		t.Fatalf("names should be empty, got: %+v", d.Containers.Names)
	}
	if !sameStringsIgnoreOrder(d.Containers.Labels, []string{"app=etl", "stage=prod"}) {
		t.Fatalf("labels mismatch: %+v", d.Containers.Labels)
	}
}

func TestDockerDir_Mixed_Dedup_Comments(t *testing.T) {
	t.Parallel()
	store, ctx := openDockerTestStore(t)
	ddir := mkDockerDir(t)

	components := []string{"# comment", "1001", "1002", "1001", "", "  ", "# x"}
	selectors := []string{"# s", "api", "api", "role=frontend", "role=frontend", " ", ""}
	writeDockerFile(t, ddir, "web", components, selectors)

	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return DockerDir(ctx, tx, ddir, "", nil)
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	rr, err := store.LoadRulesForHost(ctx, 1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Docker) != 1 {
		t.Fatalf("docker rules = %d, want 1", len(rr.Docker))
	}
	d := rr.Docker[0]
	if !sameStringsIgnoreOrder(d.Components, []string{"1001", "1002"}) {
		t.Fatalf("components=%v, want [1001 1002]", d.Components)
	}
	if !sameStringsIgnoreOrder(d.Containers.Names, []string{"api"}) {
		t.Fatalf("names=%v, want [api]", d.Containers.Names)
	}
	if !sameStringsIgnoreOrder(d.Containers.Labels, []string{"role=frontend"}) {
		t.Fatalf("labels=%v, want [role=frontend]", d.Containers.Labels)
	}
}

func TestDockerDir_ScopedHosts(t *testing.T) {
	t.Parallel()
	store, ctx := openDockerTestStore(t)
	ddir := mkDockerDir(t)

	writeDockerFile(t, ddir, "metrics", []string{"9001"}, []string{"node-exporter"})

	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return DockerDir(ctx, tx, ddir, "legacy/", []int{7, 8})
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	rr7, err7 := store.LoadRulesForHost(ctx, 7)
	if err7 != nil {
		t.Fatalf("load host7: %v", err7)
	}
	rr9, err9 := store.LoadRulesForHost(ctx, 9)
	if err9 != nil {
		t.Fatalf("load host9: %v", err9)
	}
	if len(rr7.Docker) != 1 {
		t.Fatalf("host7 docker=%d, want 1", len(rr7.Docker))
	}
	if len(rr9.Docker) != 0 {
		t.Fatalf("host9 docker=%d, want 0", len(rr9.Docker))
	}
}

func TestDockerDir_EmptyDir(t *testing.T) {
	t.Parallel()
	store, ctx := openDockerTestStore(t)
	ddir := mkDockerDir(t)

	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return DockerDir(ctx, tx, ddir, "", nil)
	}); err != nil {
		t.Fatalf("import empty dir: %v", err)
	}
	rr, err := store.LoadRulesForHost(ctx, 1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Docker) != 0 {
		t.Fatalf("docker=%d, want 0", len(rr.Docker))
	}
}

func TestDockerDir_BrokenFile(t *testing.T) {
	t.Parallel()
	store, ctx := openDockerTestStore(t)
	ddir := mkDockerDir(t)

	p := filepath.Join(ddir, "bad")
	if err := os.WriteFile(p, []byte("100\n\napi\n"), 0o000); err != nil {
		t.Fatalf("prepare unreadable file: %v", err)
	}

	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return DockerDir(ctx, tx, ddir, "", nil)
	}); err == nil {
		t.Fatalf("expected error for unreadable file, got nil")
	}

	rr, err := store.LoadRulesForHost(ctx, 1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Docker) != 0 {
		t.Fatalf("docker=%d, want 0 after failure", len(rr.Docker))
	}
}

func TestDockerDir_CompactFormat(t *testing.T) {
	t.Parallel()
	store, ctx := openDockerTestStore(t)
	ddir := mkDockerDir(t)

	if err := os.WriteFile(filepath.Join(ddir, "adbm"), []byte("5\nadbm_adbm_1\n"), 0o644); err != nil {
		t.Fatalf("write adbm: %v", err)
	}
	names := []string{
		"adcc_adcc_ui_server_1",
		"adcc_adcc_registry_service_1",
		"adcc_adcc_backend_server_1",
		"adcc_planchecker_1",
		"adcc_adcc_scheduler_1",
		"adcc_adcc_migration_1",
		"adcc_database_1",
		"adcc_clickhouse_1",
	}
	if err := os.WriteFile(filepath.Join(ddir, "adcc"), []byte("4\n"+strings.Join(names, " ")+"\n"), 0o644); err != nil {
		t.Fatalf("write adcc: %v", err)
	}

	if err := store.UpdateRules(ctx, func(tx *sql.Tx) error {
		return DockerDir(ctx, tx, ddir, "legacy/", nil)
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	rr, err := store.LoadRulesForHost(ctx, 7)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rr.Docker) != 2 {
		t.Fatalf("docker rules = %d, want 2", len(rr.Docker))
	}

	want := map[string]struct {
		Comps []string
		Names []string
	}{
		"legacy/adbm": {Comps: []string{"5"}, Names: []string{"adbm_adbm_1"}},
		"legacy/adcc": {Comps: []string{"4"}, Names: names},
	}
	seen := make(map[string]bool, len(want))

	for _, d := range rr.Docker {
		w, ok := want[d.Name]
		if !ok {
			continue
		}
		if !sameStringsIgnoreOrder(d.Components, w.Comps) {
			t.Fatalf("%s components=%v want=%v", d.Name, d.Components, w.Comps)
		}
		if !sameStringsIgnoreOrder(d.Containers.Names, w.Names) {
			t.Fatalf("%s names=%v want=%v", d.Name, d.Containers.Names, w.Names)
		}
		if len(d.Containers.Labels) != 0 {
			t.Fatalf("%s labels must be empty, got=%v", d.Name, d.Containers.Labels)
		}
		seen[d.Name] = true
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("rule %q not found", name)
		}
	}
}
