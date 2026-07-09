package policy

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// RenderBasic compiles policies into the Tuples that should be enforced at each
// of pods. namespaces supplies the label data needed to resolve
// namespaceSelector peers.
func RenderBasic(policies []*networkingv1.NetworkPolicy, pods []*corev1.Pod, namespaces []*corev1.Namespace) ([]Tuple, error) {
	sortedPods := append([]*corev1.Pod(nil), pods...)
	sort.Slice(sortedPods, func(i, j int) bool {
		if sortedPods[i].Namespace != sortedPods[j].Namespace {
			return sortedPods[i].Namespace < sortedPods[j].Namespace
		}
		return sortedPods[i].Name < sortedPods[j].Name
	})

	podsByNamespace := map[string][]*corev1.Pod{}
	for _, p := range sortedPods {
		podsByNamespace[p.Namespace] = append(podsByNamespace[p.Namespace], p)
	}

	policiesByNamespace := map[string][]*networkingv1.NetworkPolicy{}
	for _, np := range policies {
		policiesByNamespace[np.Namespace] = append(policiesByNamespace[np.Namespace], np)
	}
	for ns := range policiesByNamespace {
		nps := policiesByNamespace[ns]
		sort.Slice(nps, func(i, j int) bool { return nps[i].Name < nps[j].Name })
	}

	var tuples []Tuple
	for _, pod := range sortedPods {
		podTuples, err := renderPod(pod, policiesByNamespace[pod.Namespace], podsByNamespace, namespaces)
		if err != nil {
			return nil, fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		tuples = append(tuples, podTuples...)
	}
	return tuples, nil
}
