package pods

import (
	"log/slog"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/convert"
	"github.com/bowei/xnp/internal/rlabels"
	"k8s.io/utils/set"
)

func (pods *Store) Add() {
	pods.lock.Lock()
	defer pods.lock.Unlock()

}

func (pods *Store) Update() {
	pods.lock.Lock()
	defer pods.lock.Unlock()

}

func (pods *Store) Remove() {
	pods.lock.Lock()
	defer pods.lock.Unlock()

}

// move is one pod's migration between equivalence classes. The destination is
// the entry's rewritten EQC; only the origin has to be carried, because
// rewriting the entry is what destroys the sole record of where it used to sit.
type move struct {
	entry *Entry
	from  kv.MapKey
}

// UpdateRLabels re-groups the store onto a new set of relevant labels. A key
// enters or leaves that set as policies come and go, and the set is what every
// pod's equivalence class is a projection onto, so this is a store-wide
// operation rather than something any single pod's update can carry.
//
// The relevant labels are per namespace, so the re-grouping is confined to the
// namespaces the two sides actually differ in: a namespace whose keys are
// unchanged holds pods whose projections are all still correct, whatever
// happened elsewhere.
//
// The pods themselves do not change. They keep their labels raw precisely for
// this — the projection is the only thing that goes stale — so the re-grouping
// reads what is already stored and needs nothing from the API.
func (pods *Store) UpdateRLabels(newRLabels *rlabels.RLabels) {
	slog.Info("UpdateRLabels", "newRLabels", newRLabels)

	pods.lock.Lock()
	defer pods.lock.Unlock()

	// 1) Compute difference needed in the update. A nil Diff is no difference
	//    at all, which is the common case: most policy changes leave the set of
	//    keys the policies name exactly as it was.
	diff := newRLabels.Difference(pods.rlabels)
	if diff == nil {
		return
	}

	oldRLabels := pods.rlabels
	pods.rlabels = newRLabels

	var moved bool

	// 2) Re-group each namespace the difference names, onto the keys now
	//    relevant in it and against the keys that entered or left it.
	//
	// A namespace only the new side has had nothing relevant in it before, so
	// every key it now carries is one that entered; one only the old side has
	// projects onto the empty set from here on, so every key it used to carry
	// is one that left. Both are read straight off the Diff payload. Only a
	// namespace both sides have needs the two sets compared.
	for ns, labels := range diff.Added {
		moved = pods.regroupNs(ns, labels, labels) || moved
	}
	for ns, labels := range diff.Removed {
		moved = pods.regroupNs(ns, nil, labels) || moved
	}
	for ns, labels := range diff.Changed {
		moved = pods.regroupNs(ns, labels, labels.SymmetricDifference(oldRLabels.Labels(ns))) || moved
	}

	// 3) Recompute the nodes index.
	//
	// Rebuilt whole rather than per namespace, and only when something moved.
	// A node runs pods from any namespace and an EQC key is not namespace
	// qualified, so one namespace's pods cannot say which keys a node still
	// needs. Nor can a move be applied to the index directly: dropping the
	// origin key from a node is only correct once no pod left on that node
	// still projects onto it, which is a count this index does not keep. The
	// rebuild is one walk of the store, the same order the re-grouping above
	// already costs.
	if !moved {
		return
	}
	nodes := make(map[string]set.Set[kv.MapKey], len(pods.nodes))
	for _, podMap := range pods.pods {
		for _, entry := range podMap {
			if entry.Node == "" {
				// An unscheduled pod belongs to no node's rule set yet; it
				// lands in one when the scheduler's update arrives.
				continue
			}
			onNode, ok := nodes[entry.Node]
			if !ok {
				onNode = set.New[kv.MapKey]()
				nodes[entry.Node] = onNode
			}
			onNode.Insert(entry.EQC.Comparable())
		}
	}
	pods.nodes = nodes
}

// regroupNs re-projects the pods of ns onto labels, the keys now relevant
// there, and moves the ones whose equivalence class changed. touched is the
// keys that entered or left labels; a pod carrying none of them projects onto
// the same class as before, since a key the pod does not carry contributes
// nothing to the projection either way. Checking that first is what keeps the
// common case — a key or two changing, most pods untouched by it —
// proportional to the pods that actually move rather than to a full
// reprojection of the namespace.
//
// Reports whether any pod moved, which is what says the nodes index has to be
// rebuilt.
func (pods *Store) regroupNs(ns string, labels, touched set.Set[string]) bool {
	var changedEntries []move

	// Find entries that will change with the update to the labels and rewrite
	// their EQC, collecting them rather than updating the maps in the same
	// pass: the eqc index is what the walk would be mutating.
	for _, entry := range pods.pods[ns] {
		if !touchesKeys(entry.Labels, touched) {
			continue
		}
		// Recording the origin before the rewrite, not comparing the two
		// afterwards: touching a relevant key is already proof the projection
		// changed, since the key is in the new projection and not the old, or
		// the other way round.
		from := entry.EQC.Comparable()
		entry.EQC = convert.RelevantLabels(entry.Labels, labels)
		changedEntries = append(changedEntries, move{entry: entry, from: from})
	}

	if len(changedEntries) == 0 {
		return false
	}

	byEQC := pods.eqcToPod[ns]
	// Present whenever the namespace holds a pod, which having something to
	// move already establishes.
	classes := pods.eqc[ns]
	for _, m := range changedEntries {
		inFrom := byEQC[m.from]
		delete(inFrom, m.entry.Name)
		if len(inFrom) == 0 {
			// Drained classes are dropped rather than left empty, so that
			// walking the classes of a namespace costs what its live label
			// sets cost and not what every label set it has ever held does.
			// The class list is the same walk by another name and drains with
			// it; a class kept there after its last pod left would be a class
			// nothing can ever match a pod through.
			delete(byEQC, m.from)
			delete(classes, m.from)
		}

		to := m.entry.EQC.Comparable()
		inTo, ok := byEQC[to]
		if !ok {
			inTo = map[string]*Entry{}
			byEQC[to] = inTo
			classes[to] = m.entry.EQC
		}
		inTo[m.entry.Name] = m.entry
	}
	return true
}

// touchesKeys reports whether labels carries any of keys, and so whether a
// change confined to keys can change what labels project onto.
//
// Either side can be walked and neither is reliably the smaller — a pod carries
// tens of labels, while the relevant set is every key every policy names —
// so this walks the smaller one, as convert.RelevantLabels does and for the
// same reason: this runs once per pod on every change to the relevant set.
func touchesKeys(labels map[string]string, keys set.Set[string]) bool {
	if len(labels) <= len(keys) {
		for k := range labels {
			if keys.Has(k) {
				return true
			}
		}
		return false
	}
	for k := range keys {
		if _, ok := labels[k]; ok {
			return true
		}
	}
	return false
}
