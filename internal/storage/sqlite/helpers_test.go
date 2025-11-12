package sqlite

import "testing"

func TestB2I(t *testing.T) {
	if b2i(true) != 1 {
		t.Fatalf("b2i(true) != 1")
	}
	if b2i(false) != 0 {
		t.Fatalf("b2i(false) != 0")
	}
}

func TestNilIfEmpty(t *testing.T) {
	if got := nilIfEmpty(""); got != nil {
		t.Fatalf("nilIfEmpty(\"\") = %#v, want nil", got)
	}
	if got := nilIfEmpty("x"); got == nil {
		t.Fatalf("nilIfEmpty(\"x\") = nil, want non-nil")
	}
}
