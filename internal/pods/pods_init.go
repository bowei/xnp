package pods

import (
	"fmt"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/repr/convert"
	"github.com/bowei/xnp/internal/rlabels"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/set"
)

func (pods *Pods) Init(
	relevantLabels *rlabels.RLabels,
	podSeq func(yield func(*corev1.Pod) bool),
) error {
	pods.lock.Lock()
	defer pods.lock.Unlock()

	pods.rlabels = relevantLabels

	// Convert all Pods
	var err error
	for pod := range podSeq {
		var e *entry
		e, err = podToEntry(pod)
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

		// Equivalence class, projected onto the keys relevant in the pod's own
		// namespace. The entry carries it so that a later change to the
		// relevant labels can find the class the entry is leaving; recomputing
		// it from the old label set is not possible once that set is gone.
		e.EQC = convert.RelevantLabels(pod.Labels, pods.rlabels.Labels(pod.Namespace))

		// ns -> name -> Pod.
		inNs, ok := pods.pods[pod.Namespace]
		if !ok {
			inNs = map[string]*entry{}
			pods.pods[pod.Namespace] = inNs
		}
		inNs[pod.Name] = e

		// ns -> eqc -> name -> Pod.
		eqcKey := e.EQC.Comparable()
		byEQC, ok := pods.eqc[pod.Namespace]
		if !ok {
			byEQC = map[kv.MapKey]map[string]*entry{}
			pods.eqc[pod.Namespace] = byEQC
		}
		inEQC, ok := byEQC[eqcKey]
		if !ok {
			inEQC = map[string]*entry{}
			byEQC[eqcKey] = inEQC
		}
		inEQC[pod.Name] = e

		// node -> [eqc].
		if e.Node != "" {
			onNode, ok := pods.nodes[e.Node]
			if !ok {
				onNode = set.New[kv.MapKey]()
				pods.nodes[e.Node] = onNode
			}
			onNode.Insert(eqcKey)
		}
	}

	if err != nil {
		return fmt.Errorf("Pods.Init: %w", err)
	}

	return nil
}
