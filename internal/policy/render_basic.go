package policy

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

// RenderBasic compiles policies into the Tuples that should be enforced at each
// of pods. namespaces supplies the label data needed to resolve
// namespaceSelector peers.
func RenderBasic(policies []*networkingv1.NetworkPolicy, pods []*corev1.Pod, namespaces []*corev1.Namespace) ([]Tuple, error) {
	sortedPods, podsByNamespace, policiesByNamespace := prepareRender(policies, pods)

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
