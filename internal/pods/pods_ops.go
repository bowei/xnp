package pods

import (
	"log/slog"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/repr/convert"
	"k8s.io/utils/set"
)

func (pods *Pods) Add() {
	pods.lock.Lock()
	defer pods.lock.Unlock()

}

func (pods *Pods) Update() {
	pods.lock.Lock()
	defer pods.lock.Unlock()

}

func (pods *Pods) Remove() {
	pods.lock.Lock()
	defer pods.lock.Unlock()

}

// move is one pod's migration between equivalence classes. The destination is
// the entry's rewritten EQC; only the origin has to be carried, because
// rewriting the entry is what destroys the sole record of where it used to sit.
type move struct {
	entry *entry
	from  kv.MapKey
}

// UpdateRLabels re-groups the store onto a new set of relevant labels. A key
// enters or leaves that set as policies come and go, and the set is what every
// pod's equivalence class is a projection onto, so this is a whole-store
// operation rather than something any single pod's update can carry.
//
// The pods themselves do not change. They keep their labels raw precisely for
// this — the projection is the only thing that goes stale — so the re-grouping
// reads what is already stored and needs nothing from the API.
func (pods *Pods) UpdateRLabels(newRLabels set.Set[string]) {
	slog.Info("UpdateRLabels", "newRLabels", newRLabels)

	pods.lock.Lock()
	defer pods.lock.Unlock()

	// 1) Compute difference needed in the update.
	added := newRLabels.Difference(pods.rlabels)
	removed := pods.rlabels.Difference(newRLabels)
	if added.Len() == 0 && removed.Len() == 0 {
		return
	}
	// A pod's projection moves only if one of the pod's own label keys entered
	// or left the relevant set: a key the pod does not carry contributes
	// nothing to the projection either way. Checking that first is what keeps
	// the common case — a key or two changing, most pods untouched by it —
	// proportional to the pods that actually move rather than to a full
	// reprojection of the store.
	touched := added.Union(removed)

	pods.rlabels = newRLabels

	var moved bool

	// Naive algorithm: go through all Pods and recompute everything.
	for ns, podMap := range pods.pods {
		var changedEntries []move

		// 2) Find entries that will change with the update to the labels and
		//    rewrite their EQC.
		// 3) Add to changedEntries, but don't update the maps.
		for _, entry := range podMap {
			if !touchesKeys(entry.Labels, touched) {
				continue
			}
			// Recording the origin before the rewrite, not comparing the two
			// afterwards: touching a relevant key is already proof the
			// projection changed, since the key is in the new projection and
			// not the old, or the other way round.
			from := entry.EQC.Comparable()
			entry.EQC = convert.RelevantLabels(entry.Labels, newRLabels)
			changedEntries = append(changedEntries, move{entry: entry, from: from})
		}

		if len(changedEntries) == 0 {
			continue
		}
		moved = true

		// 4) Update map entries.
		byEQC := pods.eqc[ns]
		for _, m := range changedEntries {
			inFrom := byEQC[m.from]
			delete(inFrom, m.entry.Name)
			if len(inFrom) == 0 {
				// Drained classes are dropped rather than left empty, so that
				// walking the classes of a namespace costs what its live label
				// sets cost and not what every label set it has ever held does.
				delete(byEQC, m.from)
			}

			to := m.entry.EQC.Comparable()
			inTo, ok := byEQC[to]
			if !ok {
				inTo = map[string]*entry{}
				byEQC[to] = inTo
			}
			inTo[m.entry.Name] = m.entry
		}
	}

	// 5) Recompute nodes entry for the namespace.
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
