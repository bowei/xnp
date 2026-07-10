// Package policy renders a set of Kubernetes NetworkPolicies against a set
// of Pods into a flat list of concrete filtering tuples, as described in the
// "Network policy computation" section of the repo README. This is a
// baseline (brute-force, unoptimized) implementation intended as a
// correctness reference for later rendering-algorithm work.
package policy

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// Direction is the traffic direction a Tuple applies to.
type Direction string

const (
	DirectionIngress Direction = "ingress"
	DirectionEgress  Direction = "egress"
)

// Verdict is the action a Tuple applies.
type Verdict string

const (
	VerdictAllow Verdict = "allow"
	VerdictDeny  Verdict = "deny"
)

// Tuple is one compiled filtering rule to be enforced at Pod. Rules for a
// given (Pod, Direction) form an ordered, first-match-wins chain: explicit
// "allow" tuples followed by a single trailing wildcard tuple that carries
// the default verdict (allow if the pod is not isolated for that direction,
// deny if it is).
type Tuple struct {
	Pod  types.NamespacedName
	Src  string // concrete IP or CIDR; "" means any (wildcard)
	Dest string // concrete IP or CIDR; "" means any (wildcard)

	// Port/EndPort/Protocol are all zero-valued to mean "any" together;
	// Protocol is only meaningful when Port is set.
	Port     *int32
	EndPort  *int32
	Protocol corev1.Protocol

	Direction Direction
	Verdict   Verdict
}

// peerEntry is a resolved NetworkPolicyPeer: either a concrete address (pod
// IP or IPBlock CIDR) or the wildcard "any peer".
type peerEntry struct {
	addr     string
	wildcard bool
}

// portEntry is a resolved NetworkPolicyPort, or the wildcard "any port".
type portEntry struct {
	protocol corev1.Protocol
	port     *int32
	endPort  *int32
	wildcard bool
}

// prepareRender sorts pods into a deterministic order and buckets both pods
// and policies by namespace, ready for per-pod rendering. Shared by
// RenderBasic and RenderParallel so their inputs and pod ordering can't
// drift apart.
func prepareRender(policies []*networkingv1.NetworkPolicy, pods []*corev1.Pod) (sortedPods []*corev1.Pod, podsByNamespace map[string][]*corev1.Pod, policiesByNamespace map[string][]*networkingv1.NetworkPolicy) {
	sortedPods = append([]*corev1.Pod(nil), pods...)
	sort.Slice(sortedPods, func(i, j int) bool {
		if sortedPods[i].Namespace != sortedPods[j].Namespace {
			return sortedPods[i].Namespace < sortedPods[j].Namespace
		}
		return sortedPods[i].Name < sortedPods[j].Name
	})

	podsByNamespace = map[string][]*corev1.Pod{}
	for _, p := range sortedPods {
		podsByNamespace[p.Namespace] = append(podsByNamespace[p.Namespace], p)
	}

	policiesByNamespace = map[string][]*networkingv1.NetworkPolicy{}
	for _, np := range policies {
		policiesByNamespace[np.Namespace] = append(policiesByNamespace[np.Namespace], np)
	}
	for ns := range policiesByNamespace {
		nps := policiesByNamespace[ns]
		sort.Slice(nps, func(i, j int) bool { return nps[i].Name < nps[j].Name })
	}

	return sortedPods, podsByNamespace, policiesByNamespace
}

// renderPod computes the full ingress+egress rule chain for a single pod.
func renderPod(pod *corev1.Pod, nsPolicies []*networkingv1.NetworkPolicy, podsByNamespace map[string][]*corev1.Pod, namespaces []*corev1.Namespace) ([]Tuple, error) {
	var selecting []*networkingv1.NetworkPolicy
	for _, np := range nsPolicies {
		ok, err := matchesLabelSelector(np.Spec.PodSelector, pod.Labels)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", np.Name, err)
		}
		if ok {
			selecting = append(selecting, np)
		}
	}

	var ingressPolicies, egressPolicies []*networkingv1.NetworkPolicy
	for _, np := range selecting {
		ingress, egress := effectivePolicyTypes(np)
		if ingress {
			ingressPolicies = append(ingressPolicies, np)
		}
		if egress {
			egressPolicies = append(egressPolicies, np)
		}
	}

	podName := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
	ownIPs := podIPs(pod)

	var tuples []Tuple

	if len(ingressPolicies) == 0 {
		tuples = append(tuples, makeTuples(podName, ownIPs, DirectionIngress, VerdictAllow, peerEntry{wildcard: true}, portEntry{wildcard: true})...)
	} else {
		for _, np := range ingressPolicies {
			for _, rule := range np.Spec.Ingress {
				peers, err := resolvePeers(rule.From, np.Namespace, podsByNamespace, namespaces)
				if err != nil {
					return nil, fmt.Errorf("policy %s ingress rule: %w", np.Name, err)
				}
				ports, err := resolvePorts(rule.Ports)
				if err != nil {
					return nil, fmt.Errorf("policy %s ingress rule: %w", np.Name, err)
				}
				for _, peer := range peers {
					for _, port := range ports {
						tuples = append(tuples, makeTuples(podName, ownIPs, DirectionIngress, VerdictAllow, peer, port)...)
					}
				}
			}
		}
		tuples = append(tuples, makeTuples(podName, ownIPs, DirectionIngress, VerdictDeny, peerEntry{wildcard: true}, portEntry{wildcard: true})...)
	}

	if len(egressPolicies) == 0 {
		tuples = append(tuples, makeTuples(podName, ownIPs, DirectionEgress, VerdictAllow, peerEntry{wildcard: true}, portEntry{wildcard: true})...)
	} else {
		for _, np := range egressPolicies {
			for _, rule := range np.Spec.Egress {
				peers, err := resolvePeers(rule.To, np.Namespace, podsByNamespace, namespaces)
				if err != nil {
					return nil, fmt.Errorf("policy %s egress rule: %w", np.Name, err)
				}
				ports, err := resolvePorts(rule.Ports)
				if err != nil {
					return nil, fmt.Errorf("policy %s egress rule: %w", np.Name, err)
				}
				for _, peer := range peers {
					for _, port := range ports {
						tuples = append(tuples, makeTuples(podName, ownIPs, DirectionEgress, VerdictAllow, peer, port)...)
					}
				}
			}
		}
		tuples = append(tuples, makeTuples(podName, ownIPs, DirectionEgress, VerdictDeny, peerEntry{wildcard: true}, portEntry{wildcard: true})...)
	}

	return tuples, nil
}

// effectivePolicyTypes resolves np.Spec.PolicyTypes' defaulting behavior:
// Ingress is always assumed unless PolicyTypes is explicitly set and omits
// it; Egress is only assumed (when PolicyTypes is unset) if the policy has
// Egress rules.
func effectivePolicyTypes(np *networkingv1.NetworkPolicy) (ingress, egress bool) {
	if len(np.Spec.PolicyTypes) > 0 {
		for _, t := range np.Spec.PolicyTypes {
			switch t {
			case networkingv1.PolicyTypeIngress:
				ingress = true
			case networkingv1.PolicyTypeEgress:
				egress = true
			}
		}
		return ingress, egress
	}
	return true, len(np.Spec.Egress) > 0
}

// resolvePeers expands a NetworkPolicyPeer list into concrete peer entries.
// An empty list means the rule applies to any peer.
//
// Known simplification: IPBlock.Except sub-ranges are not carved out of the
// CIDR in this baseline renderer.
func resolvePeers(peers []networkingv1.NetworkPolicyPeer, policyNamespace string, podsByNamespace map[string][]*corev1.Pod, namespaces []*corev1.Namespace) ([]peerEntry, error) {
	if len(peers) == 0 {
		return []peerEntry{{wildcard: true}}, nil
	}

	var entries []peerEntry
	for _, peer := range peers {
		if peer.IPBlock != nil {
			entries = append(entries, peerEntry{addr: peer.IPBlock.CIDR})
			continue
		}

		nsNames := []string{policyNamespace}
		if peer.NamespaceSelector != nil {
			names, err := matchingNamespaces(peer.NamespaceSelector, namespaces)
			if err != nil {
				return nil, err
			}
			nsNames = names
		}

		for _, ns := range nsNames {
			for _, pod := range podsByNamespace[ns] {
				if peer.PodSelector != nil {
					ok, err := matchesLabelSelector(*peer.PodSelector, pod.Labels)
					if err != nil {
						return nil, err
					}
					if !ok {
						continue
					}
				}
				for _, ip := range podIPs(pod) {
					entries = append(entries, peerEntry{addr: ip})
				}
			}
		}
	}
	return entries, nil
}

// resolvePorts expands a NetworkPolicyPort list into concrete port entries.
// An empty list means the rule applies to any port/protocol.
//
// Known simplification: named (string) ports are not supported, since
// resolving them requires per-peer-pod container port lookups; such
// policies produce an error rather than a silently wrong tuple.
func resolvePorts(ports []networkingv1.NetworkPolicyPort) ([]portEntry, error) {
	if len(ports) == 0 {
		return []portEntry{{wildcard: true}}, nil
	}

	entries := make([]portEntry, 0, len(ports))
	for _, p := range ports {
		proto := corev1.ProtocolTCP
		if p.Protocol != nil {
			proto = *p.Protocol
		}

		var port *int32
		if p.Port != nil {
			if p.Port.Type == intstr.String {
				return nil, fmt.Errorf("named ports are not supported in this baseline renderer: %q", p.Port.StrVal)
			}
			v := p.Port.IntVal
			port = &v
		}

		entries = append(entries, portEntry{protocol: proto, port: port, endPort: p.EndPort})
	}
	return entries, nil
}

// makeTuples materializes one Tuple per own-IP of the enforcing pod (a pod
// may have more than one IP under dual-stack), combining it with a single
// resolved peer/port pair.
func makeTuples(pod types.NamespacedName, ownIPs []string, dir Direction, verdict Verdict, peer peerEntry, port portEntry) []Tuple {
	peerAddr := ""
	if !peer.wildcard {
		peerAddr = peer.addr
	}

	var protocol corev1.Protocol
	var portPtr, endPortPtr *int32
	if !port.wildcard {
		protocol = port.protocol
		portPtr = port.port
		endPortPtr = port.endPort
	}

	ips := ownIPs
	if len(ips) == 0 {
		// Pod has no IP assigned yet; still emit the rule with an empty
		// own-address rather than silently dropping it.
		ips = []string{""}
	}

	tuples := make([]Tuple, 0, len(ips))
	for _, own := range ips {
		t := Tuple{
			Pod:       pod,
			Port:      portPtr,
			EndPort:   endPortPtr,
			Protocol:  protocol,
			Direction: dir,
			Verdict:   verdict,
		}
		if dir == DirectionIngress {
			t.Src, t.Dest = peerAddr, own
		} else {
			t.Src, t.Dest = own, peerAddr
		}
		tuples = append(tuples, t)
	}
	return tuples
}
