package repr

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewNamespace(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "team-a",
		Labels: map[string]string{"tier": "prod"},
	}}

	got := NewNamespace(ns)
	if got.Name != "team-a" {
		t.Errorf("Name = %q, want \"team-a\"", got.Name)
	}
	if v, ok := got.Labels.Get("tier"); !ok || v != "prod" {
		t.Errorf("Labels[tier] = %q, %v, want \"prod\", true", v, ok)
	}
	if got.Original != ns {
		t.Errorf("Original = %p, want %p", got.Original, ns)
	}
}
