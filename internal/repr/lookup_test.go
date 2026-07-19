package repr

import (
	"fmt"
	"slices"
	"testing"

	"github.com/bowei/xnp/internal/intern"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// k8sMatches is the ground-truth selector semantics (what internal/policy's
// RenderBasic uses), used to cross-check CompiledSelector/InvertedIndex.
func k8sMatches(t *testing.T, sel metav1.LabelSelector, lbls map[string]string) bool {
	t.Helper()
	s, err := metav1.LabelSelectorAsSelector(&sel)
	if err != nil {
		t.Fatalf("LabelSelectorAsSelector: %v", err)
	}
	return s.Matches(labels.Set(lbls))
}

func selectorCases() []metav1.LabelSelector {
	return []metav1.LabelSelector{
		{}, // empty selector matches everything
		{MatchLabels: map[string]string{"app": "web"}},
		{MatchLabels: map[string]string{"app": "web", "env": "prod"}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"web"}},
		}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"web", "api"}},
		}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "app", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"web"}},
		}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "tier", Operator: metav1.LabelSelectorOpExists},
		}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: "tier", Operator: metav1.LabelSelectorOpDoesNotExist},
		}},
		{
			MatchLabels: map[string]string{"app": "web"},
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{Key: "tier", Operator: metav1.LabelSelectorOpExists},
				{Key: "env", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"dev", "staging"}},
			},
		},
	}
}

func labelSetCases() []map[string]string {
	return []map[string]string{
		{},
		{"app": "web"},
		{"app": "api"},
		{"app": "web", "env": "prod"},
		{"app": "web", "env": "dev"},
		{"app": "web", "tier": "frontend"},
		{"app": "web", "tier": "frontend", "env": "staging"},
		{"tier": "backend"},
	}
}

func TestCompiledSelector_MatchesK8sSemantics(t *testing.T) {
	tbl := intern.NewTable()
	for i, sel := range selectorCases() {
		cs, err := CompileSelector(sel, tbl)
		if err != nil {
			t.Fatalf("selector %d: CompileSelector: %v", i, err)
		}
		for j, lbls := range labelSetCases() {
			ls := NewLabelSet(lbls, tbl)
			got := cs.Matches(ls)
			want := k8sMatches(t, sel, lbls)
			if got != want {
				t.Errorf("selector %d vs labels %d (%v): CompiledSelector.Matches = %v, k8s = %v", i, j, lbls, got, want)
			}
		}
	}
}

func TestInvertedIndex_MatchesCompiledSelector(t *testing.T) {
	tbl := intern.NewTable()
	cases := selectorCases()
	compiled := make([]CompiledSelector, len(cases))
	for i, sel := range cases {
		cs, err := CompileSelector(sel, tbl)
		if err != nil {
			t.Fatalf("selector %d: CompileSelector: %v", i, err)
		}
		compiled[i] = cs
	}
	idx := BuildInvertedIndex(compiled)

	for j, lbls := range labelSetCases() {
		ls := NewLabelSet(lbls, tbl)

		var wantIDs []SelectorID
		for i, cs := range compiled {
			if cs.Matches(ls) {
				wantIDs = append(wantIDs, SelectorID(i))
			}
		}
		gotIDs := idx.Match(ls)
		slices.Sort(gotIDs)

		if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
			t.Errorf("labels %d (%v): InvertedIndex.Match = %v, want %v", j, lbls, gotIDs, wantIDs)
		}
	}
}

func TestCompileSelector_Errors(t *testing.T) {
	tbl := intern.NewTable()
	cases := []metav1.LabelSelector{
		{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "a", Operator: metav1.LabelSelectorOpIn}}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "a", Operator: metav1.LabelSelectorOpNotIn}}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "a", Operator: "Bogus"}}},
	}
	for i, sel := range cases {
		if _, err := CompileSelector(sel, tbl); err == nil {
			t.Errorf("case %d: CompileSelector: want error, got nil", i)
		}
	}
}

func TestSelectorRegistry_DedupesIdenticalSelectors(t *testing.T) {
	tbl := intern.NewTable()
	reg := NewSelectorRegistry(tbl)

	sel1 := metav1.LabelSelector{MatchLabels: map[string]string{"app": "web", "env": "prod"}}
	sel2 := metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod", "app": "web"}} // same content, different map order
	sel3 := metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}

	id1, err := reg.Get(sel1)
	if err != nil {
		t.Fatalf("Get(sel1): %v", err)
	}
	id2, err := reg.Get(sel2)
	if err != nil {
		t.Fatalf("Get(sel2): %v", err)
	}
	id3, err := reg.Get(sel3)
	if err != nil {
		t.Fatalf("Get(sel3): %v", err)
	}

	if id1 != id2 {
		t.Errorf("Get(sel1) = %d, Get(sel2) = %d, want equal (identical content)", id1, id2)
	}
	if id1 == id3 {
		t.Errorf("Get(sel1) = Get(sel3) = %d, want distinct (different content)", id1)
	}
	if got := len(reg.All()); got != 2 {
		t.Errorf("len(All()) = %d, want 2", got)
	}
}

func TestBitset_SetTest(t *testing.T) {
	b := NewBitset(130) // exercise more than one uint64 word
	if b.Test(5) || b.Test(64) || b.Test(129) {
		t.Fatalf("fresh Bitset has a set bit")
	}
	b.Set(5)
	b.Set(64)
	b.Set(129)
	for _, i := range []uint32{5, 64, 129} {
		if !b.Test(i) {
			t.Errorf("Test(%d) = false after Set(%d)", i, i)
		}
	}
	if b.Test(6) {
		t.Errorf("Test(6) = true, want false")
	}
	// Out of range reads are a common way this gets misused when bitsets are
	// sized for one selector count and probed with another; must not panic.
	if b.Test(1000) {
		t.Errorf("Test(1000) = true, want false")
	}
}
