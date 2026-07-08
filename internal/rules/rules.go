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

// ComponentTarget is a component the daemon must report status for. HostID 0
// means the rule's scoped host (yaml/legacy); a non-zero HostID names an ADCM
// shared-host duplicate whose component status must be posted under that id.
type ComponentTarget struct {
	HostID      int    `json:"host_id,omitempty" yaml:"host_id,omitempty"`
	ComponentID string `json:"component_id"      yaml:"component_id"`
}

type RuleSystemd struct {
	Name       string   `json:"name"       yaml:"name"`
	Unit       string   `json:"unit"       yaml:"unit"`
	UnitGlob   string   `json:"unit_glob"  yaml:"unit_glob"`
	Components []string `json:"components" yaml:"components"`
	// ComponentTargets carries per-host targeting for shared hosts. When empty,
	// Components is used with HostID 0. Populated by the DB loader, not by yaml.
	ComponentTargets []ComponentTarget `json:"component_targets,omitempty" yaml:"component_targets,omitempty"`
}

func (r RuleSystemd) Targets() []ComponentTarget {
	return EffectiveTargets(r.Components, r.ComponentTargets)
}

type DockerSelector struct {
	Names  []string `json:"names"  yaml:"names"`
	Labels []string `json:"labels" yaml:"labels"` // "k=v"
}

type RuleDocker struct {
	Name             string            `json:"name"                        yaml:"name"`
	Components       []string          `json:"components"                  yaml:"components"`
	Containers       DockerSelector    `json:"containers"                  yaml:"containers"`
	ComponentTargets []ComponentTarget `json:"component_targets,omitempty" yaml:"component_targets,omitempty"`
}

func (r RuleDocker) Targets() []ComponentTarget {
	return EffectiveTargets(r.Components, r.ComponentTargets)
}

// EffectiveTargets prefers explicit per-host targets; otherwise it maps each
// component string to HostID 0 (the rule's scoped host).
func EffectiveTargets(components []string, targets []ComponentTarget) []ComponentTarget {
	if len(targets) > 0 {
		return targets
	}
	out := make([]ComponentTarget, 0, len(components))
	for _, c := range components {
		if c == "" {
			continue
		}
		out = append(out, ComponentTarget{ComponentID: c})
	}
	return out
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

func Watch(stop <-chan struct{}, path string, apply func(Rules), onErr func(error)) error {
	if onErr == nil {
		onErr = func(error) {}
	}
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
			if loadErr != nil {
				onErr(loadErr)
				continue
			}
			apply(r)
		case werr := <-w.Errors:
			if werr != nil {
				onErr(werr)
			}
		}
	}
}

func sameFile(want, got string) bool {
	return strings.EqualFold(filepath.Base(want), filepath.Base(got))
}
