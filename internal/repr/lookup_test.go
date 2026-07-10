package repr

import (
	"fmt"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
	tbl := NewSymbolTable()
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
	tbl := NewSymbolTable()
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
		sort.Slice(gotIDs, func(a, b int) bool { return gotIDs[a] < gotIDs[b] })

		if fmt.Sprint(gotIDs) != fmt.Sprint(wantIDs) {
			t.Errorf("labels %d (%v): InvertedIndex.Match = %v, want %v", j, lbls, gotIDs, wantIDs)
		}
	}
}

func TestCompileSelector_Errors(t *testing.T) {
	tbl := NewSymbolTable()
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
	tbl := NewSymbolTable()
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

func TestIdentityAllocator_CollapsesDuplicateLabelSets(t *testing.T) {
	tbl := NewSymbolTable()
	alloc := NewIdentityAllocator()

	ls1 := NewLabelSet(map[string]string{"app": "web", "pod-template-hash": "abc"}, tbl)
	ls2 := NewLabelSet(map[string]string{"pod-template-hash": "abc", "app": "web"}, tbl)
	ls3 := NewLabelSet(map[string]string{"app": "web", "pod-template-hash": "def"}, tbl)

	id1 := alloc.Intern(ls1)
	id2 := alloc.Intern(ls2)
	id3 := alloc.Intern(ls3)

	if id1 != id2 {
		t.Errorf("Intern(ls1) = %d, Intern(ls2) = %d, want equal (replica of same identity)", id1, id2)
	}
	if id1 == id3 {
		t.Errorf("Intern(ls1) = Intern(ls3) = %d, want distinct identities", id1)
	}
	if got := alloc.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
	if got := alloc.Get(id1); got.Len() != ls1.Len() {
		t.Errorf("Get(id1).Len() = %d, want %d", got.Len(), ls1.Len())
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

func TestNamespaceSelectorCache_Match(t *testing.T) {
	tbl := NewSymbolTable()
	namespaces := []Namespace{
		NewNamespace(nsObj("prod-a", map[string]string{"tier": "prod"}), tbl),
		NewNamespace(nsObj("prod-b", map[string]string{"tier": "prod"}), tbl),
		NewNamespace(nsObj("dev-a", map[string]string{"tier": "dev"}), tbl),
	}
	cache := NewNamespaceSelectorCache(namespaces, tbl)

	sel := metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}
	got, err := cache.Match(sel)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	var names []string
	for _, id := range got {
		names = append(names, tbl.String(id))
	}
	want := []string{"prod-a", "prod-b"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("Match(tier=prod) = %v, want %v", names, want)
	}

	// Second call must hit the cache and return the same result.
	got2, err := cache.Match(sel)
	if err != nil {
		t.Fatalf("Match (cached): %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(got2) {
		t.Errorf("Match called twice returned different results: %v vs %v", got, got2)
	}

	empty, err := cache.Match(metav1.LabelSelector{MatchLabels: map[string]string{"tier": "staging"}})
	if err != nil {
		t.Fatalf("Match(tier=staging): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Match(tier=staging) = %v, want empty", empty)
	}
}

func nsObj(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}
