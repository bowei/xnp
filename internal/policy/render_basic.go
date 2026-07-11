package policy

import (
	"fmt"

	"github.com/bowei/xnp/internal/store"
)

// RenderBasic compiles in's policies into the Tuples that should be enforced
// at each of in's pods, writing them into s. in.Namespaces supplies the label
// data needed to resolve namespaceSelector peers.
func RenderBasic(in *Input, s store.Store) error {
	sortedPods, podsByNamespace, policiesByNamespace := prepareRender(in)

	for _, pod := range sortedPods {
		podTuples, err := renderPod(in, pod, policiesByNamespace[pod.Namespace], podsByNamespace)
		if err != nil {
			return fmt.Errorf("pod %s/%s: %w", in.Table.String(pod.Namespace), pod.Name, err)
		}
		s.AddAll(podTuples)
	}
	return nil
}
