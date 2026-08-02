package repr

import (
	corev1 "k8s.io/api/core/v1"
)

// Namespace is the memory-efficient mirror of a corev1.Namespace.
type Namespace struct {
	Name     string
	Original *corev1.Namespace
}

// NewNamespace converts ns into repr form, interning its name/labels via tbl.
func NewNamespace(ns *corev1.Namespace) Namespace {
	// TODO
}
