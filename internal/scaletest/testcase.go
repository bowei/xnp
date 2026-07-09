// Package scaletest generates large, synthetic sets of Kubernetes
// Namespaces, Pods, and NetworkPolicies for exercising internal/policy's
// renderer (and future rendering-algorithm work) at scale.
package scaletest

import (
	"fmt"
	"math/rand"
	"net"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Config controls the shape and size of a generated TestCase. Policy counts
// scale per-namespace (Namespaces * PoliciesPerNamespace); pod count scales
// as Nodes * PodsPerNode, independent of Namespaces.
type Config struct {
	// Seed makes generation deterministic: the same Config and Seed always
	// produce byte-identical output, so runs are comparable across renderer
	// implementations/versions.
	Seed int64

	Namespaces int

	// Nodes and PodsPerNode determine the total pod count
	// (Nodes * PodsPerNode), distributed round-robin across both
	// Namespaces and Nodes. Node is modeled explicitly (via
	// Pod.Spec.NodeName) because the thing actually being scale-tested is
	// the per-node rule set the lightweight agent has to carry, per the
	// README's architecture.
	Nodes       int
	PodsPerNode int

	PoliciesPerNamespace int
	// RulesPerPolicy is the number of ingress rules and, independently, the
	// number of egress rules a policy gets (subject to IngressFraction /
	// EgressFraction below).
	RulesPerPolicy int
	PeersPerRule   int
	PortsPerRule   int

	// IngressFraction and EgressFraction are the probability [0,1] that a
	// given policy has PolicyTypes covering that direction. A policy with
	// neither is regenerated as ingress-only, since Kubernetes requires at
	// least one type.
	IngressFraction float64
	EgressFraction  float64

	// LabelCardinality bounds how many distinct label values are used for
	// the "app" and "tier" pod/namespace labels. Small values make
	// PodSelector/NamespaceSelector rules match many objects (worst case for
	// a renderer that materializes tuples); large values approximate
	// mostly-disjoint selectors (closer to real-world fan-out).
	LabelCardinality int

	// PodCIDR is the base range pod IPs are allocated from sequentially, so
	// runs are reproducible and never collide. It must be large enough to
	// hold Nodes * PodsPerNode addresses.
	PodCIDR string

	// IPBlockFraction is the probability [0,1] that a generated peer is an
	// IPBlock (a random /24 carved out of PodCIDR) rather than a
	// pod/namespace selector peer.
	IPBlockFraction float64
}

// DefaultConfig returns a moderate-size, deterministic Config suitable as a
// starting point for scale tests.
func DefaultConfig() Config {
	return Config{
		Seed:                 1,
		Namespaces:           100,
		Nodes:                100,
		PodsPerNode:          100,
		PoliciesPerNamespace: 5,
		RulesPerPolicy:       3,
		PeersPerRule:         2,
		PortsPerRule:         2,
		IngressFraction:      0.9,
		EgressFraction:       0.5,
		LabelCardinality:     10,
		PodCIDR:              "10.0.0.0/8",
		IPBlockFraction:      0.2,
	}
}

// TestCase is a generated set of API objects, ready to pass to
// internal/policy.Render (or to serialize/replay against a live renderer).
type TestCase struct {
	Namespaces []*corev1.Namespace
	Pods       []*corev1.Pod
	Policies   []*networkingv1.NetworkPolicy
}

// Generate builds a TestCase from cfg. It is deterministic: the same cfg
// always yields the same output, independent of machine or Go version.
func Generate(cfg Config) (*TestCase, error) {
	if cfg.Namespaces <= 0 {
		return nil, fmt.Errorf("scaletest: Namespaces must be > 0")
	}
	if cfg.Nodes <= 0 {
		return nil, fmt.Errorf("scaletest: Nodes must be > 0")
	}
	ips, err := newIPAllocator(cfg.PodCIDR)
	if err != nil {
		return nil, fmt.Errorf("scaletest: %w", err)
	}
	totalPods := cfg.Nodes * cfg.PodsPerNode
	if need, have := totalPods, ips.size(); need > have {
		return nil, fmt.Errorf("scaletest: PodCIDR %s only holds %d addresses, need %d", cfg.PodCIDR, have, need)
	}

	rng := rand.New(rand.NewSource(cfg.Seed))
	tc := &TestCase{}

	for ni := 0; ni < cfg.Namespaces; ni++ {
		ns := genNamespace(ni, cfg, rng)
		tc.Namespaces = append(tc.Namespaces, ns)

		for pi := 0; pi < cfg.PoliciesPerNamespace; pi++ {
			tc.Policies = append(tc.Policies, genPolicy(ns.Name, pi, cfg, rng))
		}
	}

	// Pods are generated in one flat pass, round-robined across namespaces
	// and nodes independently, so the two dimensions vary orthogonally
	// (e.g. Nodes=100/Namespaces=100 doesn't just give one namespace per
	// node).
	for i := 0; i < totalPods; i++ {
		ns := tc.Namespaces[i%cfg.Namespaces]
		node := fmt.Sprintf("node-%d", i%cfg.Nodes)
		tc.Pods = append(tc.Pods, genPod(ns.Name, node, i, ips.next(), cfg, rng))
	}
	return tc, nil
}

func genNamespace(i int, cfg Config, rng *rand.Rand) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("ns-%d", i),
			Labels: labelSet(cfg, rng),
		},
	}
}

func genPod(namespace, node string, i int, ip net.IP, cfg Config, rng *rand.Rand) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("pod-%d", i),
			Labels:    labelSet(cfg, rng),
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{PodIP: ip.String()},
	}
}

// labelSet draws the shared "app"/"tier" labels used by both pods and
// namespaces, so PodSelector and NamespaceSelector rules generated below
// have something to match against.
func labelSet(cfg Config, rng *rand.Rand) map[string]string {
	card := cfg.LabelCardinality
	if card <= 0 {
		card = 1
	}
	return map[string]string{
		"app":  fmt.Sprintf("app-%d", rng.Intn(card)),
		"tier": fmt.Sprintf("tier-%d", rng.Intn(card)),
	}
}

func genPolicy(namespace string, i int, cfg Config, rng *rand.Rand) *networkingv1.NetworkPolicy {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      fmt.Sprintf("policy-%d", i),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: genSelector(cfg, rng),
		},
	}

	var types []networkingv1.PolicyType
	if rng.Float64() < cfg.IngressFraction {
		types = append(types, networkingv1.PolicyTypeIngress)
		for r := 0; r < cfg.RulesPerPolicy; r++ {
			np.Spec.Ingress = append(np.Spec.Ingress, genIngressRule(cfg, rng))
		}
	}
	if rng.Float64() < cfg.EgressFraction {
		types = append(types, networkingv1.PolicyTypeEgress)
		for r := 0; r < cfg.RulesPerPolicy; r++ {
			np.Spec.Egress = append(np.Spec.Egress, genEgressRule(cfg, rng))
		}
	}
	if len(types) == 0 {
		// Kubernetes requires at least one PolicyType; fall back to a
		// no-op ingress-only policy rather than silently dropping it.
		types = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
	}
	np.Spec.PolicyTypes = types

	return np
}

func genIngressRule(cfg Config, rng *rand.Rand) networkingv1.NetworkPolicyIngressRule {
	return networkingv1.NetworkPolicyIngressRule{
		From:  genPeers(cfg, rng),
		Ports: genPorts(cfg, rng),
	}
}

func genEgressRule(cfg Config, rng *rand.Rand) networkingv1.NetworkPolicyEgressRule {
	return networkingv1.NetworkPolicyEgressRule{
		To:    genPeers(cfg, rng),
		Ports: genPorts(cfg, rng),
	}
}

// genSelector returns a selector matching one of the "app" label values
// used by labelSet, giving it roughly 1/LabelCardinality selectivity.
func genSelector(cfg Config, rng *rand.Rand) metav1.LabelSelector {
	card := cfg.LabelCardinality
	if card <= 0 {
		card = 1
	}
	return metav1.LabelSelector{
		MatchLabels: map[string]string{"app": fmt.Sprintf("app-%d", rng.Intn(card))},
	}
}

func genPeers(cfg Config, rng *rand.Rand) []networkingv1.NetworkPolicyPeer {
	peers := make([]networkingv1.NetworkPolicyPeer, 0, cfg.PeersPerRule)
	for i := 0; i < cfg.PeersPerRule; i++ {
		if rng.Float64() < cfg.IPBlockFraction {
			peers = append(peers, networkingv1.NetworkPolicyPeer{
				IPBlock: &networkingv1.IPBlock{CIDR: randCIDR24(cfg.PodCIDR, rng)},
			})
			continue
		}
		sel := genSelector(cfg, rng)
		peer := networkingv1.NetworkPolicyPeer{PodSelector: &sel}
		if rng.Intn(2) == 0 {
			nsSel := genSelector(cfg, rng)
			peer.NamespaceSelector = &nsSel
		}
		peers = append(peers, peer)
	}
	return peers
}

func genPorts(cfg Config, rng *rand.Rand) []networkingv1.NetworkPolicyPort {
	protocols := []corev1.Protocol{corev1.ProtocolTCP, corev1.ProtocolUDP}
	ports := make([]networkingv1.NetworkPolicyPort, 0, cfg.PortsPerRule)
	for i := 0; i < cfg.PortsPerRule; i++ {
		proto := protocols[rng.Intn(len(protocols))]
		port := intstr.FromInt32(int32(1024 + rng.Intn(64512)))
		ports = append(ports, networkingv1.NetworkPolicyPort{
			Protocol: &proto,
			Port:     &port,
		})
	}
	return ports
}

// randCIDR24 picks a pseudo-random /24 within base (which must be an IPv4
// CIDR), for IPBlock peers that don't correspond to any generated pod.
func randCIDR24(base string, rng *rand.Rand) string {
	_, ipnet, err := net.ParseCIDR(base)
	if err != nil || ipnet.IP.To4() == nil {
		// Fall back to an arbitrary routable-looking /24; only reachable if
		// Config.PodCIDR is malformed, which Generate would otherwise have
		// already rejected via newIPAllocator.
		return fmt.Sprintf("192.0.2.%d/24", 0)
	}
	ip := append(net.IP(nil), ipnet.IP.To4()...)
	ip[2] = byte(rng.Intn(256))
	ip[3] = 0
	return fmt.Sprintf("%s/24", ip.String())
}
