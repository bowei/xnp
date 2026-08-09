package podinternal

import (
	"fmt"
	"net/netip"

	"github.com/bowei/xnp/internal/eqc"
	corev1 "k8s.io/api/core/v1"
)

// Pod is the memory-efficient mirror of a corev1.Pod.
type Pod struct {
	Namespace string
	Name      string

	// Labels as the API carries them, uncompiled. A pod is not matched against
	// its full label set but against the projection onto the keys some policy
	// selects on (RelevantLabels), and that key set is a property of the
	// NetworkPolicy store rather than of the pod: it grows and shrinks as
	// policies come and go, and every projection has to be recomputed when it
	// does. So the pod keeps the raw labels, which are the only form the
	// projection can be recomputed from, and the compiled kv.KV lives once per
	// equivalence class instead of once per pod.
	//
	// The map is shared with the API object it was read from rather than
	// copied, so it must not be mutated.
	Labels map[string]string

	IPs []netip.Addr

	// ec is the class that this Pod belongs to. This is populated outside
	// the constructor during startup initialization and may change when
	// the set of relevant labels change.
	ec *eqc.Class
}

// Class is the equivalence class holding this pod, or nil before the pod has
// been linked to one.
func (p *Pod) Class() *eqc.Class { return p.ec }

// SetClass links the pod to the class holding pods with its relevant labels.
// The link is made after conversion, when the relevant keys are known, and
// remade whenever a change to the pod's labels or to that key set moves the pod
// to a different class.
func (p *Pod) SetClass(c *eqc.Class) { p.ec = c }

// NewPod converts pod into repr form: IPs parsed into netip.Addr, labels
// carried over as they are for RelevantLabels to project later. A pod that has
// not been assigned an IP yet converts with no IPs rather than failing; an
// unparseable one is an error.
func NewPod(pod *corev1.Pod) (Pod, error) {
	ips, err := podIPs(pod)
	if err != nil {
		return Pod{}, fmt.Errorf("pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return Pod{
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Labels:    pod.Labels,
		IPs:       ips,
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
