package checktest

import (
	"context"
	"strings"
)

type FakeDocker struct {
	Names       map[string]bool
	LabelGroups map[string][]bool
}

func (f *FakeDocker) AllRunningNames(_ context.Context, names []string) int {
	if len(names) == 0 {
		return 1
	}
	for _, n := range names {
		running, found := f.Names[n]
		if !found || !running {
			return 1
		}
	}
	return 0
}

func (f *FakeDocker) AllRunningByLabels(_ context.Context, labels []string) int {
	if len(labels) == 0 {
		return 1
	}
	key := strings.Join(labels, ",")
	vals, ok := f.LabelGroups[key]
	if !ok || len(vals) == 0 {
		return 1
	}
	for _, v := range vals {
		if !v {
			return 1
		}
	}
	return 0
}
