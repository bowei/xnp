// Package repr holds memory-efficient mirrors of the Kubernetes types
// internal/policy renders (Pod, NetworkPolicy, Namespace): IPs as
// netip.Addr/netip.Prefix instead of strings, and labels as an interned,
// pre-sorted form instead of map[string]string. This is storage-layer work
// only — see lookup.go for the matching-layer structures (compiled
// selectors, inverted index, bitsets) built on top of it.
package repr

import (
	"fmt"
	"net/netip"
	"sort"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// StringID is an interned string identity, allocated by SymbolTable.
type StringID uint32

// SymbolTable interns strings into dense StringIDs so repeated values
// (namespace names foremost — every pod in a namespace carries its own
// decoded copy of the same string) collapse to one allocation, and
// comparisons in hot paths become integer equality.
type SymbolTable struct {
	ids    map[string]StringID
	values []string
}

func NewSymbolTable() *SymbolTable {
	return &SymbolTable{ids: map[string]StringID{}}
}

func (t *SymbolTable) Intern(s string) StringID {
	if id, ok := t.ids[s]; ok {
		return id
	}
	id := StringID(len(t.values))
	t.ids[s] = id
	t.values = append(t.values, s)
	return id
}

// String resolves id back to its original string.
func (t *SymbolTable) String(id StringID) string {
	return t.values[id]
}

func labelKV(key, value StringID) uint64 {
	return uint64(key)<<32 | uint64(value)
}

// LabelSet is a pod's or namespace's labels canonicalized into interned
// integer form: kv gives O(1) key lookup, sorted gives a canonical,
// hashable, iterable form for later identity/index work (see lookup.go).
type LabelSet struct {
	kv     map[StringID]StringID
	sorted []uint64
}

// NewLabelSet interns labels' keys/values via tbl and returns their
// canonical LabelSet form.
func NewLabelSet(labels map[string]string, tbl *SymbolTable) LabelSet {
	kv := make(map[StringID]StringID, len(labels))
	sorted := make([]uint64, 0, len(labels))
	for k, v := range labels {
		key, val := tbl.Intern(k), tbl.Intern(v)
		kv[key] = val
		sorted = append(sorted, labelKV(key, val))
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return LabelSet{kv: kv, sorted: sorted}
}

// Get returns the interned value StringID for key, if present.
func (ls LabelSet) Get(key StringID) (StringID, bool) {
	v, ok := ls.kv[key]
	return v, ok
}

// Len is the number of labels in the set.
func (ls LabelSet) Len() int {
	return len(ls.sorted)
}

// Protocol is corev1.Protocol packed into a byte instead of a string; the
// NetworkPolicy API only ever produces one of the three values below.
type Protocol uint8

const (
	ProtocolTCP Protocol = iota
	ProtocolUDP
	ProtocolSCTP
)

func protocolFromK8s(p corev1.Protocol) (Protocol, error) {
	switch p {
	case corev1.ProtocolTCP:
		return ProtocolTCP, nil
	case corev1.ProtocolUDP:
		return ProtocolUDP, nil
	case corev1.ProtocolSCTP:
		return ProtocolSCTP, nil
	default:
		return 0, fmt.Errorf("unsupported protocol %q", p)
	}
}

// Pod is the memory-efficient mirror of a corev1.Pod.
type Pod struct {
	Namespace StringID
	Name      string
	Labels    LabelSet
	IPs       []netip.Addr
}

// NewPod converts pod into repr form, interning its namespace/labels via
// tbl. Pod IPs are drawn from Status.PodIPs (falling back to Status.PodIP),
// mirroring internal/policy's podIPs(); a pod with no IP assigned yet
// yields a nil IPs slice rather than an error.
func NewPod(pod *corev1.Pod, tbl *SymbolTable) (Pod, error) {
	ips, err := parsePodIPs(pod)
	if err != nil {
		return Pod{}, fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return Pod{
		Namespace: tbl.Intern(pod.Namespace),
		Name:      pod.Name,
		Labels:    NewLabelSet(pod.Labels, tbl),
		IPs:       ips,
	}, nil
}

func parsePodIPs(pod *corev1.Pod) ([]netip.Addr, error) {
	var raw []string
	if len(pod.Status.PodIPs) > 0 {
		for _, ip := range pod.Status.PodIPs {
			raw = append(raw, ip.IP)
		}
	} else if pod.Status.PodIP != "" {
		raw = []string{pod.Status.PodIP}
	}
	if len(raw) == 0 {
		return nil, nil
	}
	ips := make([]netip.Addr, 0, len(raw))
	for _, s := range raw {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("invalid pod IP %q: %w", s, err)
		}
		ips = append(ips, addr)
	}
	return ips, nil
}

// Namespace is the memory-efficient mirror of a corev1.Namespace.
type Namespace struct {
	Name   StringID
	Labels LabelSet
}

// NewNamespace converts ns into repr form, interning its name/labels via tbl.
func NewNamespace(ns *corev1.Namespace, tbl *SymbolTable) Namespace {
	return Namespace{
		Name:   tbl.Intern(ns.Name),
		Labels: NewLabelSet(ns.Labels, tbl),
	}
}

// IPBlock is the memory-efficient mirror of a networkingv1.IPBlock.
//
// Except is stored for completeness but, matching internal/policy's
// documented simplification, is not carved out of CIDR by anything that
// consumes repr yet.
type IPBlock struct {
	CIDR   netip.Prefix
	Except []netip.Prefix
}

func newIPBlock(b *networkingv1.IPBlock) (*IPBlock, error) {
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
	return &IPBlock{CIDR: cidr, Except: except}, nil
}

// Port is the memory-efficient mirror of a networkingv1.NetworkPolicyPort.
type Port struct {
	Protocol Protocol
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
	protocol, err := protocolFromK8s(proto)
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

// NetworkPolicy is the memory-efficient mirror of a
// networkingv1.NetworkPolicy.
type NetworkPolicy struct {
	Namespace   StringID
	Name        string
	PodSelector metav1.LabelSelector
	PolicyTypes []networkingv1.PolicyType
	Ingress     []Rule
	Egress      []Rule
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
	}, nil
}
