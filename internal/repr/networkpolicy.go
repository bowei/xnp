package repr

import (
	"fmt"

	"github.com/bowei/kvec/pkg/pred"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// NetworkPolicy is the memory-efficient mirror of a
// networkingv1.NetworkPolicy.
type NetworkPolicy struct {
	Namespace   string
	Name        string
	PodSelector *pred.Predicate
	PolicyTypes []networkingv1.PolicyType
	Ingress     []Rule
	Egress      []Rule
}

// Peer is the memory-efficient mirror of a networkingv1.NetworkPolicyPeer.
//
// A nil PodSelector/NamespaceSelector means the field was not specified,
// which the NetworkPolicy API distinguishes from an empty selector: an
// omitted namespaceSelector restricts the peer to the policy's own namespace,
// while an empty one matches every namespace.
type Peer struct {
	PodSelector       *pred.Predicate
	NamespaceSelector *pred.Predicate
	IPBlock           *IPBlock
}

// Rule is the memory-efficient mirror of a
// networkingv1.NetworkPolicyIngress/EgressRule.
type Rule struct {
	Peers []Peer
	Ports []Port
}

// Port is the memory-efficient mirror of a networkingv1.NetworkPolicyPort.
// A nil Port means every port; a non-nil EndPort makes [Port, EndPort] a
// range. Protocol is always one of TCP/UDP/SCTP, defaulted to TCP as the API
// defaults it.
type Port struct {
	Protocol corev1.Protocol
	Port     *int32
	EndPort  *int32
}

// NewNetworkPolicy converts np into repr form, compiling its selectors for
// matching.
//
// Conversion fails rather than dropping or approximating anything it cannot
// represent: an unparseable ipBlock CIDR, an unsupported selector operator,
// or a named (string) port. Since this runs once per policy up front, a
// policy that would render wrongly is rejected even if no pod ever selects
// it, rather than at the point some renderer happens to evaluate the rule.
func NewNetworkPolicy(np *networkingv1.NetworkPolicy) (*NetworkPolicy, error) {
	podSelector, err := selectorToPred(&np.Spec.PodSelector)
	if err != nil {
		return nil, fmt.Errorf("networkpolicy %s/%s: podSelector: %w", np.Namespace, np.Name, err)
	}

	ingress := make([]Rule, len(np.Spec.Ingress))
	for i, r := range np.Spec.Ingress {
		rule, err := newRule(r.From, r.Ports)
		if err != nil {
			return nil, fmt.Errorf("networkpolicy %s/%s: ingress rule %d: %w", np.Namespace, np.Name, i, err)
		}
		ingress[i] = rule
	}

	egress := make([]Rule, len(np.Spec.Egress))
	for i, r := range np.Spec.Egress {
		rule, err := newRule(r.To, r.Ports)
		if err != nil {
			return nil, fmt.Errorf("networkpolicy %s/%s: egress rule %d: %w", np.Namespace, np.Name, i, err)
		}
		egress[i] = rule
	}

	return &NetworkPolicy{
		Namespace:   np.Namespace,
		Name:        np.Name,
		PodSelector: podSelector,
		// Shares the original's backing array rather than cloning it. The API
		// objects repr mirrors are read-only to us, and the array holds nothing
		// but this policy's own policyTypes — at most two of them — so keeping
		// it retains no more than the copy would.
		PolicyTypes: np.Spec.PolicyTypes,
		Ingress:     ingress,
		Egress:      egress,
	}, nil
}

func newRule(
	peers []networkingv1.NetworkPolicyPeer,
	ports []networkingv1.NetworkPolicyPort,
) (Rule, error) {
	convertedPeers, err := newPeers(peers)
	if err != nil {
		return Rule{}, err
	}
	convertedPorts, err := newPorts(ports)
	if err != nil {
		return Rule{}, err
	}
	return Rule{Peers: convertedPeers, Ports: convertedPorts}, nil
}

func newPeers(peers []networkingv1.NetworkPolicyPeer) ([]Peer, error) {
	if len(peers) == 0 {
		return nil, nil
	}
	out := make([]Peer, len(peers))
	for i, p := range peers {
		podSelector, err := selectorToPred(p.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("peer %d: podSelector: %w", i, err)
		}
		nsSelector, err := selectorToPred(p.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("peer %d: namespaceSelector: %w", i, err)
		}
		block, err := newIPBlock(p.IPBlock)
		if err != nil {
			return nil, fmt.Errorf("peer %d: %w", i, err)
		}
		out[i] = Peer{
			PodSelector:       podSelector,
			NamespaceSelector: nsSelector,
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
			return nil, fmt.Errorf("port %d: %w", i, err)
		}
		out[i] = port
	}
	return out, nil
}

// Named (string) ports are unsupported, matching internal/policy's
// resolvePorts: resolving them needs per-peer-pod container port lookups,
// so conversion errors rather than producing a silently wrong Port.
func newPort(p networkingv1.NetworkPolicyPort) (Port, error) {
	out := Port{Protocol: corev1.ProtocolTCP}
	if p.Protocol != nil {
		switch *p.Protocol {
		case corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP:
			out.Protocol = *p.Protocol
		default:
			return Port{}, fmt.Errorf("unsupported protocol %q", *p.Protocol)
		}
	}

	if p.Port != nil {
		if p.Port.Type != intstr.Int {
			return Port{}, fmt.Errorf("named port %q is unsupported", p.Port.StrVal)
		}
		port := p.Port.IntVal
		out.Port = &port
	}

	if p.EndPort != nil {
		if out.Port == nil {
			return Port{}, fmt.Errorf("endPort %d set without a port", *p.EndPort)
		}
		if *p.EndPort < *out.Port {
			return Port{}, fmt.Errorf("endPort %d is less than port %d", *p.EndPort, *out.Port)
		}
		endPort := *p.EndPort
		out.EndPort = &endPort
	}

	return out, nil
}
