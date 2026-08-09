package rlabels

import (
	"fmt"
	"testing"

	"k8s.io/utils/set"
)

func testRLabels(l map[string][]string) *RLabels {
	rl := &RLabels{l: PerNsLabels{}}
	for ns, labels := range l {
		rl.l[ns] = set.New(labels...)
	}
	return rl
}

// checkPerNs compares one category of a Diff against the labels it should
// carry. A nil want asserts the map was never allocated, not merely that it is
// empty: the category maps are documented to stay nil until something lands in
// them, and an empty non-nil map would read the same to len() while telling a
// caller that iterates it a different story.
func checkPerNs(t *testing.T, name string, got PerNsLabels, want map[string][]string) {
	t.Helper()

	if want == nil {
		if got != nil {
			t.Errorf("%s = %v, want nil", name, sorted(got))
		}
		return
	}
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", name, sorted(got), want)
		return
	}
	for ns, labels := range want {
		gotLabels, ok := got[ns]
		if !ok {
			t.Errorf("%s = %v, want an entry for ns %q", name, sorted(got), ns)
			continue
		}
		if !gotLabels.Equal(set.New(labels...)) {
			t.Errorf("%s[%q] = %v, want %v", name, ns, gotLabels.SortedList(), labels)
		}
	}
}

// sorted renders a PerNsLabelSet deterministically, so a failure names the
// labels that differ rather than printing them in map order.
func sorted(pls PerNsLabels) string {
	out := map[string][]string{}
	for ns, labels := range pls {
		out[ns] = labels.SortedList()
	}
	return fmt.Sprint(out)
}

// checkRLabels compares a built RLabels against the namespaces it should
// carry, reusing the Diff checker: a ns the builder should not have recorded
// shows up as an Added the want side has no entry for.
func checkRLabels(t *testing.T, name string, got *RLabels, want map[string][]string) {
	t.Helper()

	if got, want := len(got.l), len(want); got != want {
		t.Errorf("len(%s) = %v, want %v", name, got, want)
	}
	checkPerNs(t, name, got.l, want)
}

func TestBuilder(t *testing.T) {
	for _, tc := range []struct {
		desc   string
		merges map[string][][]string
		want   map[string][]string
	}{
		{desc: "nothing merged"},
		{
			desc:   "one ns",
			merges: map[string][][]string{"ns1": {{"app"}}},
			want:   map[string][]string{"ns1": {"app"}},
		},
		{
			desc: "repeated merges union",
			merges: map[string][][]string{
				"ns1": {{"app"}, {"tier", "app"}, {"role"}},
				"ns2": {{"app"}},
			},
			want: map[string][]string{"ns1": {"app", "tier", "role"}, "ns2": {"app"}},
		},
		{
			// A policy whose selectors name no keys leaves the ns unrecorded
			// rather than recorded as empty, since the two mean the same thing
			// to a consumer projecting pods.
			desc:   "empty merge does not register the ns",
			merges: map[string][][]string{"ns1": {{}, nil}},
		},
		{
			desc:   "empty merge leaves an existing ns alone",
			merges: map[string][][]string{"ns1": {{"app"}, {}}},
			want:   map[string][]string{"ns1": {"app"}},
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			var b Builder
			for ns, merges := range tc.merges {
				for _, labels := range merges {
					b.Merge(ns, set.New(labels...))
				}
			}
			checkRLabels(t, "Build()", b.Build(), tc.want)
		})
	}
}

func TestBuilderDoesNotAliasMergedSets(t *testing.T) {
	labels := set.New("app")

	var b Builder
	b.Merge("ns1", labels)
	rl := b.Build()

	// The caller keeps its set and is entitled to reuse it; the built RLabels
	// is the immutable side of every later comparison and must not follow it.
	labels.Insert("tier")
	checkRLabels(t, "Build()", rl, map[string][]string{"ns1": {"app"}})
}

func TestBuilderResetsOnBuild(t *testing.T) {
	var b Builder
	b.Merge("ns1", set.New("app"))
	first := b.Build()

	// A second round starts empty rather than continuing the first, and
	// writing a ns the first round also has does not reach back into it.
	b.Merge("ns1", set.New("tier"))
	b.Merge("ns2", set.New("role"))
	second := b.Build()

	checkRLabels(t, "first Build()", first, map[string][]string{"ns1": {"app"}})
	checkRLabels(t, "second Build()", second, map[string][]string{"ns1": {"tier"}, "ns2": {"role"}})
}

func TestBuilderDifference(t *testing.T) {
	var b Builder
	b.Merge("ns1", set.New("app"))
	b.Merge("ns2", set.New("role"))
	old := b.Build()

	b.Merge("ns1", set.New("app"))
	b.Merge("ns3", set.New("tier"))
	if d := b.Build().Difference(old); d == nil {
		t.Fatalf("Difference() = nil, want a diff")
	} else {
		checkPerNs(t, "Added", d.Added, map[string][]string{"ns3": {"tier"}})
		checkPerNs(t, "Removed", d.Removed, map[string][]string{"ns2": {"role"}})
		checkPerNs(t, "Changed", d.Changed, nil)
	}
}

func TestDifferenceNoDifference(t *testing.T) {
	for _, tc := range []struct {
		desc string
		l    map[string][]string
	}{
		{desc: "empty"},
		{desc: "populated", l: map[string][]string{"ns1": {"app"}, "ns2": {"tier"}}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			rl, same := testRLabels(tc.l), testRLabels(tc.l)
			if got := rl.Difference(same); got != nil {
				t.Errorf("Difference() = %+v, want nil", got)
			}
			if got := rl.Difference(rl); got != nil {
				t.Errorf("Difference(self) = %+v, want nil", got)
			}
		})
	}
}

func TestDifference(t *testing.T) {
	newRL := testRLabels(map[string][]string{
		"same":    {"app"},
		"grew":    {"app", "tier"},
		"shrank":  {"app"},
		"arrived": {"role"},
	})
	oldRL := testRLabels(map[string][]string{
		"same":   {"app"},
		"grew":   {"app"},
		"shrank": {"app", "tier"},
		"left":   {"role"},
	})

	d := newRL.Difference(oldRL)
	if d == nil {
		t.Fatalf("Difference() = nil, want a diff")
	}
	checkPerNs(t, "Added", d.Added, map[string][]string{"arrived": {"role"}})
	// Removed carries the labels the ns had on the side it is leaving, which is
	// the only place they still exist.
	checkPerNs(t, "Removed", d.Removed, map[string][]string{"left": {"role"}})
	// Changed carries the new set whole, in both directions of change: a ns that
	// lost a key is not distinguishable here from one that gained one, because
	// what a consumer re-projects onto is the set that survived.
	checkPerNs(t, "Changed", d.Changed, map[string][]string{
		"grew":   {"app", "tier"},
		"shrank": {"app"},
	})

	// Reversing the arguments has to swap Added and Removed and leave Changed
	// naming the same namespaces, now carrying the other side's labels.
	rev := oldRL.Difference(newRL)
	if rev == nil {
		t.Fatalf("Difference() reversed = nil, want a diff")
	}
	checkPerNs(t, "reversed Added", rev.Added, map[string][]string{"left": {"role"}})
	checkPerNs(t, "reversed Removed", rev.Removed, map[string][]string{"arrived": {"role"}})
	checkPerNs(t, "reversed Changed", rev.Changed, map[string][]string{
		"grew":   {"app"},
		"shrank": {"app", "tier"},
	})
}

func TestDifferenceLeavesEmptyCategoriesNil(t *testing.T) {
	populated := testRLabels(map[string][]string{"ns1": {"app"}, "ns2": {"tier"}})
	empty := testRLabels(nil)

	// Everything against nothing is all Added and nothing else, which is the
	// shape the first diff after startup takes.
	d := populated.Difference(empty)
	if d == nil {
		t.Fatalf("Difference(empty) = nil, want a diff")
	}
	checkPerNs(t, "Added", d.Added, map[string][]string{"ns1": {"app"}, "ns2": {"tier"}})
	checkPerNs(t, "Removed", d.Removed, nil)
	checkPerNs(t, "Changed", d.Changed, nil)

	d = empty.Difference(populated)
	if d == nil {
		t.Fatalf("empty.Difference() = nil, want a diff")
	}
	checkPerNs(t, "Added", d.Added, nil)
	checkPerNs(t, "Removed", d.Removed, map[string][]string{"ns1": {"app"}, "ns2": {"tier"}})
	checkPerNs(t, "Changed", d.Changed, nil)
}

func TestDifferenceDoesNotModifyOperands(t *testing.T) {
	newRL := testRLabels(map[string][]string{"ns1": {"app", "tier"}})
	oldRL := testRLabels(map[string][]string{"ns1": {"app"}, "ns2": {"role"}})

	d := newRL.Difference(oldRL)
	if d == nil {
		t.Fatalf("Difference() = nil, want a diff")
	}
	// The diff hands back the operands' own sets rather than copies. Nothing in
	// the diff may write through them, and a consumer that does is corrupting
	// the store it just read -- the check is here so the aliasing stays a
	// deliberate choice and not something a later rewrite silently relies on.
	checkPerNs(t, "Changed", d.Changed, map[string][]string{"ns1": {"app", "tier"}})
	checkPerNs(t, "Removed", d.Removed, map[string][]string{"ns2": {"role"}})

	if got, want := newRL.l["ns1"], set.New("app", "tier"); !got.Equal(want) {
		t.Errorf("ForNS(ns1) = %v after Difference(), want %v", got.SortedList(), want.SortedList())
	}
	if got, want := oldRL.l["ns1"], set.New("app"); !got.Equal(want) {
		t.Errorf("other.ForNS(ns1) = %v after Difference(), want %v", got.SortedList(), want.SortedList())
	}
	if got, want := len(newRL.l), 1; got != want {
		t.Errorf("len(rl.l) = %v after Difference(), want %v", got, want)
	}
	if got, want := len(oldRL.l), 2; got != want {
		t.Errorf("len(other.l) = %v after Difference(), want %v", got, want)
	}
}
