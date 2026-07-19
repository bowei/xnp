package labels

import (
	"testing"

	"github.com/bowei/xnp/internal/intern"
)

func TestMake(t *testing.T) {
	tbl := intern.NewTable()
	ls := Make(map[string]string{"app": "web", "env": "prod"}, tbl)

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

func TestMake_SortedOrderIsCanonical(t *testing.T) {
	tbl := intern.NewTable()
	labels := map[string]string{"z": "1", "a": "2", "m": "3"}

	// map iteration order is randomized by the runtime; building the same
	// LabelSet repeatedly must still produce an identical sorted slice, or
	// downstream hashing/identity collapsing (lookup.go) would silently
	// treat identical label sets as distinct.
	first := Make(labels, tbl)
	for i := range 10 {
		got := Make(labels, tbl)
		if got.Len() != first.Len() {
			t.Fatalf("iteration %d: Len() = %d, want %d", i, got.Len(), first.Len())
		}
		for j := 0; j < got.Len(); j++ {
			gotKey, gotVal := got.At(j)
			wantKey, wantVal := first.At(j)
			if gotKey != wantKey || gotVal != wantVal {
				t.Fatalf("iteration %d: At(%d) = (%d, %d), want (%d, %d)", i, j, gotKey, gotVal, wantKey, wantVal)
			}
		}
	}
}

func TestLabelSet_HashEqual(t *testing.T) {
	tbl := intern.NewTable()
	a := Make(map[string]string{"app": "web", "env": "prod"}, tbl)
	b := Make(map[string]string{"env": "prod", "app": "web"}, tbl)
	c := Make(map[string]string{"app": "api"}, tbl)

	if !a.Equal(b) {
		t.Errorf("Equal(a, b) = false, want true (same content, different map order)")
	}
	if a.Hash() != b.Hash() {
		t.Errorf("Hash(a) != Hash(b), want equal")
	}
	if a.Equal(c) {
		t.Errorf("Equal(a, c) = true, want false")
	}
}
