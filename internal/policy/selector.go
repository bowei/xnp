package policy

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// matchesLabelSelector reports whether lbls satisfy sel. An empty selector
// (no MatchLabels/MatchExpressions) matches everything, per Kubernetes
// LabelSelector semantics.
func matchesLabelSelector(sel metav1.LabelSelector, lbls map[string]string) (bool, error) {
	s, err := metav1.LabelSelectorAsSelector(&sel)
	if err != nil {
		return false, err
	}
	return s.Matches(labels.Set(lbls)), nil
}

// matchingNamespaces returns the sorted names of namespaces selected by sel.
func matchingNamespaces(sel *metav1.LabelSelector, namespaces []*corev1.Namespace) ([]string, error) {
	var names []string
	for _, ns := range namespaces {
		ok, err := matchesLabelSelector(*sel, ns.Labels)
		if err != nil {
			return nil, err
		}
		if ok {
			names = append(names, ns.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// podIPs returns the concrete IP addresses assigned to pod, preferring the
// dual-stack Status.PodIPs list and falling back to the singular
// Status.PodIP.
func podIPs(pod *corev1.Pod) []string {
	if len(pod.Status.PodIPs) > 0 {
		ips := make([]string, 0, len(pod.Status.PodIPs))
		for _, ip := range pod.Status.PodIPs {
			ips = append(ips, ip.IP)
		}
		return ips
	}
	if pod.Status.PodIP != "" {
		return []string{pod.Status.PodIP}
	}
	return nil
}
