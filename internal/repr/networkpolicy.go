package repr

import (
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewNetworkPolicy(np *networkingv1.NetworkPolicy) *NetworkPolicy {
	// TODO
}

// NetworkPolicy is the memory-efficient mirror of a
// networkingv1.NetworkPolicy.
type NetworkPolicy struct {
	Namespace   string
	Name        string
	PodSelector metav1.LabelSelector
	PolicyTypes []networkingv1.PolicyType
	Ingress     []Rule
	Egress      []Rule

	Original *networkingv1.NetworkPolicy
}

// Peer is the memory-efficient mirror of a networkingv1.NetworkPolicyPeer.
// Selectors are kept as raw metav1.LabelSelector — policies are low
// cardinality, so compiling them is a lookup-speed concern (see lookup.go's
// CompiledSelector), not a memory one.
type Peer struct {
	PodSelector       *metav1.LabelSelector
	NamespaceSelector *metav1.LabelSelector
	IPBlock           *IPBlock
}

// Rule is the memory-efficient mirror of a
// networkingv1.NetworkPolicyIngress/EgressRule.
type Rule struct {
	Peers []Peer
	Ports []Port
}

// Port is the memory-efficient mirror of a networkingv1.NetworkPolicyPort.
type Port struct {
	Protocol v1.Protocol
	Port     *int32
	EndPort  *int32
}

func newPeers(peers []networkingv1.NetworkPolicyPeer) ([]Peer, error) {
	if len(peers) == 0 {
		return nil, nil
	}
	out := make([]Peer, len(peers))
	for i, p := range peers {
		block, err := newIPBlock(p.IPBlock)
		if err != nil {
			return nil, err
		}
		out[i] = Peer{
			PodSelector:       p.PodSelector,
			NamespaceSelector: p.NamespaceSelector,
			IPBlock:           block,
		}
	}
	return out, nil
}

func newPorts(ports []networkingv1.NetworkPolicyPort) ([]Port, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	out := make([]Port, len(ports))
	for i, p := range ports {
		port, err := newPort(p)
		if err != nil {
			return nil, err
		}
		out[i] = port
	}
	return out, nil
}

// Named (string) ports are unsupported, matching internal/policy's
// resolvePorts: resolving them needs per-peer-pod container port lookups,
// so conversion errors rather than producing a silently wrong Port.
func newPort(p networkingv1.NetworkPolicyPort) (Port, error) {
	// TODO
}
