package policy

import (
	"encoding/binary"
	"hash/fnv"
	"slices"
)

// LabelSetID is an interned identity for a canonical label set: replicas
// from the same ReplicaSet/Deployment have identical labels, so a
// 5,000-pod cluster might have only a couple hundred distinct label sets.
// See doc/optimizations.md "Label optimizations" #1 (Cilium's "security
// identity" concept).
type LabelSetID uint32

// internedLabelSet is a pod's or namespace's labels canonicalized into
// interned integer form: kv gives O(1) key lookup (for residual checks),
// sorted gives a canonical, hashable, iterable form (for identity hashing
// and the inverted index).
type internedLabelSet struct {
	kv     map[uint32]uint32
	sorted []uint64
}

func internSet(labels map[string]string, tbl *symbolTable) internedLabelSet {
	kvMap := make(map[uint32]uint32, len(labels))
	sorted := make([]uint64, 0, len(labels))
	for k, v := range labels {
		key, val := tbl.intern(k), tbl.intern(v)
		kvMap[key] = val
		sorted = append(sorted, kv(key, val))
	}
	slices.Sort(sorted)
	return internedLabelSet{kv: kvMap, sorted: sorted}
}

// identityAllocator maps canonical label sets to dense LabelSetIDs,
// collapsing repeated label sets (the common case) onto a single ID so
// downstream selector evaluation happens once per identity, not once per
// pod.
type identityAllocator struct {
	byHash map[uint64]LabelSetID
	sets   []internedLabelSet
}

func newIdentityAllocator() *identityAllocator {
	return &identityAllocator{byHash: map[uint64]LabelSetID{}}
}

// intern canonicalizes labels via tbl and returns the LabelSetID for its
// content, allocating a new one the first time this exact label set is
// seen.
func (a *identityAllocator) intern(labels map[string]string, tbl *symbolTable) LabelSetID {
	ls := internSet(labels, tbl)
	h := hashKV(ls.sorted)
	// Hash collisions between distinct label sets would incorrectly merge
	// identities; guard with full equality and linear-probe past a
	// collision.
	for {
		id, ok := a.byHash[h]
		if !ok {
			break
		}
		if kvEqual(a.sets[id].sorted, ls.sorted) {
			return id
		}
		h++
	}
	id := LabelSetID(len(a.sets))
	a.sets = append(a.sets, ls)
	a.byHash[h] = id
	return id
}

func (a *identityAllocator) get(id LabelSetID) internedLabelSet {
	return a.sets[id]
}

func hashKV(sorted []uint64) uint64 {
	h := fnv.New64a()
	var b [8]byte
	for _, x := range sorted {
		binary.LittleEndian.PutUint64(b[:], x)
		h.Write(b[:])
	}
	return h.Sum64()
}

func kvEqual(a, b []uint64) bool {
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
