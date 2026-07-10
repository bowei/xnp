package repr

import "slices"

// LabelSet is a pod's or namespace's labels canonicalized into interned
// integer form: kv gives O(1) key lookup, sorted gives a canonical,
// hashable, iterable form for later identity/index work (see lookup.go).
type LabelSet struct {
	kv     map[StringID]StringID
	sorted []kvPair
}

// NewLabelSet interns labels' keys/values via tbl and returns their
// canonical LabelSet form.
func NewLabelSet(labels map[string]string, tbl *SymbolTable) LabelSet {
	kv := make(map[StringID]StringID, len(labels))
	sorted := make([]kvPair, 0, len(labels))
	for k, v := range labels {
		key, val := tbl.Intern(k), tbl.Intern(v)
		kv[key] = val
		sorted = append(sorted, newKVPair(key, val))
	}
	slices.Sort(sorted)
	return LabelSet{kv: kv, sorted: sorted}
}

// Get returns the interned value StringID for key, if present.
func (ls LabelSet) Get(key StringID) (StringID, bool) {
	v, ok := ls.kv[key]
	return v, ok
}

// Len is the number of labels in the set.
func (ls LabelSet) Len() int {
	return len(ls.sorted)
}

// Hash that can be used to put into a map key.
func (ls LabelSet) Hash() uint64 {
	return hashLabelKV(ls.sorted)
}

// Equal returns true if this is equal to `other`.
func (ls LabelSet) Equal(other LabelSet) bool {
	return labelKVEqual(ls.sorted, other.sorted)
}
