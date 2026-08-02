package eqc

import (
	"encoding/binary"
	"slices"
	"strings"
	"unique"
)

// Experiment: what if EqC's LabelSet used Go's stdlib "unique" package
// (unique.Handle[string]) to intern label keys/values directly, instead of
// this repo's own intern.Table + uint32 intern.ID indirection (as
// internal/labels.Set does)? That would let a LabelSet be built straight
// from map[string]string with no intern.Table plumbed through, and drop
// the KeyID/ValueID types entirely.
//
// This file is a self-contained, EqC-only prototype of that design (see
// eqc_unique_bench_test.go for the benchmarks comparing it against EqC).
// It intentionally mirrors eqc.go's structure field-for-field, changing
// only the label representation, so the benchmark diff isolates that one
// variable. It does not plug into internal/labels, internal/repr, or
// anything outside this package (this is an experiment, not a replacement).
//
// Result (benchstat, n=6, Add/AddTwice across all benchConfigs shapes):
// geomean +21% time/op, +9% bytes/op, allocs/op unchanged. Every shape
// regressed or was flat; none improved. Update shows the same shape
// (+15-70%, worst on the many-relevant-keys configs); Remove — which never
// touches label content — is a wash, confirming the regression traces to
// label handling specifically, not general overhead from this file's
// second copy of the EqC bookkeeping. TestMemoryFootprint's steady-state
// bytes/object also runs consistently higher for UEqC (worst case, on the
// 80-relevant-key config: 1891 B/obj vs 3107 B/obj).
//
// Root cause is UKV losing the one property intern.ID has that
// unique.Handle[string] doesn't: a small dense integer that's already a
// sort key and already a fixed-width, cheaply-hashable map key.
// unique.Handle only gives O(1) *equality* (pointer comparison) — ordering
// (compareUKV) and content-addressing (AppendProjectKey, for
// labelSetByKey) both have to fall back to the underlying string, so both
// scale with label text length instead of a flat 4 or 8 bytes per pair.
// That shows up worst exactly where more/longer labels are in play
// (VeryHighCardinality_Keys, 80 relevant keys: +86% time, +75% bytes) and
// is smallest where labels are few and short (AllShared_1Key: +10%).
//
// The code *is* simpler in the way the prompt expected — no intern.Table
// to construct/thread through, MakeUSet takes a plain map[string]string —
// but that simplification is exactly what gives up the fixed-width sort
// key/map key, which is what this package's hot paths (Add's
// AppendProjectKey + internKey, UpdateRelevant's full rebuild) lean on
// hardest. Given EqC is meant for high-scale rendering, not switching.

// UKV is a single (key, value) label pair, each interned as a
// unique.Handle[string] rather than through intern.Table's uint32 ID.
type UKV struct {
	K, V unique.Handle[string]
}

// USet is the unique.Handle-based analog of internal/labels.Set.
type USet struct {
	sorted []UKV
}

// MakeUSet interns labels' keys/values via unique.Make and returns their
// canonical USet form. Unlike labels.Make, this needs no intern.Table
// argument — unique.Make's table is process-global.
func MakeUSet(kv map[string]string) USet {
	sorted := make([]UKV, 0, len(kv))
	for k, v := range kv {
		sorted = append(sorted, UKV{unique.Make(k), unique.Make(v)})
	}
	slices.SortFunc(sorted, compareUKV)
	return USet{sorted: sorted}
}

// MakeUSetKV is a test/benchmark helper mirroring labels.MakeSet: builds a
// USet directly from alternating key, value, key, value, ... strings.
func MakeUSetKV(kvs ...string) USet {
	sorted := make([]UKV, 0, (len(kvs)+1)/2)
	for i := 0; i < len(kvs); i += 2 {
		k := kvs[i]
		v := ""
		if i+1 < len(kvs) {
			v = kvs[i+1]
		}
		sorted = append(sorted, UKV{unique.Make(k), unique.Make(v)})
	}
	slices.SortFunc(sorted, compareUKV)
	return USet{sorted: sorted}
}

// compareUKV orders pairs by their actual string content. unique.Handle
// gives O(1) equality but not ordering, so — unlike intern.ID, which is
// already a sortable dense integer — establishing a canonical order here
// means dereferencing back to the string on every comparison.
func compareUKV(a, b UKV) int {
	if c := strings.Compare(a.K.Value(), b.K.Value()); c != 0 {
		return c
	}
	return strings.Compare(a.V.Value(), b.V.Value())
}

func (s USet) Len() int { return len(s.sorted) }

func (s USet) At(i int) (key, value unique.Handle[string]) {
	kv := s.sorted[i]
	return kv.K, kv.V
}

// ProjectRelevant mirrors labels.Set.ProjectRelevant.
func (s USet) ProjectRelevant(relevantSet map[unique.Handle[string]]bool) USet {
	out := USet{}
	for _, kv := range s.sorted {
		if relevantSet[kv.K] {
			out.sorted = append(out.sorted, kv)
		}
	}
	return out
}

// AppendProjectKey mirrors labels.Set.AppendProjectKey, but — lacking a
// dense int ID to pack — encodes each pair as its actual (length-prefixed)
// key/value string bytes rather than a fixed 8 bytes. This is the direct
// cost of dropping the intern.ID indirection: the content-addressing key
// is only as compact as the labels themselves.
func (s USet) AppendProjectKey(dst []byte, relevantSet map[unique.Handle[string]]bool) []byte {
	for _, kv := range s.sorted {
		if !relevantSet[kv.K] {
			continue
		}
		k, v := kv.K.Value(), kv.V.Value()
		dst = binary.AppendUvarint(dst, uint64(len(k)))
		dst = append(dst, k...)
		dst = binary.AppendUvarint(dst, uint64(len(v)))
		dst = append(dst, v...)
	}
	return dst
}

// ProjectEquals mirrors labels.Set.ProjectEquals.
func (s USet) ProjectEquals(relevantSet map[unique.Handle[string]]bool, want USet) bool {
	w := 0
	for _, kv := range s.sorted {
		if !relevantSet[kv.K] {
			continue
		}
		if w >= len(want.sorted) || want.sorted[w] != kv {
			return false
		}
		w++
	}
	return w == len(want.sorted)
}

// ULabeled mirrors Labeled, but with USet in place of labels.Set.
type ULabeled interface {
	ID() uint64
	Namespace() NsID
	Labels() USet
}

// ULabelSetID is the unique-variant's group identity. It's an alias, not a
// distinct type: it plays exactly the same role as LabelSetID (an
// insertion-order counter, never compared across the two variants), so
// aliasing lets UEqC reuse EqC's locKey/objLoc unchanged.
type ULabelSetID = LabelSetID

type UPartitions map[ULabelSetID]UPartition

type UPartition struct {
	labels     USet
	labelSetID ULabelSetID
	objects    []ULabeled
}

func (p UPartition) Labels() USet            { return p.labels }
func (p UPartition) LabelSetID() ULabelSetID { return p.labelSetID }
func (p UPartition) Objects() []ULabeled     { return p.objects }

// UEqC is the unique.Handle-based analog of EqC. See the package doc
// comment above for what varies (and what deliberately doesn't) versus EqC.
type UEqC struct {
	relevant    []unique.Handle[string]
	relevantSet map[unique.Handle[string]]bool

	partitions map[NsID]UPartitions

	nextLabelSetID ULabelSetID
	labelSetByKey  map[string]ULabelSetID

	locations map[locKey]objLoc
}

// NewUnique is the UEqC analog of New.
func NewUnique(relevant []unique.Handle[string]) *UEqC {
	return &UEqC{
		relevant:      append([]unique.Handle[string](nil), relevant...),
		relevantSet:   newURelevantSet(relevant),
		partitions:    map[NsID]UPartitions{},
		labelSetByKey: map[string]ULabelSetID{},
		locations:     map[locKey]objLoc{},
	}
}

func newURelevantSet(relevant []unique.Handle[string]) map[unique.Handle[string]]bool {
	set := make(map[unique.Handle[string]]bool, len(relevant))
	for _, k := range relevant {
		set[k] = true
	}
	return set
}

// Add mirrors EqC.Add.
func (c *UEqC) Add(objects []ULabeled) {
	var keyBuf []byte
	for _, o := range objects {
		ls := o.Labels()
		keyBuf = ls.AppendProjectKey(keyBuf[:0], c.relevantSet)
		id, isNew := c.internKey(keyBuf)

		ns := o.Namespace()
		nsPartitions, ok := c.partitions[ns]
		if !ok {
			nsPartitions = UPartitions{}
			c.partitions[ns] = nsPartitions
		}

		p := nsPartitions[id]
		if isNew {
			p.labels = ls.ProjectRelevant(c.relevantSet)
			p.labelSetID = id
		}
		p.objects = append(p.objects, o)
		nsPartitions[id] = p
		c.locations[locKey{ns, o.ID()}] = objLoc{labelSetID: id, idx: len(p.objects) - 1}
	}
}

// Update mirrors EqC.Update.
func (c *UEqC) Update(objects []ULabeled) {
	for _, o := range objects {
		ns := o.Namespace()
		loc, ok := c.locations[locKey{ns, o.ID()}]
		if !ok {
			c.Add([]ULabeled{o})
			continue
		}

		nsPartitions := c.partitions[ns]
		p := nsPartitions[loc.labelSetID]
		if o.Labels().ProjectEquals(c.relevantSet, p.labels) {
			p.objects[loc.idx] = o
			nsPartitions[loc.labelSetID] = p
			continue
		}

		c.evict(ns, o.ID(), loc)
		c.Add([]ULabeled{o})
	}
}

// Remove mirrors EqC.Remove.
func (c *UEqC) Remove(objects []ULabeled) {
	for _, o := range objects {
		ns := o.Namespace()
		loc, ok := c.locations[locKey{ns, o.ID()}]
		if !ok {
			continue
		}
		c.evict(ns, o.ID(), loc)
	}
}

// evict mirrors EqC.evict.
func (c *UEqC) evict(ns NsID, id uint64, loc objLoc) {
	nsPartitions := c.partitions[ns]
	p := nsPartitions[loc.labelSetID]

	last := len(p.objects) - 1
	if loc.idx != last {
		p.objects[loc.idx] = p.objects[last]
		c.locations[locKey{ns, p.objects[loc.idx].ID()}] = objLoc{labelSetID: loc.labelSetID, idx: loc.idx}
	}
	p.objects = p.objects[:last]
	delete(c.locations, locKey{ns, id})

	if len(p.objects) == 0 {
		delete(nsPartitions, loc.labelSetID)
		if len(nsPartitions) == 0 {
			delete(c.partitions, ns)
		}
	} else {
		nsPartitions[loc.labelSetID] = p
	}
}

// UpdateRelevant mirrors EqC.UpdateRelevant.
func (c *UEqC) UpdateRelevant(keys []unique.Handle[string]) {
	var all []ULabeled
	for _, nsPartitions := range c.partitions {
		for _, p := range nsPartitions {
			all = append(all, p.objects...)
		}
	}

	c.relevant = append([]unique.Handle[string](nil), keys...)
	c.relevantSet = newURelevantSet(keys)
	c.partitions = map[NsID]UPartitions{}
	c.nextLabelSetID = 0
	c.labelSetByKey = map[string]ULabelSetID{}
	c.locations = map[locKey]objLoc{}
	c.Add(all)
}

// Range mirrors EqC.Range.
func (c *UEqC) Range() func(yield func(NsID, UPartitions) bool) {
	return func(yield func(NsID, UPartitions) bool) {
		for ns, p := range c.partitions {
			if !yield(ns, p) {
				return
			}
		}
	}
}

// internKey mirrors EqC.internKey.
func (c *UEqC) internKey(key []byte) (id ULabelSetID, isNew bool) {
	if id, ok := c.labelSetByKey[string(key)]; ok {
		return id, false
	}
	id = c.nextLabelSetID
	c.nextLabelSetID++
	c.labelSetByKey[string(key)] = id
	return id, true
}
