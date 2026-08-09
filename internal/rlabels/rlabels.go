package rlabels

import "k8s.io/utils/set"

type PerNsLabelSet map[string]set.Set[string]

// RLabels encapsulates the Pod labels that are relevant for policy decisions
// on each namespace.
type RLabels struct {
	// l is a map from ns -> labels.
	l PerNsLabelSet
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
	Added   PerNsLabelSet
	Removed PerNsLabelSet
	Changed PerNsLabelSet
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

func insert(m PerNsLabelSet, ns string, labels set.Set[string]) PerNsLabelSet {
	if m == nil {
		m = PerNsLabelSet{}
	}
	m[ns] = labels
	return m
}
