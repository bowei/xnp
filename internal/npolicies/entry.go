package npolicies

import (
	"fmt"
	"net/netip"

	"github.com/bowei/kvec/pkg/pred"
	"github.com/bowei/xnp/internal/repr/convert"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// entry is the memory-efficient mirror of a
// networkingv1.entry.
type entry struct {
	Namespace string
	Name      string

	PodSelector *pred.Predicate
	PolicyTypes []networkingv1.PolicyType
	Ingress     []rule
	Egress      []rule
}

// peer is the memory-efficient mirror of a networkingv1.NetworkPolicyPeer.
//
// A nil PodSelector/NamespaceSelector means the field was not specified,
// which the NetworkPolicy API distinguishes from an empty selector: an
// omitted namespaceSelector restricts the peer to the policy's own namespace,
// while an empty one matches every namespace.
type peer struct {
	PodSelector       *pred.Predicate
	NamespaceSelector *pred.Predicate
	IPBlock           *ipBlock
}

// rule is the memory-efficient mirror of a
// networkingv1.NetworkPolicyIngress/EgressRule.
type rule struct {
	Peers []peer
	Ports []port
}

// port is the memory-efficient mirror of a networkingv1.NetworkPolicyPort.
// A nil port means every port; a non-nil EndPort makes [port, EndPort] a
// range. Protocol is always one of TCP/UDP/SCTP, defaulted to TCP as the API
// defaults it.
type port struct {
	Protocol corev1.Protocol
	Port     *int32
	EndPort  *int32
}

// newEntry converts np into repr form, compiling its selectors for
// matching.
//
// Conversion fails rather than dropping or approximating anything it cannot
// represent: an unparseable ipBlock CIDR, an unsupported selector operator,
// or a named (string) port. Since this runs once per policy up front, a
// policy that would render wrongly is rejected even if no pod ever selects
// it, rather than at the point some renderer happens to evaluate the rule.
func newEntry(np *networkingv1.NetworkPolicy) (*entry, error) {
	podSelector, err := convert.SelectorToPred(&np.Spec.PodSelector)
	if err != nil {
		return nil, fmt.Errorf("networkpolicy %s/%s: podSelector: %w", np.Namespace, np.Name, err)
	}

	ingress := make([]rule, len(np.Spec.Ingress))
	for i, r := range np.Spec.Ingress {
		rule, err := newRule(r.From, r.Ports)
		if err != nil {
			return nil, fmt.Errorf("networkpolicy %s/%s: ingress rule %d: %w", np.Namespace, np.Name, i, err)
		}
		ingress[i] = rule
	}

	egress := make([]rule, len(np.Spec.Egress))
	for i, r := range np.Spec.Egress {
		rule, err := newRule(r.To, r.Ports)
		if err != nil {
			return nil, fmt.Errorf("networkpolicy %s/%s: egress rule %d: %w", np.Namespace, np.Name, i, err)
		}
		egress[i] = rule
	}

	return &entry{
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
) (rule, error) {
	convertedPeers, err := newPeers(peers)
	if err != nil {
		return rule{}, err
	}
	convertedPorts, err := newPorts(ports)
	if err != nil {
		return rule{}, err
	}
	return rule{Peers: convertedPeers, Ports: convertedPorts}, nil
}

func newPeers(peers []networkingv1.NetworkPolicyPeer) ([]peer, error) {
	if len(peers) == 0 {
		return nil, nil
	}
	out := make([]peer, len(peers))
	for i, p := range peers {
		podSelector, err := convert.SelectorToPred(p.PodSelector)
		if err != nil {
			return nil, fmt.Errorf("peer %d: podSelector: %w", i, err)
		}
		nsSelector, err := convert.SelectorToPred(p.NamespaceSelector)
		if err != nil {
			return nil, fmt.Errorf("peer %d: namespaceSelector: %w", i, err)
		}
		block, err := newIPBlock(p.IPBlock)
		if err != nil {
			return nil, fmt.Errorf("peer %d: %w", i, err)
		}
		out[i] = peer{
			PodSelector:       podSelector,
			NamespaceSelector: nsSelector,
			IPBlock:           block,
		}
	}
	return out, nil
}

func newPorts(ports []networkingv1.NetworkPolicyPort) ([]port, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	out := make([]port, len(ports))
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
func newPort(p networkingv1.NetworkPolicyPort) (port, error) {
	out := port{Protocol: corev1.ProtocolTCP}
	if p.Protocol != nil {
		switch *p.Protocol {
		case corev1.ProtocolTCP, corev1.ProtocolUDP, corev1.ProtocolSCTP:
			out.Protocol = *p.Protocol
		default:
			return port{}, fmt.Errorf("unsupported protocol %q", *p.Protocol)
		}
	}

	if p.Port != nil {
		if p.Port.Type != intstr.Int {
			return port{}, fmt.Errorf("named port %q is unsupported", p.Port.StrVal)
		}
		port := p.Port.IntVal
		out.Port = &port
	}

	if p.EndPort != nil {
		if out.Port == nil {
			return port{}, fmt.Errorf("endPort %d set without a port", *p.EndPort)
		}
		if *p.EndPort < *out.Port {
			return port{}, fmt.Errorf("endPort %d is less than port %d", *p.EndPort, *out.Port)
		}
		endPort := *p.EndPort
		out.EndPort = &endPort
	}

	return out, nil
}

// ipBlock is the memory-efficient mirror of a networkingv1.ipBlock.
//
// Except is stored for completeness but, matching internal/policy's
// documented simplification, is not carved out of CIDR by anything that
// consumes repr yet.
type ipBlock struct {
	CIDR   netip.Prefix
	Except []netip.Prefix
}

func newIPBlock(b *networkingv1.IPBlock) (*ipBlock, error) {
	if b == nil {
		return nil, nil
	}
	cidr, err := netip.ParsePrefix(b.CIDR)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", b.CIDR, err)
	}
	var except []netip.Prefix
	for _, e := range b.Except {
		p, err := netip.ParsePrefix(e)
		if err != nil {
			return nil, fmt.Errorf("invalid except CIDR %q: %w", e, err)
		}
		except = append(except, p)
	}
	return &ipBlock{CIDR: cidr, Except: except}, nil
}
