package rlabels

import "k8s.io/utils/set"

// Builder accumulates the relevant labels a namespace at a time -- one
// contribution per policy, in whatever order the policies are walked -- and
// freezes the result into an RLabels.
//
// The zero Builder is ready to use.
type Builder struct {
	// l is the set under construction. It stays nil until something lands in
	// it, so a Builder nothing was merged into builds the same RLabels as one
	// that was never touched.
	l PerNsLabels
}

// Merge labels into the relevant set for namespace ns.
//
// The keys are copied out rather than the set being retained: the caller is
// merging one policy's keys at a time and is free to keep the set it passed
// and mutate it, while an RLabels hands its own sets straight back through
// Difference and so needs sets nothing else still holds a reference to.
//
// An empty labels set does not register ns. A namespace with no relevant keys
// projects every pod onto the empty label set, which is exactly what a
// namespace absent from the map does, so recording it would buy no meaning and
// would make a policy whose selectors name no keys -- an empty podSelector,
// say -- show up as an Added and then a Removed ns as it comes and goes.
func (b *Builder) Merge(ns string, labels set.Set[string]) {
	if labels.Len() == 0 {
		return
	}
	if cur, ok := b.l[ns]; ok {
		// A key at a time rather than through the variadic Insert, which can
		// only be called on a materialized slice and so pays an UnsortedList
		// per merge. This is the innermost statement of the key merge -- it
		// runs once per namespace a peer's namespaceSelector matches, per such
		// peer, per policy, which is hundreds of times per policy on a cluster
		// with many namespaces -- and that one slice was four fifths of
		// everything NPolicies.Init allocated.
		for key := range labels {
			cur.Insert(key)
		}
		return
	}
	if b.l == nil {
		b.l = PerNsLabels{}
	}
	b.l[ns] = labels.Clone()
}

// Build freezes what has been merged so far into an RLabels and resets the
// Builder, so a reused Builder starts from an empty set rather than continuing
// the one it just handed over.
//
// The RLabels takes the accumulated map itself rather than a copy. It is then
// the immutable side of a comparison -- kept as the old state, diffed against
// the next one, and handing its own sets out through Diff -- which a later
// Merge into the same ns would write through.
func (b *Builder) Build() *RLabels {
	rl := &RLabels{l: b.l}
	b.l = nil
	return rl
}

// PerNsLabels maps from ns -> labels for the namespace.
type PerNsLabels map[string]set.Set[string]

// RLabels encapsulates the Pod labels that are relevant for policy decisions
// on each namespace.
type RLabels struct {
	// l is a map from ns -> labels.
	l PerNsLabels
}

// Labels returns the relevant labels of ns, and nil for a ns with none: a
// namespace absent from the map and one recorded with an empty set mean the
// same thing, that every object in it projects onto the empty label set.
//
// The set itself is returned rather than a copy, as Diff's payloads are, so it
// must not be mutated.
func (rl *RLabels) Labels(ns string) set.Set[string] {
	return rl.l[ns]
}

// Diff partitions the namespaces whose relevant labels differ between two
// RLabels. The three sets are disjoint and keyed by ns: a namespace appears in
// exactly one of them, or in none if its labels are the same on both sides.
//
// The payload is the label set the ns has on the side that has it -- the new
// set for Added and Changed, the old one for Removed -- rather than the keys
// that moved. Every consumer re-projects the whole namespace anyway (a pod's
// equivalence class is a projection onto the entire set, so one key entering it
// invalidates the projection), and the old set is what says which keys stop
// being tracked once a ns drops out.
type Diff struct {
	Added   PerNsLabels
	Removed PerNsLabels
	Changed PerNsLabels
}

// Difference reports how `rl` differs from `other`, treating `rl` as the new
// state and `other` as the old one: a ns only `rl` has is Added, a ns only
// `other` has is Removed, and a ns both have with unequal label sets is
// Changed.
//
// Returns nil if there are no differences, so a caller can test the result
// itself rather than three maps. The maps within are likewise left nil when
// their category is empty, which is the common case -- a policy change usually
// touches one ns.
func (rl *RLabels) Difference(other *RLabels) *Diff {
	var d Diff

	for ns, labels := range rl.l {
		otherLabels, ok := other.l[ns]
		switch {
		case !ok:
			d.Added = insert(d.Added, ns, labels)
		case !labels.Equal(otherLabels):
			d.Changed = insert(d.Changed, ns, labels)
		}
	}

	// Only the namespaces missing from `rl` are left: the pass above has already
	// settled every ns the two have in common.
	for ns, labels := range other.l {
		if _, ok := rl.l[ns]; !ok {
			d.Removed = insert(d.Removed, ns, labels)
		}
	}

	if d.Added == nil && d.Removed == nil && d.Changed == nil {
		return nil
	}
	return &d
}

func insert(m PerNsLabels, ns string, labels set.Set[string]) PerNsLabels {
	if m == nil {
		m = PerNsLabels{}
	}
	m[ns] = labels
	return m
}
