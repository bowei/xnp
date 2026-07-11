package repr

import (
	"fmt"

	"github.com/bowei/xnp/internal/tuple"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// NetworkPolicy is the memory-efficient mirror of a
// networkingv1.NetworkPolicy.
type NetworkPolicy struct {
	Namespace   StringID
	Name        string
	PodSelector metav1.LabelSelector
	PolicyTypes []networkingv1.PolicyType
	Ingress     []Rule
	Egress      []Rule

	Original *networkingv1.NetworkPolicy
}

// NewNetworkPolicy converts np into repr form, interning its namespace via
// tbl.
func NewNetworkPolicy(np *networkingv1.NetworkPolicy, tbl *SymbolTable) (NetworkPolicy, error) {
	ingress := make([]Rule, len(np.Spec.Ingress))
	for i, rule := range np.Spec.Ingress {
		peers, err := newPeers(rule.From)
		if err != nil {
			return NetworkPolicy{}, fmt.Errorf("policy %s/%s ingress rule %d: %w", np.Namespace, np.Name, i, err)
		}
		ports, err := newPorts(rule.Ports)
		if err != nil {
			return NetworkPolicy{}, fmt.Errorf("policy %s/%s ingress rule %d: %w", np.Namespace, np.Name, i, err)
		}
		ingress[i] = Rule{Peers: peers, Ports: ports}
	}

	egress := make([]Rule, len(np.Spec.Egress))
	for i, rule := range np.Spec.Egress {
		peers, err := newPeers(rule.To)
		if err != nil {
			return NetworkPolicy{}, fmt.Errorf("policy %s/%s egress rule %d: %w", np.Namespace, np.Name, i, err)
		}
		ports, err := newPorts(rule.Ports)
		if err != nil {
			return NetworkPolicy{}, fmt.Errorf("policy %s/%s egress rule %d: %w", np.Namespace, np.Name, i, err)
		}
		egress[i] = Rule{Peers: peers, Ports: ports}
	}

	return NetworkPolicy{
		Namespace:   tbl.Intern(np.Namespace),
		Name:        np.Name,
		PodSelector: np.Spec.PodSelector,
		PolicyTypes: np.Spec.PolicyTypes,
		Ingress:     ingress,
		Egress:      egress,
		Original:    np,
	}, nil
}

// Port is the memory-efficient mirror of a networkingv1.NetworkPolicyPort.
type Port struct {
	Protocol tuple.Protocol
	Port     *int32
	EndPort  *int32
}

// Named (string) ports are unsupported, matching internal/policy's
// resolvePorts: resolving them needs per-peer-pod container port lookups,
// so conversion errors rather than producing a silently wrong Port.
func newPort(p networkingv1.NetworkPolicyPort) (Port, error) {
	proto := corev1.ProtocolTCP
	if p.Protocol != nil {
		proto = *p.Protocol
	}
	protocol, err := tuple.ProtocolFromK8s(proto)
	if err != nil {
		return Port{}, err
	}

	var port *int32
	if p.Port != nil {
		if p.Port.Type == intstr.String {
			return Port{}, fmt.Errorf("named ports are not supported: %q", p.Port.StrVal)
		}
		v := p.Port.IntVal
		port = &v
	}

	return Port{Protocol: protocol, Port: port, EndPort: p.EndPort}, nil
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

// Rule is the memory-efficient mirror of a
// networkingv1.NetworkPolicyIngress/EgressRule.
type Rule struct {
	Peers []Peer
	Ports []Port
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
