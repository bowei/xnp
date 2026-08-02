package repr

import (
	"github.com/bowei/kvec/pkg/kv"
	corev1 "k8s.io/api/core/v1"
)

// Namespace is the memory-efficient mirror of a corev1.Namespace.
type Namespace struct {
	Name   string
	Labels kv.KV

	Original *corev1.Namespace
}

// NewNamespace converts ns into repr form, precompiling its labels for
// matching against the namespaceSelectors of policy peers.
func NewNamespace(ns *corev1.Namespace) Namespace {
	return Namespace{
		Name:     ns.Name,
		Labels:   NewLabels(ns.Labels),
		Original: ns,
	}
}
