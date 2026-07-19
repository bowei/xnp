package intern

import (
	"testing"
)

func TestTable_InternDedupesAndRoundTrips(t *testing.T) {
	tbl := NewTable()
	a := tbl.Intern("foo")
	b := tbl.Intern("bar")
	c := tbl.Intern("foo")

	if a != c {
		t.Fatalf("Intern(\"foo\") = %d, then %d; want equal IDs for equal strings", a, c)
	}
	if a == b {
		t.Fatalf("Intern(\"foo\") and Intern(\"bar\") collided: %d", a)
	}
	if got := tbl.String(a); got != "foo" {
		t.Errorf("String(%d) = %q, want \"foo\"", a, got)
	}
	if got := tbl.String(b); got != "bar" {
		t.Errorf("String(%d) = %q, want \"bar\"", b, got)
	}
}
