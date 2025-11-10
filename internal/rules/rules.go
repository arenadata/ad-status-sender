package rules

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/goccy/go-yaml"
)

const debounceDelay = 150 * time.Millisecond

type Rules struct {
	Systemd []RuleSystemd `json:"systemd" yaml:"systemd"`
	Docker  []RuleDocker  `json:"docker"  yaml:"docker"`
}

type RuleSystemd struct {
	Unit       string   `json:"unit"       yaml:"unit"`
	UnitGlob   string   `json:"unit_glob"  yaml:"unit_glob"`
	Components []string `json:"components" yaml:"components"`
}

type DockerSelector struct {
	Names  []string `json:"names"  yaml:"names"`
	Labels []string `json:"labels" yaml:"labels"` // "k=v"
}

type RuleDocker struct {
	Name       string         `json:"name"       yaml:"name"`
	Components []string       `json:"components" yaml:"components"`
	Containers DockerSelector `json:"containers" yaml:"containers"`
}

func Load(path string) (Rules, error) {
	var r Rules
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if unErr := yaml.Unmarshal(b, &r); unErr != nil {
		return r, unErr
	}
	return r, nil
}

type Store struct {
	mu    sync.RWMutex
	rules Rules
}

func (s *Store) Get() Rules {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rules
}

func (s *Store) Set(r Rules) {
	s.mu.Lock()
	s.rules = r
	s.mu.Unlock()
}

func Watch(stop <-chan struct{}, path string, apply func(Rules)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	_ = w.Add(path)
	_ = w.Add(filepath.Dir(path))

	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	fire := func() { debounce.Reset(debounceDelay) }

	for {
		select {
		case <-stop:
			return nil
		case ev := <-w.Events:
			if !sameFile(path, ev.Name) {
				continue
			}
			if ev.Has(fsnotify.Write) ||
				ev.Has(fsnotify.Create) ||
				ev.Has(fsnotify.Rename) {
				fire()
			}
		case <-debounce.C:
			r, loadErr := Load(path)
			if loadErr == nil {
				apply(r)
			}
		case <-w.Errors:
		}
	}
}

func sameFile(want, got string) bool {
	return strings.EqualFold(filepath.Base(want), filepath.Base(got))
}
