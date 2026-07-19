package eqc

import (
	"encoding/binary"
	"slices"
)

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
		out = append(out, KV(k, v))
	}
	slices.SortFunc(out, func(a, b KeyValue) int {
		if a.K() != b.K() {
			return int(a.K()) - int(b.K())
		}
		return int(a.V()) - int(b.V())
	})
	return out
}

func (s LabelSet) Len() int { return len(s) }

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
		case toMatch[j].K() < s[i].K(), toMatch[j].K() == s[i].K() && toMatch[j].V() < s[i].V():
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
		if want[kv.K()] {
			out = append(out, kv)
		}
	}
	return out
}

// key returns a canonical byte-string encoding of s, suitable for use as
// a map key to content-address a LabelSet in O(1). Relies on s already
// being sorted (the LabelSet invariant).
func (s LabelSet) key() string {
	buf := make([]byte, len(s)*8)
	for i, kv := range s {
		binary.BigEndian.PutUint32(buf[i*8:], uint32(kv.K()))
		binary.BigEndian.PutUint32(buf[i*8+4:], uint32(kv.V()))
	}
	return string(buf)
}

// appendProjectKey is equivalent to append(dst, s.Project(keys).key()...),
// but computes the canonical byte key directly from the full
// (unprojected) s in one pass instead of first allocating the
// intermediate projected LabelSet, and writes into a caller-owned
// buffer.
func (s LabelSet) appendProjectKey(dst []byte, relevantSet map[KeyID]bool) []byte {
	for _, kv := range s {
		if !relevantSet[kv.K()] {
			continue
		}
		dst = binary.BigEndian.AppendUint32(dst, uint32(kv.K()))
		dst = binary.BigEndian.AppendUint32(dst, uint32(kv.V()))
	}
	return dst
}

// projectEquals reports whether s's projection onto relevantSet equals
// want, without materializing the projected subset — equivalent to
// slices.Equal(s.Project(keys), want).
func (s LabelSet) projectEquals(relevantSet map[KeyID]bool, want LabelSet) bool {
	w := 0
	for _, kv := range s {
		if !relevantSet[kv.K()] {
			continue
		}
		if w >= len(want) || want[w] != kv {
			return false
		}
		w++
	}
	return w == len(want)
}
