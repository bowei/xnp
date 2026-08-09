package pods

import (
	"fmt"
	"net/netip"

	"github.com/bowei/kvec/pkg/kv"
	corev1 "k8s.io/api/core/v1"
)

type Entry struct {
	Namespace string
	Name      string
	Labels    map[string]string
	IPs       []netip.Addr

	// Node is the pod's assignment, empty until the scheduler makes one. It is
	// what the nodes index is keyed by: the rules a node is shipped are the
	// rules for the pods running on it.
	Node string

	// EQC for this entry.
	EQC *kv.KV
}

func podToEntry(pod *corev1.Pod) (*Entry, error) {
	ips, err := podIPs(pod)
	if err != nil {
		return nil, fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return &Entry{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Labels:    pod.Labels,
		IPs:       ips,
		Node:      pod.Spec.NodeName,
	}, nil
}

// podIPs reads the pod's addresses from status. PodIPs is the dual-stack
// field and is authoritative when set; PodIP is its single-stack predecessor
// and always holds PodIPs[0], so it is only read when PodIPs is empty.
func podIPs(pod *corev1.Pod) ([]netip.Addr, error) {
	if len(pod.Status.PodIPs) > 0 {
		ips := make([]netip.Addr, len(pod.Status.PodIPs))
		for i, ip := range pod.Status.PodIPs {
			addr, err := netip.ParseAddr(ip.IP)
			if err != nil {
				return nil, fmt.Errorf("invalid pod IP %q: %w", ip.IP, err)
			}
			ips[i] = addr
		}
		return ips, nil
	}
	if pod.Status.PodIP == "" {
		return nil, nil
	}
	addr, err := netip.ParseAddr(pod.Status.PodIP)
	if err != nil {
		return nil, fmt.Errorf("invalid pod IP %q: %w", pod.Status.PodIP, err)
	}
	return []netip.Addr{addr}, nil
}
