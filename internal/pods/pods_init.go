package pods

import (
	"fmt"

	"github.com/bowei/xnp/internal/repr/convert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/set"
)

func (pods *Pods) Init(
	relevantLabels set.Set[string],
	podSeq func(yield func(*corev1.Pod) bool),
) error {
	pods.lock.Lock()
	defer pods.lock.Unlock()

	pods.rlabels = relevantLabels

	// Convert all Pods
	var err error
	for pod := range podSeq {
		var entry *entry
		entry, err = podToEntry(pod)
		if err != nil {
			// The iterator is stopped rather than returned from directly: a
			// range-over-func body cannot return for its caller, and running it
			// to completion after a failure would convert pods whose results
			// are already known to be discarded.
			//
			// TODO: handle this more elegantly, we should convert as many as
			// possible and flag the conversion issues.
			break
		}

		// ns -> name -> Pod.
		pods.pods[pod.Namespace][pod.Name] = entry

		// Equivalence class. ns -> eqc -> name -> Pod.
		eqcKV := convert.RelevantLabels(pod.Labels, pods.rlabels)
		pods.eqc[pod.Namespace][eqcKV.Comparable()][pod.Name] = entry
	}

	if err != nil {
		return fmt.Errorf("Pods.Init: %w", err)
	}

	return nil
}
