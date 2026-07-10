package repr

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewNamespace(t *testing.T) {
	tbl := NewSymbolTable()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{"tier": "prod"}}}

	got := NewNamespace(ns, tbl)
	if tbl.String(got.Name) != "team-a" {
		t.Errorf("Name = %q, want \"team-a\"", tbl.String(got.Name))
	}
	tierKey := tbl.Intern("tier")
	v, ok := got.Labels.Get(tierKey)
	if !ok || tbl.String(v) != "prod" {
		t.Errorf("Labels[tier] = %q (ok=%v), want \"prod\"", tbl.String(v), ok)
	}
}
