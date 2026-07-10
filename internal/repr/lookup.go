// Fast label-selector matching over repr's storage types: compiled
// selectors, an inverted index, bitmap match results, and identity
// collapsing — a repr-typed clone of internal/policy's selector.go/
// identity.go/index.go (doc/optimizations.md's "Label optimizations"),
// kept here rather than imported so repr has no dependency on policy.
package repr

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LabelSetID is a dense integer identity for a canonical LabelSet: replicas
// from the same ReplicaSet/Deployment have identical labels, so selector
// evaluation can run once per identity instead of once per pod.
type LabelSetID uint32

// IdentityAllocator maps canonical LabelSets to dense LabelSetIDs, collapsing
// repeated label sets (the common case) onto a single ID.
type IdentityAllocator struct {
	byHash map[uint64]LabelSetID
	sets   []LabelSet
}

func NewIdentityAllocator() *IdentityAllocator {
	return &IdentityAllocator{byHash: map[uint64]LabelSetID{}}
}

// Intern returns the LabelSetID for ls's content, allocating a new one the
// first time this exact label set is seen.
func (a *IdentityAllocator) Intern(ls LabelSet) LabelSetID {
	h := hashLabelKV(ls.sorted)
	// Hash collisions between distinct label sets would incorrectly merge
	// identities; guard with full equality and linear-probe past a
	// collision.
	for {
		id, ok := a.byHash[h]
		if !ok {
			break
		}
		if labelKVEqual(a.sets[id].sorted, ls.sorted) {
			return id
		}
		h++
	}
	id := LabelSetID(len(a.sets))
	a.sets = append(a.sets, ls)
	a.byHash[h] = id
	return id
}

// Get returns the LabelSet previously allocated as id.
func (a *IdentityAllocator) Get(id LabelSetID) LabelSet {
	return a.sets[id]
}

// Len is the number of distinct identities allocated so far.
func (a *IdentityAllocator) Len() int {
	return len(a.sets)
}

func hashLabelKV(sorted []kvPair) uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, x := range sorted {
		binary.LittleEndian.PutUint64(b[:], uint64(x))
		h.Write(b[:])
	}
	return h.Sum64()
}

func labelKVEqual(a, b []kvPair) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SelectorID is a dense integer identity for a compiled, deduped selector.
type SelectorID uint32

type residualOp int

const (
	residualDoesNotExist residualOp = iota
	residualIn                      // multi-value In
	residualNotIn
)

// residualReq is a selector requirement that can't be answered by presence
// in an inverted index and must be evaluated directly against a candidate
// label set.
type residualReq struct {
	key    StringID
	op     residualOp
	values map[StringID]struct{}
}

// CompiledSelector is a metav1.LabelSelector compiled once into interned
// integer form, split into:
//
//   - exact: k=v requirements (MatchLabels, and single-value In) that are
//     indexable via a simple inverted index.
//   - existsKeys: Exists requirements, also indexable (by key alone).
//   - residual: DoesNotExist/NotIn/multi-value-In, evaluated only on
//     candidates that already cleared the indexed part.
type CompiledSelector struct {
	exact      []kvPair // sorted
	existsKeys []StringID
	residual   []residualReq
}

// positiveCount is the number of indexable positive requirements (exact +
// Exists) a matching label set must satisfy.
func (cs CompiledSelector) positiveCount() int {
	return len(cs.exact) + len(cs.existsKeys)
}

// CompileSelector converts sel into compiled form, interning every key/value
// it references into tbl.
func CompileSelector(sel metav1.LabelSelector, tbl *SymbolTable) (CompiledSelector, error) {
	var cs CompiledSelector

	for k, v := range sel.MatchLabels {
		cs.exact = append(cs.exact, newKVPair(tbl.Intern(k), tbl.Intern(v)))
	}

	for _, expr := range sel.MatchExpressions {
		key := tbl.Intern(expr.Key)
		switch expr.Operator {
		case metav1.LabelSelectorOpIn:
			if len(expr.Values) == 0 {
				return CompiledSelector{}, fmt.Errorf("selector: In requires at least one value for key %q", expr.Key)
			}
			if len(expr.Values) == 1 {
				cs.exact = append(cs.exact, newKVPair(key, tbl.Intern(expr.Values[0])))
				continue
			}
			values := make(map[StringID]struct{}, len(expr.Values))
			for _, v := range expr.Values {
				values[tbl.Intern(v)] = struct{}{}
			}
			cs.residual = append(cs.residual, residualReq{key: key, op: residualIn, values: values})
		case metav1.LabelSelectorOpNotIn:
			if len(expr.Values) == 0 {
				return CompiledSelector{}, fmt.Errorf("selector: NotIn requires at least one value for key %q", expr.Key)
			}
			values := make(map[StringID]struct{}, len(expr.Values))
			for _, v := range expr.Values {
				values[tbl.Intern(v)] = struct{}{}
			}
			cs.residual = append(cs.residual, residualReq{key: key, op: residualNotIn, values: values})
		case metav1.LabelSelectorOpExists:
			cs.existsKeys = append(cs.existsKeys, key)
		case metav1.LabelSelectorOpDoesNotExist:
			cs.residual = append(cs.residual, residualReq{key: key, op: residualDoesNotExist})
		default:
			return CompiledSelector{}, fmt.Errorf("selector: unsupported operator %q", expr.Operator)
		}
	}

	slices.Sort(cs.exact)
	slices.Sort(cs.existsKeys)
	return cs, nil
}

// Matches is the full (exact + Exists + residual) evaluation of cs against
// ls. It's the ground-truth semantics; InvertedIndex is an optimization of
// exactly this check at scale.
func (cs CompiledSelector) Matches(ls LabelSet) bool {
	for _, e := range cs.exact {
		if v, ok := ls.kv[e.key()]; !ok || v != e.value() {
			return false
		}
	}
	for _, k := range cs.existsKeys {
		if _, ok := ls.kv[k]; !ok {
			return false
		}
	}
	return cs.matchesResidual(ls)
}

func (cs CompiledSelector) matchesResidual(ls LabelSet) bool {
	for _, r := range cs.residual {
		v, present := ls.kv[r.key]
		switch r.op {
		case residualDoesNotExist:
			if present {
				return false
			}
		case residualIn:
			if !present {
				return false
			}
			if _, ok := r.values[v]; !ok {
				return false
			}
		case residualNotIn:
			if present {
				if _, ok := r.values[v]; ok {
					return false
				}
			}
		}
	}
	return true
}

// canonicalKey produces a string that's identical for two
// metav1.LabelSelector values with the same meaning regardless of map/slice
// iteration order, so SelectorRegistry and NamespaceSelectorCache can dedupe
// repeated identical selectors.
func canonicalKey(sel metav1.LabelSelector) string {
	keys := make([]string, 0, len(sel.MatchLabels))
	for k := range sel.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(sel.MatchLabels[k])
		b.WriteByte(',')
	}

	exprs := append([]metav1.LabelSelectorRequirement(nil), sel.MatchExpressions...)
	sort.Slice(exprs, func(i, j int) bool {
		if exprs[i].Key != exprs[j].Key {
			return exprs[i].Key < exprs[j].Key
		}
		return exprs[i].Operator < exprs[j].Operator
	})
	for _, e := range exprs {
		b.WriteString(e.Key)
		b.WriteByte(':')
		b.WriteString(string(e.Operator))
		b.WriteByte(':')
		values := append([]string(nil), e.Values...)
		sort.Strings(values)
		b.WriteString(strings.Join(values, "|"))
		b.WriteByte(';')
	}
	return b.String()
}

// SelectorRegistry compiles metav1.LabelSelectors exactly once, deduping
// identical selectors by their canonical content.
type SelectorRegistry struct {
	tbl      *SymbolTable
	byKey    map[string]SelectorID
	compiled []CompiledSelector
}

func NewSelectorRegistry(tbl *SymbolTable) *SelectorRegistry {
	return &SelectorRegistry{tbl: tbl, byKey: map[string]SelectorID{}}
}

func (r *SelectorRegistry) Get(sel metav1.LabelSelector) (SelectorID, error) {
	key := canonicalKey(sel)
	if id, ok := r.byKey[key]; ok {
		return id, nil
	}
	cs, err := CompileSelector(sel, r.tbl)
	if err != nil {
		return 0, err
	}
	id := SelectorID(len(r.compiled))
	r.compiled = append(r.compiled, cs)
	r.byKey[key] = id
	return id, nil
}

// Compiled returns the compiled selector for id.
func (r *SelectorRegistry) Compiled(id SelectorID) CompiledSelector {
	return r.compiled[id]
}

// All returns every compiled selector registered so far, indexed by
// SelectorID.
func (r *SelectorRegistry) All() []CompiledSelector {
	return r.compiled
}

// Bitset is a fixed-size bit vector, used to store per-identity match
// results so membership tests downstream are a single word AND instead of a
// set/map lookup.
type Bitset []uint64

func NewBitset(n int) Bitset {
	return make(Bitset, (n+63)/64)
}

func (b Bitset) Set(i uint32) {
	b[i/64] |= 1 << (i % 64)
}

func (b Bitset) Test(i uint32) bool {
	if int(i/64) >= len(b) {
		return false
	}
	return b[i/64]&(1<<(i%64)) != 0
}

// InvertedIndex maps label key/value pairs (and bare keys, for Exists
// requirements) to the selectors that require them, so matching a label set
// costs O(label-set size) index probes instead of O(selector count) full
// selector evaluations.
type InvertedIndex struct {
	byExact map[kvPair][]SelectorID
	byKey   map[StringID][]SelectorID // Exists requirements, indexed by key alone
	// scanAlways holds selectors with zero positive (exact/Exists)
	// requirements — an empty selector, or one built purely from
	// NotIn/DoesNotExist — which can't be found by indexing a label set's
	// positive content and so must always be residual-checked directly.
	scanAlways []SelectorID
	required   []int // positiveCount() per SelectorID
	compiled   []CompiledSelector
}

func BuildInvertedIndex(compiled []CompiledSelector) *InvertedIndex {
	idx := &InvertedIndex{
		byExact:  map[kvPair][]SelectorID{},
		byKey:    map[StringID][]SelectorID{},
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

// Match returns the IDs of every selector that matches ls: a counting join
// over the inverted index (a selector matches iff all of its positive
// requirements are found) followed by a residual check on the survivors.
func (idx *InvertedIndex) Match(ls LabelSet) []SelectorID {
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

// NamespaceSelectorCache memoizes NamespaceSelector matches against the
// (always small) namespace set, keyed by the selector's canonical content,
// so an identical NamespaceSelector reused across many rules is only
// evaluated once.
//
// The match cache is lazily populated; mu guards it for concurrent callers.
type NamespaceSelectorCache struct {
	tbl        *SymbolTable
	namespaces []LabelSet
	names      []StringID

	mu    sync.RWMutex
	cache map[string][]StringID
}

func NewNamespaceSelectorCache(namespaces []Namespace, tbl *SymbolTable) *NamespaceSelectorCache {
	c := &NamespaceSelectorCache{tbl: tbl, cache: map[string][]StringID{}}
	for _, ns := range namespaces {
		c.namespaces = append(c.namespaces, ns.Labels)
		c.names = append(c.names, ns.Name)
	}
	return c
}

// Match returns the interned names of every namespace selected by sel,
// sorted by name for determinism.
func (c *NamespaceSelectorCache) Match(sel metav1.LabelSelector) ([]StringID, error) {
	key := canonicalKey(sel)

	c.mu.RLock()
	names, ok := c.cache[key]
	c.mu.RUnlock()
	if ok {
		return names, nil
	}

	cs, err := CompileSelector(sel, c.tbl)
	if err != nil {
		return nil, err
	}
	var matched []StringID
	for i, ls := range c.namespaces {
		if cs.Matches(ls) {
			matched = append(matched, c.names[i])
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		return c.tbl.String(matched[i]) < c.tbl.String(matched[j])
	})

	c.mu.Lock()
	c.cache[key] = matched
	c.mu.Unlock()
	return matched, nil
}
