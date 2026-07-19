// Package basic renders a set of Kubernetes NetworkPolicies against a set of
// Pods into a flat list of concrete filtering tuples, as described in the
// "Network policy computation" section of the repo README. RenderBasic is a
// baseline (brute-force, unoptimized) implementation intended as a
// correctness reference for the optimized renderers in policy/indexed;
// RenderParallel is the same algorithm fanned out across a worker pool.
package basic

import (
	"fmt"

	"github.com/bowei/xnp/internal/policy/common"
	"github.com/bowei/xnp/internal/store"
)

// RenderBasic compiles in's policies into the Tuples that should be enforced
// at each of in's pods, writing them into s. in.Namespaces supplies the label
// data needed to resolve namespaceSelector peers.
func RenderBasic(in *common.Input, s store.Store) error {
	sortedPods, podsByNamespace, policiesByNamespace := common.PrepareRender(in)

	for _, pod := range sortedPods {
		podTuples, err := common.RenderPod(in, pod, policiesByNamespace[pod.Namespace], podsByNamespace)
		if err != nil {
			return fmt.Errorf("pod %s/%s: %w", in.Table.String(pod.Namespace), pod.Name, err)
		}
		s.AddAll(podTuples)
	}
	return nil
}
