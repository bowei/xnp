package eqc

import "sort"

// Later improvement: change to RoaringSets

// NsID is the unique integer for a Namespace.
type NsID uint32

// KeyID is the unique integer for a given label key.
type KeyID uint32

// ValueID is the unique integer for a given label value.
type ValueID uint32

const (
	NoNS    NsID    = 0
	NoValue ValueID = 0
)

type LabelSetID uint64

// KeyValue pair
type KeyValue struct {
	K KeyID
	V ValueID
}

// KV makes examples more readable.
func KV(k KeyID, v ValueID) KeyValue { return KeyValue{k, v} }

// LabelSet is a set of key:value labels. This must always be sorted
// by ascending lexigraphic order (K, V).
type LabelSet []KeyValue

// MakeLabelSet is a helper that returns a LabelSet from pairs of Key,
// Value. If this is called with a Key with no associated Value
// (unpaired), the last KeyValue will have V == NoValue.
func MakeLabelSet(kvs ...any) LabelSet {
	out := make(LabelSet, 0, (len(kvs)+1)/2)
	for i := 0; i < len(kvs); i += 2 {
		k := kvs[i].(KeyID)
		v := NoValue
		if i+1 < len(kvs) {
			v = kvs[i+1].(ValueID)
		}
		out = append(out, KeyValue{K: k, V: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].K != out[j].K {
			return out[i].K < out[j].K
		}
		return out[i].V < out[j].V
	})
	return out
}

// Values returns the matching values for the given Keys.
func (s LabelSet) Values(keys []KeyID) []ValueID {
	var out []ValueID
	for _, k := range keys {
		i := sort.Search(len(s), func(i int) bool { return s[i].K >= k })
		for ; i < len(s) && s[i].K == k; i++ {
			out = append(out, s[i].V)
		}
	}
	return out
}

// Matches returns true if toMatch matches this LabelSet. s is treated
// as a set of required key:value pairs; toMatch matches if it
// contains every pair in s (toMatch may have additional labels not
// present in s).
func (s LabelSet) Matches(toMatch LabelSet) bool {
	i, j := 0, 0
	for i < len(s) {
		if j >= len(toMatch) {
			return false
		}
		switch {
		case toMatch[j].K < s[i].K, toMatch[j].K == s[i].K && toMatch[j].V < s[i].V:
			j++
		case toMatch[j] == s[i]:
			i++
			j++
		default:
			return false
		}
	}
	return true
}

// Project returns the subset of labels that have keys given in
// `relevant`.
func (s LabelSet) Project(relevant []KeyID) LabelSet {
	want := make(map[KeyID]bool, len(relevant))
	for _, k := range relevant {
		want[k] = true
	}
	out := make(LabelSet, 0, len(s))
	for _, kv := range s {
		if want[kv.K] {
			out = append(out, kv)
		}
	}
	return out
}
