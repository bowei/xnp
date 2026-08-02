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
	Namespace string
	Name      string
	Labels    TODO
	IPs       []netip.Addr

	Original *corev1.Pod
}
