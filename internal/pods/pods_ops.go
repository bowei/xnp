package pods

import (
	"log/slog"

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

func (pods *Pods) UpdateRLabels(newRLabels set.Set[string]) {
	slog.Info("UpdatRLabels", "newRLables", newRLabels)

	pods.lock.Lock()
	defer pods.lock.Unlock()

	// 1) Compute difference needed in the update.
	added := newRLabels.Difference(pods.rlabels)
	removed := pods.rlabels.Difference(newRLabels)

	// Naive algorithm: go through all Pods and recompute everything.
	for ns, podMap := range pods.pods {
		var changedEntries []*entry

		// 2) Find entries that will change with the update to the labels and
		//    rewrite their EQC.
		// 3) Add to changedEntries, but don't update the maps.
		for name, entry := range podMap {
			slog.Info("Processing", "ns", ns, "name", name, "entry", entry)

		}

		// 4) Update map entries.
		// 5) Recompute nodes entry for the namespace.
	}
}
