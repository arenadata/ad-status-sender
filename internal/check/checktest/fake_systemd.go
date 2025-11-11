package checktest

import "context"

type FakeSystemd struct {
	Units map[string]bool
	Globs map[string][]string
}

func (f *FakeSystemd) SystemdStatus(_ context.Context, unit string) int {
	if ok := f.Units[unit]; ok {
		if ok {
			return 0
		}
		return 1
	}
	return 1
}
func (f *FakeSystemd) ExpandUnitsByGlob(_ context.Context, glob string) []string {
	return append([]string(nil), f.Globs[glob]...)
}
