package policy

import (
	"sort"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SelectorID is a dense integer identity for a compiled, deduped selector.
type SelectorID uint32

// selectorRegistry compiles metav1.LabelSelectors exactly once, deduping
// identical selectors by their canonical content (doc/optimizations.md
// "Label optimizations" #6: LabelSelectorAsSelector allocates and sorts on
// every call; this compiles once and reuses the result). Real and generated
// policy sets alike reuse the same handful of selectors across many rules,
// so this also keeps the inverted index built from registry.compiled small.
type selectorRegistry struct {
	tbl      *symbolTable
	byKey    map[string]SelectorID
	compiled []compiledSelector
}

func newSelectorRegistry(tbl *symbolTable) *selectorRegistry {
	return &selectorRegistry{tbl: tbl, byKey: map[string]SelectorID{}}
}

func (r *selectorRegistry) get(sel metav1.LabelSelector) (SelectorID, error) {
	key := canonicalKey(sel)
	if id, ok := r.byKey[key]; ok {
		return id, nil
	}
	cs, err := compileSelector(sel, r.tbl)
	if err != nil {
		return 0, err
	}
	id := SelectorID(len(r.compiled))
	r.compiled = append(r.compiled, cs)
	r.byKey[key] = id
	return id, nil
}

// bitset is a fixed-size bit vector, used to store per-identity match
// results (doc/optimizations.md "Label optimizations" #8) so membership
// tests downstream are a single word AND instead of a set/map lookup.
type bitset []uint64

func newBitset(n int) bitset {
	return make(bitset, (n+63)/64)
}

func (b bitset) set(i uint32) {
	b[i/64] |= 1 << (i % 64)
}

func (b bitset) test(i uint32) bool {
	if int(i/64) >= len(b) {
		return false
	}
	return b[i/64]&(1<<(i%64)) != 0
}

// invertedIndex maps label key/value pairs (and bare keys, for Exists
// requirements) to the selectors that require them, so matching a label set
// costs O(label-set size) index probes instead of O(selector count) full
// selector evaluations. This is Calico Felix's labelindex approach (doc
// "Label optimizations" #3).
type invertedIndex struct {
	byExact map[uint64][]SelectorID
	byKey   map[uint32][]SelectorID // Exists requirements, indexed by key alone
	// scanAlways holds selectors with zero positive (exact/Exists)
	// requirements — an empty selector, or one built purely from
	// NotIn/DoesNotExist — which can't be found by indexing a label set's
	// positive content and so must always be residual-checked directly.
	// These are rare in practice.
	scanAlways []SelectorID
	required   []int // positiveCount() per SelectorID
	compiled   []compiledSelector
}

func buildInvertedIndex(compiled []compiledSelector) *invertedIndex {
	idx := &invertedIndex{
		byExact:  map[uint64][]SelectorID{},
		byKey:    map[uint32][]SelectorID{},
		required: make([]int, len(compiled)),
		compiled: compiled,
	}
	for i, cs := range compiled {
		sid := SelectorID(i)
		idx.required[i] = cs.positiveCount()
		for _, e := range cs.exact {
			idx.byExact[e] = append(idx.byExact[e], sid)
		}
		for _, k := range cs.existsKeys {
			idx.byKey[k] = append(idx.byKey[k], sid)
		}
		if cs.positiveCount() == 0 {
			idx.scanAlways = append(idx.scanAlways, sid)
		}
	}
	return idx
}

// match returns the IDs of every selector that matches ls: a counting join
// over the inverted index (a selector matches iff all of its positive
// requirements are found) followed by a residual check on the survivors.
func (idx *invertedIndex) match(ls internedLabelSet) []SelectorID {
	counts := map[SelectorID]int{}
	for _, e := range ls.sorted {
		for _, sid := range idx.byExact[e] {
			counts[sid]++
		}
	}
	for k := range ls.kv {
		for _, sid := range idx.byKey[k] {
			counts[sid]++
		}
	}

	var matched []SelectorID
	for sid, c := range counts {
		if c == idx.required[sid] && idx.compiled[sid].matchesResidual(ls) {
			matched = append(matched, sid)
		}
	}
	for _, sid := range idx.scanAlways {
		if idx.compiled[sid].matchesResidual(ls) {
			matched = append(matched, sid)
		}
	}
	return matched
}

// namespaceSelectorCache memoizes NamespaceSelector matches against the
// (always small) namespace set, keyed by the selector's canonical content,
// so an identical NamespaceSelector reused across many rules — common both
// in generated and real-world policy sets — is only evaluated once (doc
// "Label optimizations" #5 and #6).
// namespaceSelectorCache's match cache is lazily populated and shared across
// every group RenderIndexed(Parallel) resolves, so concurrent callers (from
// RenderIndexedParallel's group-template workers) need mu to guard reads and
// writes; RenderIndexed's single-threaded loop pays its uncontended cost.
type namespaceSelectorCache struct {
	tbl        *symbolTable
	namespaces []internedLabelSet
	names      []string

	mu    sync.RWMutex
	cache map[string][]string
}

func newNamespaceSelectorCache(namespaces []*corev1.Namespace, tbl *symbolTable) *namespaceSelectorCache {
	c := &namespaceSelectorCache{tbl: tbl, cache: map[string][]string{}}
	for _, ns := range namespaces {
		c.namespaces = append(c.namespaces, internSet(ns.Labels, tbl))
		c.names = append(c.names, ns.Name)
	}
	return c
}

func (c *namespaceSelectorCache) match(sel metav1.LabelSelector) ([]string, error) {
	key := canonicalKey(sel)

	c.mu.RLock()
	names, ok := c.cache[key]
	c.mu.RUnlock()
	if ok {
		return names, nil
	}

	cs, err := compileSelector(sel, c.tbl)
	if err != nil {
		return nil, err
	}
	var matched []string
	for i, ls := range c.namespaces {
		if cs.matches(ls) {
			matched = append(matched, c.names[i])
		}
	}
	sort.Strings(matched)

	c.mu.Lock()
	c.cache[key] = matched
	c.mu.Unlock()
	return matched, nil
}
