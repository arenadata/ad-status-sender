package rules

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadAndStore(t *testing.T) {
	data := []byte(`
systemd:
  - unit: "nginx.service"
    components: ["1","2"]
docker:
  - name: "web"
    components: ["3"]
    containers:
      names: ["nginx"]
`)
	fn := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(fn, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(fn)
	if err != nil {
		t.Fatalf("load err: %v", err)
	}
	if len(r.Systemd) != 1 || len(r.Docker) != 1 {
		t.Fatalf("unexpected content: %+v", r)
	}

	var s Store
	s.Set(r)
	got := s.Get()
	if len(got.Systemd) != 1 || got.Systemd[0].Unit != "nginx.service" {
		t.Fatalf("store/get mismatch: %+v", got)
	}
}

func TestWatchHotReload(t *testing.T) {
	dir := t.TempDir()
	fn := filepath.Join(dir, "rules.yaml")

	initial := []byte(`systemd: []`)
	if err := os.WriteFile(fn, initial, 0o644); err != nil {
		t.Fatal(err)
	}

	var applied int32
	stop := make(chan struct{})
	defer close(stop)

	// start watcher
	go func() {
		_ = Watch(stop, fn, func(Rules) {
			atomic.AddInt32(&applied, 1)
		})
	}()

	// give watcher time to init
	time.Sleep(100 * time.Millisecond)

	// modify file
	updated := []byte(`
systemd:
  - unit: "a.service"
    components: ["1"]
`)
	if err := os.WriteFile(fn, updated, 0o644); err != nil {
		t.Fatal(err)
	}

	// allow debounceDelay (150ms) + margin
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&applied) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&applied) == 0 {
		t.Fatalf("watcher did not apply changes")
	}
}
