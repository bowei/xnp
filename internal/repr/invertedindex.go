package repr

import "github.com/bowei/xnp/internal/intern"

// InvertedIndex maps label key/value pairs (and bare keys, for Exists
// requirements) to the selectors that require them, so matching a label set
// costs O(label-set size) index probes instead of O(selector count) full
// selector evaluations.
type InvertedIndex struct {
	byExact map[kvPair][]SelectorID
	byKey   map[intern.ID][]SelectorID // Exists requirements, indexed by key alone
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
		byKey:    map[intern.ID][]SelectorID{},
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

// InvertedIndexStats describes the size of an InvertedIndex, for logging/
// diagnostics rather than matching itself.
type InvertedIndexStats struct {
	Selectors    int // len(compiled): total selectors indexed
	ExactEntries int // distinct key=value pairs in byExact
	KeyEntries   int // distinct bare keys in byKey (Exists requirements)
	ScanAlways   int // selectors with no positive requirement, checked on every match
}

// Stats reports idx's size.
func (idx *InvertedIndex) Stats() InvertedIndexStats {
	return InvertedIndexStats{
		Selectors:    len(idx.compiled),
		ExactEntries: len(idx.byExact),
		KeyEntries:   len(idx.byKey),
		ScanAlways:   len(idx.scanAlways),
	}
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
