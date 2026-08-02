package repr

import (
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
