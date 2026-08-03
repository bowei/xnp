package repr

import (
	"github.com/bowei/kvec/pkg/kv"
	corev1 "k8s.io/api/core/v1"
)

// Namespace is the memory-efficient mirror of a corev1.Namespace.
type Namespace struct {
	Name   string
	Labels kv.KV
}

// NewNamespace converts ns into repr form, precompiling its labels for
// matching against the namespaceSelectors of policy peers.
//
// Unlike a Pod's, these labels are compiled over the whole set rather than
// projected onto the relevant keys: there is one namespace per thousands of
// pods, so the projection would save little, and matching the full set is
// always correct — the projection is an optimization, not a change of meaning.
func NewNamespace(ns *corev1.Namespace) Namespace {
	return Namespace{
		Name:   ns.Name,
		Labels: labelsToKV(ns.Labels),
	}
}
