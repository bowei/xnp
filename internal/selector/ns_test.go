package selector

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/bowei/xnp/internal/intern"
	"github.com/bowei/xnp/internal/repr"
)

func TestNamespaceSelectorCache_Match(t *testing.T) {
	tbl := intern.NewTable()
	namespaces := []repr.Namespace{
		repr.NewNamespace(nsObj("prod-a", map[string]string{"tier": "prod"}), tbl),
		repr.NewNamespace(nsObj("prod-b", map[string]string{"tier": "prod"}), tbl),
		repr.NewNamespace(nsObj("dev-a", map[string]string{"tier": "dev"}), tbl),
	}
	cache := NewNamespaceSelectorCache(namespaces, tbl)

	sel := metav1.LabelSelector{MatchLabels: map[string]string{"tier": "prod"}}
	got, err := cache.Match(sel)
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	var names []string
	for _, id := range got {
		names = append(names, tbl.String(id))
	}
	want := []string{"prod-a", "prod-b"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("Match(tier=prod) = %v, want %v", names, want)
	}

	// Second call must hit the cache and return the same result.
	got2, err := cache.Match(sel)
	if err != nil {
		t.Fatalf("Match (cached): %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(got2) {
		t.Errorf("Match called twice returned different results: %v vs %v", got, got2)
	}

	empty, err := cache.Match(metav1.LabelSelector{MatchLabels: map[string]string{"tier": "staging"}})
	if err != nil {
		t.Fatalf("Match(tier=staging): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("Match(tier=staging) = %v, want empty", empty)
	}
}

func nsObj(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}
