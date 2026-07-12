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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/bowei/xnp/internal/repr"
	"github.com/bowei/xnp/internal/tuple"
)

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

// protocolToK8s converts repr's packed Protocol back to the string form
// Tuple carries, at the render output boundary.
func protocolToK8s(p tuple.Protocol) corev1.Protocol {
	switch p {
	case tuple.ProtocolUDP:
		return corev1.ProtocolUDP
	case tuple.ProtocolSCTP:
		return corev1.ProtocolSCTP
	default:
		return corev1.ProtocolTCP
	}
}

// podIPStrings renders pod's interned IPs back to text form once per pod, so
// per-tuple materialization (writeTuples) never re-stringifies the same
// address.
func podIPStrings(pod repr.Pod) []string {
	if len(pod.IPs) == 0 {
		return nil
	}
	ips := make([]string, len(pod.IPs))
	for i, addr := range pod.IPs {
		ips[i] = addr.String()
	}
	return ips
}

// prepareRender sorts pods into a deterministic order and buckets both pods
// and policies by namespace, ready for per-pod rendering. Shared by
// RenderBasic and RenderParallel so their inputs and pod ordering can't
// drift apart. Policies are bucketed by index into in.Policies rather than
// copied, so policy identity (used by the indexed renderers to key
// SelectorIDs) stays stable.
func prepareRender(in *Input) (sortedPods []repr.Pod, podsByNamespace map[repr.StringID][]repr.Pod, policiesByNamespace map[repr.StringID][]int) {
	sortedPods = append([]repr.Pod(nil), in.Pods...)
	sort.Slice(sortedPods, func(i, j int) bool {
		pi, pj := sortedPods[i], sortedPods[j]
		if pi.Namespace != pj.Namespace {
			return in.Table.String(pi.Namespace) < in.Table.String(pj.Namespace)
		}
		return pi.Name < pj.Name
	})

	podsByNamespace = map[repr.StringID][]repr.Pod{}
	for _, p := range sortedPods {
		podsByNamespace[p.Namespace] = append(podsByNamespace[p.Namespace], p)
	}

	policiesByNamespace = map[repr.StringID][]int{}
	for i, np := range in.Policies {
		policiesByNamespace[np.Namespace] = append(policiesByNamespace[np.Namespace], i)
	}
	for ns := range policiesByNamespace {
		idxs := policiesByNamespace[ns]
		sort.Slice(idxs, func(i, j int) bool { return in.Policies[idxs[i]].Name < in.Policies[idxs[j]].Name })
	}

	return sortedPods, podsByNamespace, policiesByNamespace
}

// renderPod computes the full ingress+egress rule chain for a single pod.
func renderPod(in *Input, pod repr.Pod, nsPolicyIdx []int, podsByNamespace map[repr.StringID][]repr.Pod) ([]tuple.Tuple, error) {
	var selecting []int
	for _, idx := range nsPolicyIdx {
		np := in.Policies[idx]
		cs, err := repr.CompileSelector(np.PodSelector, in.Table)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", np.Name, err)
		}
		if cs.Matches(pod.Labels) {
			selecting = append(selecting, idx)
		}
	}

	var ingressIdx, egressIdx []int
	for _, idx := range selecting {
		ingress, egress := effectivePolicyTypes(in.Policies[idx])
		if ingress {
			ingressIdx = append(ingressIdx, idx)
		}
		if egress {
			egressIdx = append(egressIdx, idx)
		}
	}

	podName := types.NamespacedName{Namespace: in.Table.String(pod.Namespace), Name: pod.Name}
	ownIPs := podIPStrings(pod)

	var tuples []tuple.Tuple

	if len(ingressIdx) == 0 {
		ts, err := makeTuples(podName, ownIPs, tuple.DirectionIngress, tuple.VerdictAllow, peerEntry{wildcard: true}, portEntry{wildcard: true})
		if err != nil {
			return nil, err
		}
		tuples = append(tuples, ts...)
	} else {
		for _, idx := range ingressIdx {
			np := in.Policies[idx]
			for _, rule := range np.Ingress {
				peers, err := resolvePeers(rule.Peers, np.Namespace, podsByNamespace, in.Namespaces, in.Table)
				if err != nil {
					return nil, fmt.Errorf("policy %s ingress rule: %w", np.Name, err)
				}
				ports := resolvePorts(rule.Ports)
				for _, peer := range peers {
					for _, port := range ports {
						ts, err := makeTuples(podName, ownIPs, tuple.DirectionIngress, tuple.VerdictAllow, peer, port)
						if err != nil {
							return nil, fmt.Errorf("policy %s ingress rule: %w", np.Name, err)
						}
						tuples = append(tuples, ts...)
					}
				}
			}
		}
		ts, err := makeTuples(podName, ownIPs, tuple.DirectionIngress, tuple.VerdictDeny, peerEntry{wildcard: true}, portEntry{wildcard: true})
		if err != nil {
			return nil, err
		}
		tuples = append(tuples, ts...)
	}

	if len(egressIdx) == 0 {
		ts, err := makeTuples(podName, ownIPs, tuple.DirectionEgress, tuple.VerdictAllow, peerEntry{wildcard: true}, portEntry{wildcard: true})
		if err != nil {
			return nil, err
		}
		tuples = append(tuples, ts...)
	} else {
		for _, idx := range egressIdx {
			np := in.Policies[idx]
			for _, rule := range np.Egress {
				peers, err := resolvePeers(rule.Peers, np.Namespace, podsByNamespace, in.Namespaces, in.Table)
				if err != nil {
					return nil, fmt.Errorf("policy %s egress rule: %w", np.Name, err)
				}
				ports := resolvePorts(rule.Ports)
				for _, peer := range peers {
					for _, port := range ports {
						ts, err := makeTuples(podName, ownIPs, tuple.DirectionEgress, tuple.VerdictAllow, peer, port)
						if err != nil {
							return nil, fmt.Errorf("policy %s egress rule: %w", np.Name, err)
						}
						tuples = append(tuples, ts...)
					}
				}
			}
		}
		ts, err := makeTuples(podName, ownIPs, tuple.DirectionEgress, tuple.VerdictDeny, peerEntry{wildcard: true}, portEntry{wildcard: true})
		if err != nil {
			return nil, err
		}
		tuples = append(tuples, ts...)
	}

	return tuples, nil
}

// effectivePolicyTypes resolves np.PolicyTypes' defaulting behavior: Ingress
// is always assumed unless PolicyTypes is explicitly set and omits it;
// Egress is only assumed (when PolicyTypes is unset) if the policy has
// Egress rules.
func effectivePolicyTypes(np repr.NetworkPolicy) (ingress, egress bool) {
	if len(np.PolicyTypes) > 0 {
		for _, t := range np.PolicyTypes {
			switch t {
			case networkingv1.PolicyTypeIngress:
				ingress = true
			case networkingv1.PolicyTypeEgress:
				egress = true
			}
		}
		return ingress, egress
	}
	return true, len(np.Egress) > 0
}

// resolvePeers expands a repr.Peer list into concrete peer entries. An empty
// list means the rule applies to any peer.
//
// Known simplification: IPBlock.Except sub-ranges are not carved out of the
// CIDR in this baseline renderer.
func resolvePeers(peers []repr.Peer, policyNamespace repr.StringID, podsByNamespace map[repr.StringID][]repr.Pod, namespaces []repr.Namespace, tbl *repr.SymbolTable) ([]peerEntry, error) {
	if len(peers) == 0 {
		return []peerEntry{{wildcard: true}}, nil
	}

	var entries []peerEntry
	for _, peer := range peers {
		if peer.IPBlock != nil {
			entries = append(entries, peerEntry{addr: peer.IPBlock.CIDR.String()})
			continue
		}

		nsIDs := []repr.StringID{policyNamespace}
		if peer.NamespaceSelector != nil {
			ids, err := matchingNamespaces(*peer.NamespaceSelector, namespaces, tbl)
			if err != nil {
				return nil, err
			}
			nsIDs = ids
		}

		for _, ns := range nsIDs {
			for _, pod := range podsByNamespace[ns] {
				if peer.PodSelector != nil {
					cs, err := repr.CompileSelector(*peer.PodSelector, tbl)
					if err != nil {
						return nil, err
					}
					if !cs.Matches(pod.Labels) {
						continue
					}
				}
				for _, ip := range pod.IPs {
					entries = append(entries, peerEntry{addr: ip.String()})
				}
			}
		}
	}
	return entries, nil
}

// matchingNamespaces returns the interned names of every namespace selected
// by sel, sorted by name for determinism.
func matchingNamespaces(sel metav1.LabelSelector, namespaces []repr.Namespace, tbl *repr.SymbolTable) ([]repr.StringID, error) {
	cs, err := repr.CompileSelector(sel, tbl)
	if err != nil {
		return nil, err
	}
	var ids []repr.StringID
	for _, ns := range namespaces {
		if cs.Matches(ns.Labels) {
			ids = append(ids, ns.Name)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return tbl.String(ids[i]) < tbl.String(ids[j]) })
	return ids, nil
}

// resolvePorts expands a repr.Port list into concrete port entries. An empty
// list means the rule applies to any port/protocol. Unlike the pre-repr
// baseline, this can no longer fail: named (string) ports are rejected at
// conversion time (NewInput), not here.
func resolvePorts(ports []repr.Port) []portEntry {
	if len(ports) == 0 {
		return []portEntry{{wildcard: true}}
	}

	entries := make([]portEntry, len(ports))
	for i, p := range ports {
		entries[i] = portEntry{protocol: protocolToK8s(p.Protocol), port: p.Port, endPort: p.EndPort}
	}
	return entries
}

// makeTuples materializes one Tuple per own-IP of the enforcing pod (a pod
// may have more than one IP under dual-stack), combining it with a single
// resolved peer/port pair. It's a thin allocating wrapper around writeTuples
// for RenderBasic/RenderParallel, which don't know their total output size
// up front (see doc comment on RenderIndexed for why that matters at scale);
// RenderIndexed and RenderIndexedParallel call writeTuples directly into a
// preallocated result slice instead, to skip this allocation entirely.
func makeTuples(pod types.NamespacedName, ownIPs []string, dir tuple.Direction, verdict tuple.Verdict, peer peerEntry, port portEntry) ([]tuple.Tuple, error) {
	n := len(ownIPs)
	if n == 0 {
		n = 1
	}
	cols := tuple.NewColumns(n)
	if _, err := writeTuples(cols, pod, ownIPs, dir, verdict, peer, port); err != nil {
		return nil, err
	}
	return cols.ToSlice(), nil
}

// writeTuples is makeTuples' non-allocating counterpart: it writes directly
// into dst (which must have length >= max(len(ownIPs), 1)) and returns the
// count written, so a caller that already knows its total output size can
// fill one preallocated Columns instead of allocating and copying a
// temporary slice per call.
func writeTuples(
	dst tuple.Columns,
	pod types.NamespacedName,
	ownIPs []string,
	dir tuple.Direction,
	verdict tuple.Verdict,
	peer peerEntry,
	port portEntry,
) (int, error) {
	peerAddr := ""
	if !peer.wildcard {
		peerAddr = peer.addr
	}

	var protocol tuple.Protocol
	var portPtr, endPortPtr *int32
	if !port.wildcard {
		var err error
		protocol, err = tuple.ProtocolFromK8s(port.protocol)
		if err != nil {
			return 0, err
		}
		portPtr = port.port
		endPortPtr = port.endPort
	}

	ips := ownIPs
	if len(ips) == 0 {
		// Pod has no IP assigned yet; still emit the rule with an empty
		// own-address rather than silently dropping it.
		ips = []string{""}
	}

	for i, own := range ips {
		dst.Pod[i] = pod
		if dir == tuple.DirectionIngress {
			dst.Src[i], dst.Dest[i] = peerAddr, own
		} else {
			dst.Src[i], dst.Dest[i] = own, peerAddr
		}
		if portPtr != nil {
			dst.Port[i] = *portPtr
			dst.HasPort[i] = true
		}
		if endPortPtr != nil {
			dst.EndPort[i] = *endPortPtr
			dst.HasEndPort[i] = true
		}
		dst.Protocol[i] = protocol
		dst.Direction[i] = dir
		dst.Verdict[i] = verdict
	}
	return len(ips), nil
}
