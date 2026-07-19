package labelset

import (
	"encoding/binary"
	"hash/fnv"
	"slices"

	"github.com/bowei/xnp/internal/intern"
)

// LabelSet is a pod's or namespace's labels canonicalized into interned
// integer form: kv gives O(1) key lookup, sorted gives a canonical,
// hashable, iterable form for later identity/index work (see lookup.go).
type LabelSet struct {
	kv     map[intern.ID]intern.ID
	sorted []intern.Pair
}

// Make interns labels' keys/values via tbl and returns their
// canonical LabelSet form.
func Make(labels map[string]string, tbl *intern.Table) LabelSet {
	kv := make(map[intern.ID]intern.ID, len(labels))
	sorted := make([]intern.Pair, 0, len(labels))
	for k, v := range labels {
		key, val := tbl.Intern(k), tbl.Intern(v)
		kv[key] = val
		sorted = append(sorted, intern.NewPair(key, val))
	}
	slices.Sort(sorted)
	return LabelSet{kv: kv, sorted: sorted}
}

// Get returns the value for key, if present.
func (s LabelSet) Get(key intern.ID) (intern.ID, bool) {
	for i := range s.sorted {
		if s.sorted[i].Key() == key {
			return s.sorted[i].Value(), true
		}
	}
	return 0, false
}

// Len is the number of labels in the set.
func (s LabelSet) Len() int {
	return len(s.sorted)
}

// At returns the i'th key/value pair, in the canonical sorted (K, V) order
// LabelSet maintains. Lets external packages (e.g. an eqc.LabelSet adapter)
// walk a LabelSet's content without repr exposing its internal kvPair type.
func (s LabelSet) At(i int) (key, value intern.ID) {
	kv := s.sorted[i]
	return kv.Key(), kv.Value()
}

// Hash that can be used to put into a map key.
func (s LabelSet) Hash() uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, x := range s.sorted {
		binary.LittleEndian.PutUint64(b[:], uint64(x))
		h.Write(b[:])
	}
	return h.Sum64()
}

// Equal returns true if this is equal to `other`.
func (s LabelSet) Equal(other LabelSet) bool {
	return slices.Equal(s.sorted, other.sorted)
}
