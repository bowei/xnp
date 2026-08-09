package npolicies

import (
	"fmt"
	"net/netip"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/kvec/pkg/pred"
	"github.com/bowei/xnp/internal/repr/convert"
	"github.com/bowei/xnp/internal/rlabels"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/set"
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

// mergeKeys registers every label key np selects on with rl, attributed to the
// namespaces whose objects are matched against them.
//
// The keys are read off the API object rather than the compiled entry: a
// Predicate is built for matching and drops terms that pred.Build proves
// redundant, while a key stays relevant as long as some selector names it. The
// compiled entry is what the namespaceSelectors are *matched* with, since
// newEntry has already built those Predicates and e mirrors np rule for rule
// and peer for peer.
//
// Most keys land in the policy's own namespace: that is where its podSelector
// selects, and where a peer whose namespaceSelector is omitted is confined. A
// peer that does carry a namespaceSelector instead selects pods in every
// namespace that selector matches, so its podSelector keys are registered
// against those namespaces -- a pod there projected without them lands in an
// equivalence class the peer selector then reads wrongly.
//
// Pod and Namespace keys are not separated. A peer's namespaceSelector names
// Namespace labels and every other selector names Pod labels, but keeping one
// set costs only tracking a few keys that cannot matter for one of the two
// kinds -- an extra key splits equivalence classes finer than it has to, which
// is the harmless direction -- and never drops one that can. Those keys stay
// with the policy's own namespace: they are read off Namespaces, which no
// namespace owns.
// bowei-- this may need to be revisited.
func (e *entry) mergeKeys(
	rl *rlabels.Builder,
	np *networkingv1.NetworkPolicy,
	nsLabels map[string]*kv.KV,
) {
	own := set.New[string]()
	addSelectorKeys(own, &np.Spec.PodSelector)
	for i, r := range np.Spec.Ingress {
		e.Ingress[i].mergePeerKeys(rl, own, r.From, nsLabels)
	}
	for i, r := range np.Spec.Egress {
		e.Egress[i].mergePeerKeys(rl, own, r.To, nsLabels)
	}

	// Merged last: rl.Merge copies the set it is handed, so own can be filled
	// in one pass and handed over once rather than per selector.
	rl.Merge(e.Namespace, own)
}

// mergePeerKeys adds the keys peers name either to own -- the policy's own
// namespace, settled by the caller -- or, for a peer reaching into other
// namespaces, straight into rl under each namespace it reaches.
func (r *rule) mergePeerKeys(
	rl *rlabels.Builder,
	own set.Set[string],
	peers []networkingv1.NetworkPolicyPeer,
	nsLabels map[string]*kv.KV,
) {
	for i, peer := range peers {
		addSelectorKeys(own, peer.NamespaceSelector)
		if peer.NamespaceSelector == nil {
			addSelectorKeys(own, peer.PodSelector)
			continue
		}

		// An empty namespaceSelector is not an omitted one: it matches every
		// namespace, and its Predicate carries no terms, so the walk below
		// reaches all of them without a case of its own.
		//
		// A peer with no podSelector selects every pod in the namespaces it
		// reaches and so names no keys: there is nothing to register, and the
		// walk over every namespace is skipped rather than run for an empty
		// set.
		podKeys := set.New[string]()
		addSelectorKeys(podKeys, peer.PodSelector)
		if podKeys.Len() == 0 {
			continue
		}
		for ns, labels := range nsLabels {
			if r.Peers[i].NamespaceSelector.Match(labels) {
				rl.Merge(ns, podKeys)
			}
		}
	}
}

// addSelectorKeys adds the keys sel names to keys. Every operator counts,
// including DoesNotExist and NotIn: an object is matched against its labels
// projected onto the relevant keys, so a key dropped from that projection
// would read as absent and satisfy an exclusion the real object fails.
func addSelectorKeys(keys set.Set[string], sel *metav1.LabelSelector) {
	if sel == nil {
		return
	}
	for key := range sel.MatchLabels {
		keys.Insert(key)
	}
	for _, expr := range sel.MatchExpressions {
		keys.Insert(expr.Key)
	}
}
