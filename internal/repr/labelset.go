package repr

import (
	"slices"

	"github.com/bowei/xnp/internal/intern"
)

// LabelSet is a pod's or namespace's labels canonicalized into interned
// integer form: kv gives O(1) key lookup, sorted gives a canonical,
// hashable, iterable form for later identity/index work (see lookup.go).
type LabelSet struct {
	kv     map[intern.ID]intern.ID
	sorted []kvPair
}

// NewLabelSet interns labels' keys/values via tbl and returns their
// canonical LabelSet form.
func NewLabelSet(labels map[string]string, tbl *intern.Table) LabelSet {
	kv := make(map[intern.ID]intern.ID, len(labels))
	sorted := make([]kvPair, 0, len(labels))
	for k, v := range labels {
		key, val := tbl.Intern(k), tbl.Intern(v)
		kv[key] = val
		sorted = append(sorted, newKVPair(key, val))
	}
	slices.Sort(sorted)
	return LabelSet{kv: kv, sorted: sorted}
}

// Get returns the interned value intern.ID for key, if present.
func (ls LabelSet) Get(key intern.ID) (intern.ID, bool) {
	v, ok := ls.kv[key]
	return v, ok
}

// Len is the number of labels in the set.
func (ls LabelSet) Len() int {
	return len(ls.sorted)
}

// At returns the i'th key/value pair, in the canonical sorted (K, V) order
// LabelSet maintains. Lets external packages (e.g. an eqc.LabelSet adapter)
// walk a LabelSet's content without repr exposing its internal kvPair type.
func (ls LabelSet) At(i int) (key, value intern.ID) {
	kv := ls.sorted[i]
	return kv.key(), kv.value()
}

// Hash that can be used to put into a map key.
func (ls LabelSet) Hash() uint64 {
	return hashLabelKV(ls.sorted)
}

// Equal returns true if this is equal to `other`.
func (ls LabelSet) Equal(other LabelSet) bool {
	return labelKVEqual(ls.sorted, other.sorted)
}
