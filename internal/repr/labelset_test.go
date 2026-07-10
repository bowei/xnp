package repr

import (
	"testing"
)

func TestNewLabelSet(t *testing.T) {
	tbl := NewSymbolTable()
	ls := NewLabelSet(map[string]string{"app": "web", "env": "prod"}, tbl)

	if got := ls.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	appKey := tbl.Intern("app")
	v, ok := ls.Get(appKey)
	if !ok {
		t.Fatalf("Get(app) missing")
	}
	if got := tbl.String(v); got != "web" {
		t.Errorf("Get(app) = %q, want \"web\"", got)
	}
	if _, ok := ls.Get(tbl.Intern("missing")); ok {
		t.Errorf("Get(missing) = ok, want !ok")
	}
}

func TestNewLabelSet_SortedOrderIsCanonical(t *testing.T) {
	tbl := NewSymbolTable()
	labels := map[string]string{"z": "1", "a": "2", "m": "3"}

	// map iteration order is randomized by the runtime; building the same
	// LabelSet repeatedly must still produce an identical sorted slice, or
	// downstream hashing/identity collapsing (lookup.go) would silently
	// treat identical label sets as distinct.
	first := NewLabelSet(labels, tbl)
	for i := range 10 {
		got := NewLabelSet(labels, tbl)
		if len(got.sorted) != len(first.sorted) {
			t.Fatalf("iteration %d: sorted length = %d, want %d", i, len(got.sorted), len(first.sorted))
		}
		for j := range got.sorted {
			if got.sorted[j] != first.sorted[j] {
				t.Fatalf("iteration %d: sorted[%d] = %d, want %d", i, j, got.sorted[j], first.sorted[j])
			}
		}
	}
}
