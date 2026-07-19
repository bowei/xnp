package labels

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"slices"

	"github.com/bowei/xnp/internal/intern"
)

// noValue fills the value half of an unpaired trailing key passed to
// MakeSet.
const noValue intern.ID = math.MaxUint32

// Set is a pod's or namespace's labels canonicalized into interned
// integer form: kv gives O(1) key lookup, sorted gives a canonical,
// hashable, iterable form for later identity/index work (see lookup.go).
type Set struct {
	sorted []intern.Pair
}

// Make interns labels' keys/values via tbl and returns their
// canonical LabelSet form.
func Make(labels map[string]string, tbl *intern.Table) Set {
	sorted := make([]intern.Pair, 0, len(labels))
	for k, v := range labels {
		key, val := tbl.Intern(k), tbl.Intern(v)
		sorted = append(sorted, intern.NewPair(key, val))
	}
	slices.Sort(sorted)
	return Set{sorted: sorted}
}

// MakeSet is a test/benchmark helper that builds a Set directly from
// alternating key, value, key, value, ... intern.IDs, without needing an
// intern.Table. If called with a trailing, unpaired key, that last pair's
// value is noValue.
func MakeSet(kvs ...intern.ID) Set {
	sorted := make([]intern.Pair, 0, (len(kvs)+1)/2)
	for i := 0; i < len(kvs); i += 2 {
		k := kvs[i]
		v := noValue
		if i+1 < len(kvs) {
			v = kvs[i+1]
		}
		sorted = append(sorted, intern.NewPair(k, v))
	}
	slices.Sort(sorted)
	return Set{sorted: sorted}
}

// Get returns the value for key, if present.
func (s Set) Get(key intern.ID) (intern.ID, bool) {
	for i := range s.sorted {
		if s.sorted[i].Key() == key {
			return s.sorted[i].Value(), true
		}
	}
	return 0, false
}

// Len is the number of labels in the set.
func (s Set) Len() int {
	return len(s.sorted)
}

// At returns the i'th key/value pair, in the canonical sorted (K, V) order
// Set maintains. Lets external packages walk a Set's content without this
// package exposing its internal intern.Pair representation.
func (s Set) At(i int) (key, value intern.ID) {
	kv := s.sorted[i]
	return kv.Key(), kv.Value()
}

// Hash that can be used to put into a map key.
func (s Set) Hash() uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, x := range s.sorted {
		binary.LittleEndian.PutUint64(b[:], uint64(x))
		h.Write(b[:])
	}
	return h.Sum64()
}

// Equal returns true if this is equal to `other`.
func (s Set) Equal(other Set) bool {
	return slices.Equal(s.sorted, other.sorted)
}

// Matches returns true if toMatch matches this LabelSet. s is treated
// as a set of required key:value pairs; toMatch matches if it
// contains every pair in s (toMatch may have additional labels not
// present in s).
func (s Set) Matches(toMatch Set) bool {
	i, j := 0, 0
	for i < len(s.sorted) {
		if j >= len(toMatch.sorted) {
			return false
		}

		a := s.sorted[i]
		b := toMatch.sorted[j]

		switch {
		case b.Key() < a.Key(), b.Key() == a.Key() && b.Value() < a.Value():
			j++
		case a == b:
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
func (s Set) Project(relevant []intern.ID) Set {
	want := make(map[intern.ID]bool, len(relevant))
	for _, k := range relevant {
		want[k] = true
	}
	return s.ProjectRelevant(want)
}

// ProjectRelevant is equivalent to Project, but takes an already-built
// relevant-keys set instead of rebuilding one from a key list on every
// call — for callers (e.g. eqc.EqC) that project the same relevant set
// repeatedly and can amortize building it once.
func (s Set) ProjectRelevant(relevantSet map[intern.ID]bool) Set {
	out := Set{}
	for _, kv := range s.sorted {
		if relevantSet[kv.Key()] {
			out.sorted = append(out.sorted, kv)
		}
	}
	return out
}

// Key returns a canonical byte-string encoding of s, suitable for use as
// a map key to content-address a LabelSet in O(1). Relies on s already
// being sorted (the LabelSet invariant).
func (s Set) Key() string {
	buf := make([]byte, len(s.sorted)*8)
	for i, kv := range s.sorted {
		binary.BigEndian.PutUint32(buf[i*8:], uint32(kv.Key()))
		binary.BigEndian.PutUint32(buf[i*8+4:], uint32(kv.Value()))
	}
	return string(buf)
}

// AppendProjectKey is equivalent to append(dst, s.Project(keys).key()...),
// but computes the canonical byte key directly from the full
// (unprojected) s in one pass instead of first allocating the
// intermediate projected LabelSet, and writes into a caller-owned
// buffer.
func (s Set) AppendProjectKey(dst []byte, relevantSet map[intern.ID]bool) []byte {
	for _, kv := range s.sorted {
		if !relevantSet[kv.Key()] {
			continue
		}
		dst = binary.BigEndian.AppendUint32(dst, uint32(kv.Key()))
		dst = binary.BigEndian.AppendUint32(dst, uint32(kv.Value()))
	}
	return dst
}

// ProjectEquals reports whether s's projection onto relevantSet equals
// want, without materializing the projected subset — equivalent to
// slices.Equal(s.Project(keys), want).
func (s Set) ProjectEquals(relevantSet map[intern.ID]bool, want Set) bool {
	w := 0
	for _, kv := range s.sorted {
		if !relevantSet[kv.Key()] {
			continue
		}
		if w >= len(want.sorted) || want.sorted[w] != kv {
			return false
		}
		w++
	}
	return w == len(want.sorted)
}
