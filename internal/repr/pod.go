package repr

import (
	"fmt"
	"net/netip"

	"github.com/bowei/xnp/internal/intern"
	"github.com/bowei/xnp/internal/labels"
	corev1 "k8s.io/api/core/v1"
)

// Pod is the memory-efficient mirror of a corev1.Pod.
type Pod struct {
	Namespace intern.ID
	Name      string
	Labels    labels.Set
	IPs       []netip.Addr

	Original *corev1.Pod
}

// NewPod converts pod into repr form, interning its namespace/labels via
// tbl. Pod IPs are drawn from Status.PodIPs (falling back to Status.PodIP),
// mirroring internal/policy's podIPs(); a pod with no IP assigned yet
// yields a nil IPs slice rather than an error.
func NewPod(pod *corev1.Pod, tbl *intern.Table) (Pod, error) {
	ips, err := parsePodIPs(pod)
	if err != nil {
		return Pod{}, fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return Pod{
		Namespace: tbl.Intern(pod.Namespace),
		Name:      pod.Name,
		Labels:    labels.Make(pod.Labels, tbl),
		IPs:       ips,
		Original:  pod,
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
