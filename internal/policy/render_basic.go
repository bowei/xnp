package policy

import (
	"fmt"

	"github.com/bowei/xnp/internal/tuple"
)

// RenderBasic compiles in's policies into the Tuples that should be enforced
// at each of in's pods. in.Namespaces supplies the label data needed to
// resolve namespaceSelector peers.
func RenderBasic(in *Input) (tuple.Store, error) {
	sortedPods, podsByNamespace, policiesByNamespace := prepareRender(in)

	store := tuple.NewStore(0)
	for _, pod := range sortedPods {
		podTuples, err := renderPod(in, pod, policiesByNamespace[pod.Namespace], podsByNamespace)
		if err != nil {
			return nil, fmt.Errorf("pod %s/%s: %w", in.Table.String(pod.Namespace), pod.Name, err)
		}
		store.AddAll(podTuples)
	}
	return store, nil
}
