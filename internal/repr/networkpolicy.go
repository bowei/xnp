package repr

import (
	"fmt"

	"github.com/bowei/xnp/internal/intern"
	"github.com/bowei/xnp/internal/tuple"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// NetworkPolicy is the memory-efficient mirror of a
// networkingv1.NetworkPolicy.
type NetworkPolicy struct {
	Namespace   string
	Name        string
	PodSelector metav1.LabelSelector
	PolicyTypes []networkingv1.PolicyType
	Ingress     []Rule
	Egress      []Rule

	Original *networkingv1.NetworkPolicy
}
