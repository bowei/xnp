package policy

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/bowei/xnp/internal/intern"
	"github.com/bowei/xnp/internal/repr"
)

// Input is the repr-converted form of the Pods/NetworkPolicies/Namespaces a
// Render* call renders. Building it (NewInput) is "phase 1": mapping raw
// Kubernetes resources into repr's memory-efficient mirrors, once, up front.
// Every Render* function is a pure function of an Input ("phase 2") and
// never sees a corev1/networkingv1 type.
type Input struct {
	Table      *intern.Table
	Policies   []repr.NetworkPolicy
	Pods       []repr.Pod
	Namespaces []repr.Namespace
}

// NewInput converts policies, pods, and namespaces into repr form, interning
// every string they reference into one shared intern.Table so intern.IDs are
// comparable across all three.
//
// Named (string) ports are validated here rather than during rendering (see
// repr.NewNetworkPolicy): a policy with an unsupported named port now fails
// conversion unconditionally, even if the policy never ends up selecting any
// pod, rather than only when a renderer actually evaluates that rule.
func NewInput(policies []*networkingv1.NetworkPolicy, pods []*corev1.Pod, namespaces []*corev1.Namespace) (*Input, error) {
	tbl := intern.NewTable()

	reprPolicies := make([]repr.NetworkPolicy, len(policies))
	for i, np := range policies {
		converted, err := repr.NewNetworkPolicy(np, tbl)
		if err != nil {
			return nil, err
		}
		reprPolicies[i] = converted
	}

	reprPods := make([]repr.Pod, len(pods))
	for i, pod := range pods {
		converted, err := repr.NewPod(pod, tbl)
		if err != nil {
			return nil, err
		}
		reprPods[i] = converted
	}

	reprNamespaces := make([]repr.Namespace, len(namespaces))
	for i, ns := range namespaces {
		reprNamespaces[i] = repr.NewNamespace(ns, tbl)
	}

	return &Input{
		Table:      tbl,
		Policies:   reprPolicies,
		Pods:       reprPods,
		Namespaces: reprNamespaces,
	}, nil
}
