package store

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// nsSeq is the iterator form Namespaces.Init takes, over a fixed list.
func nsSeq(nss ...*corev1.Namespace) func(yield func(*corev1.Namespace) bool) {
	return func(yield func(*corev1.Namespace) bool) {
		for _, ns := range nss {
			if !yield(ns) {
				return
			}
		}
	}
}

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

func TestNewNamespaces(t *testing.T) {
	nss := NewNamespaces()

	if got := nss.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if _, ok := nss.Get("ns1"); ok {
		t.Error("Get on an empty store = ok, want !ok")
	}
	for range nss.All() {
		t.Error("All on an empty store yielded a namespace")
	}
}

func TestNamespacesInit(t *testing.T) {
	nss := NewNamespaces()
	err := nss.Init(nsSeq(
		namespace("ns1", map[string]string{"env": "prod"}),
		namespace("ns2", nil),
	))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if got, want := nss.Len(), 2; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}

	got, ok := nss.Get("ns1")
	if !ok {
		t.Fatal("Get(ns1) = !ok, want ok")
	}
	if got.Name != "ns1" {
		t.Errorf("Get(ns1).Name = %q, want ns1", got.Name)
	}
	// Labels are compiled whole rather than projected onto the relevant keys,
	// so a key no policy names is still carried.
	if v, ok := got.Labels.Get("env"); !ok || v != "prod" {
		t.Errorf("Get(ns1).Labels[env] = %q, %v, want prod, true", v, ok)
	}

	if _, ok := nss.Get("nope"); ok {
		t.Error("Get(nope) = ok, want !ok")
	}
}

func TestNamespacesInitReplacesPreviousContents(t *testing.T) {
	nss := NewNamespaces()
	if err := nss.Init(nsSeq(namespace("old", nil))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := nss.Init(nsSeq(namespace("new", nil))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if _, ok := nss.Get("old"); ok {
		t.Error("Get(old) = ok after re-Init, want the previous contents dropped")
	}
	if _, ok := nss.Get("new"); !ok {
		t.Error("Get(new) = !ok, want ok")
	}
	if got, want := nss.Len(), 1; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
}

func TestNamespacesInitEmpty(t *testing.T) {
	nss := NewNamespaces()
	if err := nss.Init(nsSeq()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := nss.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
}

func TestNamespacesAll(t *testing.T) {
	nss := NewNamespaces()
	if err := nss.Init(nsSeq(namespace("ns1", nil), namespace("ns2", nil))); err != nil {
		t.Fatalf("Init: %v", err)
	}

	var all []string
	for ns := range nss.All() {
		all = append(all, ns.Name)
	}
	slices.Sort(all)
	if want := []string{"ns1", "ns2"}; !slices.Equal(all, want) {
		t.Errorf("All() = %v, want %v", all, want)
	}

	var seen int
	for range nss.All() {
		seen++
		break
	}
	if want := 1; seen != want {
		t.Errorf("All() with a break yielded %d, want %d", seen, want)
	}
}
