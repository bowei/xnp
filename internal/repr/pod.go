package repr

import (
	"fmt"
	"net/netip"

	"github.com/bowei/kvec/pkg/kv"
	corev1 "k8s.io/api/core/v1"
)

// Pod is the memory-efficient mirror of a corev1.Pod.
type Pod struct {
	Namespace string
	Name      string
	Labels    kv.KV
	IPs       []netip.Addr

	Original *corev1.Pod
}

// NewPod converts pod into repr form: labels precompiled for matching, IPs
// parsed into netip.Addr. A pod that has not been assigned an IP yet converts
// with no IPs rather than failing; an unparseable one is an error.
func NewPod(pod *corev1.Pod) (Pod, error) {
	ips, err := podIPs(pod)
	if err != nil {
		return Pod{}, fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return Pod{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Labels:    NewLabels(pod.Labels),
		IPs:       ips,
		Original:  pod,
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
